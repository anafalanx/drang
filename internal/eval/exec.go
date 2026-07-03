package eval

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anafalanx/drang/internal/value"
	"github.com/anafalanx/drang/internal/winjob"
)

// execOpts holds the trailing {cwd, env_exact, env_add, stdin, timeout}options for
// the process builtins.
type execOpts struct {
	cwd            string
	env            []string // set when the child should not inherit implicitly
	hasEnv         bool     // true even when env is intentionally empty
	resolveWithEnv bool     // resolve bare commands against env's PATH
	stdin          string
	hasStdin       bool
	stdinFile      string // feed the child's stdin from this file (zero-copy), instead of {stdin}
	hasStdinFile   bool
	stdinPipe      bool   // start() only: keep a writable stdin so send_stdin/close_stdin can drive the child
	mergeStderr    bool   // route the child's stderr to its stdout (2>&1)
	arg0           string // present a different argv[0] than the launched executable
	hasArg0        bool
	timeout        time.Duration // 0 = no wall-clock limit
	supervise      bool          // start(): tie a detached child's lifetime to ours (KILL_ON_JOB_CLOSE)
	limits         winjob.Limits // kernel-enforced resource caps (memory/CPU/process-count); zero = uncapped
}

// limitInt parses a resource-limit option: a non-negative int (0 = uncapped, like timeout).
func limitInt(name, key string, v value.Value) (int64, error) {
	if v.Tag() != value.Int {
		return 0, fmt.Errorf("%s: %s must be an int", name, key)
	}
	n := v.AsInt()
	if n < 0 {
		return 0, fmt.Errorf("%s: %s must be >= 0", name, key)
	}
	return n, nil
}

// builtinRun spawns a command with inherited stdio, returning true on success or
// a catchable Err carrying the child's exit code on a non-zero exit (127 if it
// could not be started). No shell — args are passed verbatim.
func builtinRun(args []value.Value) (value.Value, error) {
	argv, opts, err := splitExecArgs("run", args)
	if err != nil {
		return value.MakeNil(), err
	}
	// stdout/stderr are process-wide shared writers; serialize their copiers (see lockedShared) so
	// concurrent pmap workers running run() can't race the common sink. os.Stdout/os.Stderr are
	// *os.File and pass through unwrapped to the lock-free direct-descriptor path.
	c, err := newJobCmd(argv, opts, os.Stdin, lockedShared(stdout), lockedShared(stderr))
	if err != nil {
		return execError(argv[0], err, ""), nil
	}
	if startErr := c.start(); startErr != nil {
		return execError(argv[0], startErr, ""), nil
	}
	code, timedOut, werr := c.wait()
	switch {
	case timedOut:
		return value.MakeErr(fmt.Sprintf("%s timed out after %s", argv[0], opts.timeout), 124), nil
	case c.limitHit.Load() != nil:
		b, _ := c.breachErr(argv[0])
		return b, nil
	case werr != nil:
		return value.MakeErr(fmt.Sprintf("run: %v", werr), 1), nil
	case code != 0:
		return execErrCode(argv[0], code, ""), nil
	}
	return value.MakeBool(true), nil // truthy success, composes with // and if
}

// Proc is a handle to a started external process — the process analogue of Task.
// It is an intentionally SHARED reference (DeepCopy returns itself); a goroutine
// reaps the child and records its exit status, which await(proc) reads.
type Proc struct {
	mu     sync.Mutex  // guards job: kill's Terminate vs. the reaping goroutine's Close
	job    *winjob.Job // the command job — Terminate kills the tree, Close releases; nil once closed
	pid    int
	done   chan struct{}
	res    value.Value // exit status: true on 0, else a catchable Err carrying the code
	killed atomic.Bool // set by kill() so the reaper reports "was killed", not a bare exit code

	stdinMu     sync.Mutex // guards the writable-stdin handle below
	stdinW      *os.File   // writable stdin, set when started with {stdin_pipe: true}; nil otherwise
	stdinClosed bool
}

// writeStdin feeds s to a process started with {stdin_pipe: true}. It errors if the process has no
// writable stdin or its stdin was already closed (or the child closed its read end).
func (p *Proc) writeStdin(s string) error {
	p.stdinMu.Lock()
	defer p.stdinMu.Unlock()
	if p.stdinW == nil {
		return fmt.Errorf("process was not started with {stdin_pipe: true}")
	}
	if p.stdinClosed {
		return fmt.Errorf("stdin is already closed")
	}
	_, err := p.stdinW.WriteString(s)
	return err
}

// closeStdin closes the writable stdin (EOF for the child). Idempotent, and a no-op when the
// process has no writable stdin.
func (p *Proc) closeStdin() {
	p.stdinMu.Lock()
	defer p.stdinMu.Unlock()
	if p.stdinW != nil && !p.stdinClosed {
		p.stdinClosed = true
		p.stdinW.Close()
	}
}

func (p *Proc) TypeName() string                           { return "process" }
func (p *Proc) Display() string                            { return fmt.Sprintf("<process %d>", p.pid) }
func (p *Proc) Len() int                                   { return 0 }
func (p *Proc) DeepCopy(map[value.Obj]value.Obj) value.Obj { return p }

func (p *Proc) Equal(o value.Obj) bool {
	other, ok := o.(*Proc)
	return ok && other == p
}

// terminate kills the process and its whole tree via the command job — idempotent, and safe
// against the reaping goroutine's closeJob.
func (p *Proc) terminate() {
	p.killed.Store(true) // so the reaper can distinguish "I killed it" from a natural exit
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.job != nil {
		_ = p.job.Terminate(1)
	}
}

// closeJob releases drang's handle to the command job once the child is reaped. For a
// non-supervised (not KILL_ON_JOB_CLOSE) job this just drops our reference; a still-living child
// outlives drang.
func (p *Proc) closeJob() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.job != nil {
		p.job.Close()
		p.job = nil
	}
}

// builtinStart launches a command WITHOUT waiting — a detached background/GUI child
// (the drang equivalent of `exec cmd &`). It returns a process handle for
// await/kill/pid, or a catchable Err (code 127) if the command cannot be started.
// Stdio is detached (not wired to the terminal); a goroutine reaps the child and
// records its exit status. Distinct from spawn, which runs a drang function.
func builtinStart(args []value.Value) (value.Value, error) {
	argv, opts, err := splitExecArgs("start", args)
	if err != nil {
		return value.MakeNil(), err
	}
	if opts.timeout > 0 {
		// A started process is detached and runs unbounded; {timeout} cannot apply. Reject it
		// rather than silently ignore it. (Use run/capture for a bounded command.)
		return value.MakeErr("start does not accept {timeout}: a started process is detached and runs unbounded", 1), nil
	}
	// Detached: stdio goes to the null device (not the terminal), like `exec cmd &`.
	c, err := newJobCmd(argv, opts, nil, nil, nil)
	if err != nil {
		return execError(argv[0], err, ""), nil
	}
	// {stdin_pipe}: keep a writable pipe to the child's stdin so send_stdin/close_stdin can drive
	// it. The child reads the read end (closed after launch); drang holds the write end.
	var stdinW *os.File
	if opts.stdinPipe {
		pr, pw, perr := os.Pipe()
		if perr != nil {
			return execError(argv[0], perr, ""), nil
		}
		c.ownedStdin = pr
		stdinW = pw
	}
	c.killOnClose = opts.supervise // supervise:true ties it to drang's life; else it outlives drang
	if startErr := c.start(); startErr != nil {
		if stdinW != nil {
			stdinW.Close()
		}
		return execError(argv[0], startErr, ""), nil
	}
	p := &Proc{job: c.job, pid: c.proc.Pid(), done: make(chan struct{}), res: value.MakeBool(true), stdinW: stdinW}
	go func() {
		defer close(p.done)
		code, werr := c.proc.Wait() // reap the child; the NUL stdio needs no draining
		c.stopMonitor()             // finalize limitHit before reading breachErr
		p.closeStdin()              // release the write end once the child is gone
		p.closeJob()
		switch {
		case p.killed.Load():
			p.res = value.MakeErr(fmt.Sprintf("%s was killed", argv[0]), 137)
		case c.limitHit.Load() != nil:
			b, _ := c.breachErr(argv[0])
			p.res = b
		case werr != nil:
			p.res = value.MakeErr(fmt.Sprintf("%s: %v", argv[0], werr), 1)
		case code != 0:
			p.res = execErrCode(argv[0], code, "")
		}
	}()
	return value.MakeObj(value.Proc, p), nil
}

func procArg(name string, v value.Value) (*Proc, value.Value, bool) {
	if v.Tag() != value.Proc {
		return nil, value.MakeErr(fmt.Sprintf("%s expects a process, got %s", name, v.TypeName()), 1), false
	}
	return v.Obj().(*Proc), value.MakeNil(), true
}

// builtinKill terminates a started process; its pending await then yields the Err.
func builtinKill(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("kill expects 1 argument (a process), got %d", len(args))
	}
	p, errv, ok := procArg("kill", args[0])
	if !ok {
		return errv, nil
	}
	p.terminate() // whole tree, not just the direct child; idempotent if already gone
	return value.MakeBool(true), nil
}

// builtinStatus polls a started process WITHOUT blocking (unlike await): it returns
// {running: true} while the child is alive, else {running: false, ok, code} — the same
// ok/code shape as capture_all (code 0 on success, else the exit/breach/killed code). This is
// how you supervise a background child without committing to a blocking wait.
func builtinStatus(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("status expects 1 argument (a process), got %d", len(args))
	}
	p, errv, ok := procArg("status", args[0])
	if !ok {
		return errv, nil
	}
	m := value.MakeMap()
	om := m.Obj().(*value.OrderedMap)
	select {
	case <-p.done: // reaped: p.res is final (set before close(p.done))
		om.Set(value.MakeStr("running"), value.MakeBool(false))
		if p.res.IsErr() {
			om.Set(value.MakeStr("ok"), value.MakeBool(false))
			om.Set(value.MakeStr("code"), value.MakeInt(p.res.ErrCode()))
		} else {
			om.Set(value.MakeStr("ok"), value.MakeBool(true))
			om.Set(value.MakeStr("code"), value.MakeInt(0))
		}
	default:
		om.Set(value.MakeStr("running"), value.MakeBool(true))
	}
	return m, nil
}

// builtinSendStdin writes to the stdin of a process started with {stdin_pipe: true} — the way to
// drive a live child after launch (a prompt answer, a stream of commands). Returns true, or a
// catchable Err if the process has no writable stdin or the child closed its read end.
func builtinSendStdin(args []value.Value) (value.Value, error) {
	if len(args) != 2 {
		return value.MakeNil(), fmt.Errorf("send_stdin expects 2 arguments (process, string), got %d", len(args))
	}
	p, errv, ok := procArg("send_stdin", args[0])
	if !ok {
		return errv, nil
	}
	if err := p.writeStdin(args[1].Display()); err != nil {
		return value.MakeErr(fmt.Sprintf("send_stdin: %v", err), 1), nil
	}
	return value.MakeBool(true), nil
}

// builtinCloseStdin closes a started process's writable stdin (sends EOF), so a child that reads
// until EOF can finish. Idempotent; a no-op for a process without a writable stdin.
func builtinCloseStdin(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("close_stdin expects 1 argument (a process), got %d", len(args))
	}
	p, errv, ok := procArg("close_stdin", args[0])
	if !ok {
		return errv, nil
	}
	p.closeStdin()
	return value.MakeBool(true), nil
}

// builtinPid returns a started process's PID.
func builtinPid(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("pid expects 1 argument (a process), got %d", len(args))
	}
	p, errv, ok := procArg("pid", args[0])
	if !ok {
		return errv, nil
	}
	return value.MakeInt(int64(p.pid)), nil
}

// evalStreamLines runs a command and invokes a callback with each line of its
// stdout AS IT STREAMS (not buffered) — for long-running or high-volume tools
// (build logs, tails); the "stream vs buffer" counterpart of capture. Shape:
// stream_lines(cmd, args..., {opts}?, |$line| ...). It returns true on success or
// a catchable Err (exit code / 124 timeout), after the command finishes. stderr
// stays on the terminal. It is a special form (like spawn and the HOFs) rather
// than a map builtin, because calling callFunction from a builtins-map entry
// would form a package initialization cycle.
func evalStreamLines(args []value.Value, depth int) (value.Value, error) {
	if len(args) < 2 {
		return value.MakeNil(), fmt.Errorf("stream_lines expects a command and a callback")
	}
	cb, ok := asFunction(args[len(args)-1])
	if !ok {
		return value.MakeNil(), fmt.Errorf("stream_lines: last argument must be a function, got %s", args[len(args)-1].TypeName())
	}
	argv, opts, err := splitExecArgs("stream_lines", args[:len(args)-1])
	if err != nil {
		return value.MakeNil(), err
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return value.MakeErr(fmt.Sprintf("stream_lines: %v", err), 1), nil
	}
	c, err := newJobCmd(argv, opts, nil, pw, lockedShared(stderr)) // stdout -> a pipe we scan; stderr stays on the terminal (serialized)
	if err != nil {
		pr.Close()
		pw.Close()
		return execError(argv[0], err, ""), nil
	}
	if startErr := c.start(); startErr != nil {
		pr.Close()
		pw.Close()
		return execError(argv[0], startErr, ""), nil
	}
	pw.Close() // the parent's copy; the child has its own, so pr EOFs when the child exits

	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // tolerate long lines
	var cbErr error                                       // a callback exit/die to propagate
	var abortVal value.Value                              // a callback-returned Err to surface
	aborted := false
	for scanner.Scan() {
		v, cerr := callFunction(cb, []value.Value{value.MakeStr(scanner.Text())}, depth)
		if cerr != nil {
			c.killTree() // callback aborted (exit/die) — stop the child and its tree
			cbErr = cerr
			break
		}
		if v.IsErr() {
			c.killTree() // callback returned/propagated an Err — stop the child, surface it
			abortVal, aborted = v, true
			break
		}
	}
	scanErr := scanner.Err()
	if scanErr != nil && cbErr == nil && !aborted {
		// e.g. a line beyond the 4MB cap: the child is still writing into an undrained pipe, so kill
		// it (else wait would block) and report the scan error distinctly.
		c.killTree()
	}
	pr.Close()
	code, timedOut, werr := c.wait()

	switch {
	case cbErr != nil:
		return value.MakeNil(), cbErr
	case aborted:
		return abortVal, nil
	case scanErr != nil:
		return value.MakeErr(fmt.Sprintf("stream_lines: %v", scanErr), 1), nil
	case timedOut:
		return value.MakeErr(fmt.Sprintf("%s timed out after %s", argv[0], opts.timeout), 124), nil
	case c.limitHit.Load() != nil:
		b, _ := c.breachErr(argv[0])
		return b, nil
	case werr != nil:
		return value.MakeErr(fmt.Sprintf("stream_lines: %v", werr), 1), nil
	case code != 0:
		return execErrCode(argv[0], code, ""), nil
	}
	return value.MakeBool(true), nil
}

// builtinCapture spawns a command capturing stdout, returning the trimmed stdout
// string on success or a catchable Err (with the child's stderr folded into the
// message) on failure.
func builtinCapture(args []value.Value) (value.Value, error) {
	argv, opts, err := splitExecArgs("capture", args)
	if err != nil {
		return value.MakeNil(), err
	}
	var out, errBuf bytes.Buffer
	c, err := newJobCmd(argv, opts, nil, &out, &errBuf)
	if err != nil {
		return execError(argv[0], err, ""), nil
	}
	if startErr := c.start(); startErr != nil {
		return execError(argv[0], startErr, ""), nil
	}
	code, timedOut, werr := c.wait()
	switch {
	case timedOut:
		return value.MakeErr(fmt.Sprintf("%s timed out after %s", argv[0], opts.timeout), 124), nil
	case c.limitHit.Load() != nil:
		b, _ := c.breachErr(argv[0])
		return b, nil
	case werr != nil:
		return value.MakeErr(fmt.Sprintf("capture: %v", werr), 1), nil
	case code != 0:
		return execErrCode(argv[0], code, errBuf.String()), nil
	}
	return value.MakeStr(strings.TrimSpace(out.String())), nil
}

// builtinCaptureAll runs a command and ALWAYS returns a record
// {out, err, code, ok} (stdout, stderr, exit code, success) — a non-zero exit is
// data to inspect, not a thrown Err. (capture() is the "give me stdout or fail"
// form; capture_all is the "tell me everything" form, like Open3.capture3.)
// code is 124 on timeout and 127 when the command can't start.
func builtinCaptureAll(args []value.Value) (value.Value, error) {
	argv, opts, err := splitExecArgs("capture_all", args)
	if err != nil {
		return value.MakeNil(), err
	}
	var out, errBuf bytes.Buffer
	code := 0
	c, cerr := newJobCmd(argv, opts, nil, &out, &errBuf)
	if cerr != nil {
		code = 127 // could not resolve the command
	} else if startErr := c.start(); startErr != nil {
		code = 127 // could not start
	} else {
		ec, timedOut, werr := c.wait()
		switch {
		case timedOut:
			code = 124
		case c.limitHit.Load() != nil:
			code = 137 // a resource cap was breached
		case werr != nil:
			code = 127 // a wait/system failure
		default:
			code = ec // Windows exit codes are non-negative
		}
	}
	rec := value.MakeMap()
	om := rec.Obj().(*value.OrderedMap)
	om.Set(value.MakeStr("out"), value.MakeStr(strings.TrimSpace(out.String())))
	om.Set(value.MakeStr("err"), value.MakeStr(strings.TrimSpace(errBuf.String())))
	om.Set(value.MakeStr("code"), value.MakeInt(int64(code)))
	om.Set(value.MakeStr("ok"), value.MakeBool(code == 0))
	return rec, nil
}

// builtinPipe runs a streaming pipeline: each stage is an array [cmd, args...],
// wired stdout->stdin through real OS pipes (no full-buffering between stages). It
// returns the LAST stage's trimmed stdout on success, or a catchable Err — code 127
// if a stage can't start, 124 on timeout, else the last stage's exit code (bash's
// default pipeline semantics; an intermediate non-zero exit is not itself an error).
// A trailing {cwd, env_exact, env_add, stdin, timeout}map applies to the whole
// pipeline (stdin feeds the first stage).
func builtinPipe(args []value.Value) (value.Value, error) {
	var opts execOpts
	stages := args
	if n := len(stages); n > 0 && stages[n-1].Tag() == value.Map {
		o, err := execOptions("pipe", stages[n-1].Obj().(*value.OrderedMap))
		if err != nil {
			return value.MakeNil(), err
		}
		opts = o
		stages = stages[:n-1]
	}
	if len(stages) == 0 {
		return value.MakeNil(), fmt.Errorf("pipe expects at least one stage [cmd, args...]")
	}
	argvs := make([][]string, len(stages))
	for i, s := range stages {
		if s.Tag() != value.Arr {
			return value.MakeNil(), fmt.Errorf("pipe: stage %d must be an array [cmd, args...], got %s", i+1, s.TypeName())
		}
		av, err := execArgStrings("pipe", []value.Value{s})
		if err != nil {
			return value.MakeNil(), err
		}
		if len(av) == 0 {
			return value.MakeNil(), fmt.Errorf("pipe: stage %d is empty", i+1)
		}
		argvs[i] = av
	}
	return runPipeline(argvs, opts), nil
}

func runPipeline(argvs [][]string, o execOpts) value.Value {
	n := len(argvs)
	stages := make([]*jobCmd, n)
	var out, lastErr bytes.Buffer

	for i, av := range argvs {
		stderrW := lockedShared(stderr) // intermediate diagnostics stay visible; serialize the shared sink
		if i == n-1 {
			stderrW = &lastErr // the last stage's stderr folds into its Err, like capture (a private buffer)
		}
		c, err := newJobCmd(av, o, nil, nil, stderrW)
		if err != nil {
			return value.MakeErr(fmt.Sprintf("pipe: cannot start stage %d (%s): %v", i+1, av[0], err), 127)
		}
		stages[i] = c
	}
	if o.hasStdin {
		stages[0].stdin = strings.NewReader(o.stdin)
	}
	stages[n-1].stdout = &out

	// Inter-stage pipes: stage[i].stdout -> stage[i+1].stdin, wired as raw files (used directly).
	var pipes []*os.File
	closePipes := func() {
		for _, f := range pipes {
			f.Close()
		}
	}
	for i := 0; i < n-1; i++ {
		pr, pw, err := os.Pipe()
		if err != nil {
			closePipes()
			return value.MakeErr(fmt.Sprintf("pipe: %v", err), 1)
		}
		stages[i].stdout = pw
		stages[i+1].stdin = pr
		pipes = append(pipes, pr, pw)
	}

	started := 0
	for i := range stages {
		if err := stages[i].start(); err != nil {
			closePipes()
			for j := 0; j < started; j++ {
				stages[j].killTree()
				stages[j].wait()
			}
			return value.MakeErr(fmt.Sprintf("pipe: cannot start stage %d (%s): %v", i+1, argvs[i][0], err), 127)
		}
		started++
	}
	closePipes() // drop the parent's inter-stage copies so EOF propagates between children

	// Wait every stage (each must be reaped and its job closed). A timeout or system error in ANY
	// stage governs the pipeline — an intermediate stage that is tree-killed on timeout would let a
	// downstream stage see a clean EOF and exit 0, so keying only off the last stage (bash's exit
	// semantics) would silently swallow the timeout. Absent those, the last stage's exit code is the
	// pipeline's status, per bash.
	lastCode := 0
	timedOutStage := -1
	breachStage := -1
	var werr error
	werrStage := -1
	for i := range stages {
		cd, to, we := stages[i].wait()
		if to && timedOutStage < 0 {
			timedOutStage = i
		}
		if stages[i].limitHit.Load() != nil && breachStage < 0 {
			breachStage = i
		}
		if we != nil && werr == nil {
			werr, werrStage = we, i
		}
		if i == n-1 { // bash pipeline semantics: the last stage's status is the pipeline's
			lastCode = cd
		}
	}
	switch {
	case timedOutStage >= 0:
		return value.MakeErr(fmt.Sprintf("%s timed out after %s", argvs[timedOutStage][0], o.timeout), 124)
	case breachStage >= 0:
		b, _ := stages[breachStage].breachErr(argvs[breachStage][0])
		return b
	case werr != nil:
		return value.MakeErr(fmt.Sprintf("pipe: stage %d (%s): %v", werrStage+1, argvs[werrStage][0], werr), 1)
	case lastCode != 0:
		return execErrCode(argvs[n-1][0], lastCode, lastErr.String())
	}
	return value.MakeStr(strings.TrimSpace(out.String()))
}

// splitExecArgs peels a trailing options map, then flattens the remaining args
// one level (arrays splice; scalars stringify) into the command words. Zero
// command words is an aborting (arity) error.
func splitExecArgs(name string, args []value.Value) ([]string, execOpts, error) {
	var opts execOpts
	raw := args
	if n := len(raw); n > 0 && raw[n-1].Tag() == value.Map {
		o, err := execOptions(name, raw[n-1].Obj().(*value.OrderedMap))
		if err != nil {
			return nil, opts, err
		}
		opts = o
		raw = raw[:n-1]
	}
	argv, err := execArgStrings(name, raw)
	if err != nil {
		return nil, opts, err
	}
	if len(argv) == 0 {
		return nil, opts, fmt.Errorf("%s expects at least a command", name)
	}
	return argv, opts, nil
}

func execArgStrings(name string, raw []value.Value) ([]string, error) {
	var out []string
	for _, v := range raw {
		switch v.Tag() {
		case value.Str:
			out = append(out, v.AsStr())
		case value.Int, value.Float, value.Bool:
			out = append(out, v.Display())
		case value.Arr:
			for _, e := range v.Obj().(*value.Array).Elems {
				switch e.Tag() {
				case value.Str:
					out = append(out, e.AsStr())
				case value.Int, value.Float, value.Bool:
					out = append(out, e.Display())
				default:
					return nil, fmt.Errorf("%s: cannot use a %s as a command argument", name, e.TypeName())
				}
			}
		default:
			return nil, fmt.Errorf("%s: cannot use a %s as a command argument", name, v.TypeName())
		}
	}
	return out, nil
}

func execOptions(name string, m *value.OrderedMap) (execOpts, error) {
	var o execOpts
	var exactEnv, overlayEnv *value.OrderedMap
	keys, vals := m.Keys(), m.Vals()
	for i, k := range keys {
		if k.Tag() != value.Str {
			return o, fmt.Errorf("%s: option keys must be strings", name)
		}
		switch k.AsStr() {
		case "cwd":
			o.cwd = vals[i].Display()
		case "stdin":
			o.stdin = vals[i].Display()
			o.hasStdin = true
		case "stdin_file":
			if vals[i].Tag() != value.Str {
				return o, fmt.Errorf("%s: stdin_file must be a string path", name)
			}
			o.stdinFile = vals[i].AsStr()
			o.hasStdinFile = true
		case "merge_stderr":
			if vals[i].Tag() != value.Bool {
				return o, fmt.Errorf("%s: merge_stderr must be true or false", name)
			}
			o.mergeStderr = vals[i].Truthy()
		case "stdin_pipe":
			if name != "start" {
				return o, fmt.Errorf("%s: stdin_pipe is only for start() (send_stdin/close_stdin drive a started process)", name)
			}
			if vals[i].Tag() != value.Bool {
				return o, fmt.Errorf("%s: stdin_pipe must be true or false", name)
			}
			o.stdinPipe = vals[i].Truthy()
		case "arg0":
			o.arg0 = vals[i].Display()
			o.hasArg0 = true
		case "timeout":
			if vals[i].Tag() != value.Int {
				return o, fmt.Errorf("%s: timeout must be an int (milliseconds)", name)
			}
			ms := vals[i].AsInt()
			if ms < 0 {
				return o, fmt.Errorf("%s: timeout must be >= 0 milliseconds", name)
			}
			if ms > 0 { // 0 = no limit
				o.timeout = time.Duration(ms) * time.Millisecond
			}
		case "env_exact":
			if vals[i].Tag() != value.Map {
				return o, fmt.Errorf("%s: env_exact option must be a map", name)
			}
			exactEnv = vals[i].Obj().(*value.OrderedMap)
		case "env":
			return o, fmt.Errorf("%s: 'env' is not an option; use 'env_exact' for an exact replacement, or 'env_add' to overlay onto the inherited environment", name)
		case "env_add":
			if vals[i].Tag() != value.Map {
				return o, fmt.Errorf("%s: env_add option must be a map", name)
			}
			overlayEnv = vals[i].Obj().(*value.OrderedMap)
		case "supervise":
			if vals[i].Tag() != value.Bool {
				return o, fmt.Errorf("%s: supervise must be true or false", name)
			}
			o.supervise = vals[i].Truthy()
		case "max_memory": // per-process commit cap, BYTES
			n, err := limitInt(name, "max_memory", vals[i])
			if err != nil {
				return o, err
			}
			o.limits.ProcessMemoryBytes = uint64(n)
		case "max_job_memory": // whole-tree commit cap, BYTES
			n, err := limitInt(name, "max_job_memory", vals[i])
			if err != nil {
				return o, err
			}
			o.limits.JobMemoryBytes = uint64(n)
		case "max_cpu": // per-process user CPU time, MILLISECONDS (like timeout's unit)
			n, err := limitInt(name, "max_cpu", vals[i])
			if err != nil {
				return o, err
			}
			o.limits.ProcessCPUTime = time.Duration(n) * time.Millisecond
		case "max_job_cpu": // whole-job user CPU time, MILLISECONDS
			n, err := limitInt(name, "max_job_cpu", vals[i])
			if err != nil {
				return o, err
			}
			o.limits.JobCPUTime = time.Duration(n) * time.Millisecond
		case "max_job_procs": // max concurrent processes in the whole job (count)
			n, err := limitInt(name, "max_job_procs", vals[i])
			if err != nil {
				return o, err
			}
			o.limits.ActiveProcessCap = uint32(n)
		default:
			return o, fmt.Errorf("%s: unknown option %q", name, k.AsStr())
		}
	}
	if o.stdinPipe && (o.hasStdin || o.hasStdinFile) {
		return o, fmt.Errorf("%s: stdin_pipe cannot combine with stdin/stdin_file (it IS the stdin)", name)
	}
	if exactEnv != nil && overlayEnv != nil {
		return o, fmt.Errorf("%s: env_exact and env_add are mutually exclusive", name)
	}
	if exactEnv != nil {
		o.env = buildEnv(exactEnv, false)
		o.hasEnv = true
		o.resolveWithEnv = true
	} else if overlayEnv != nil {
		o.env = buildEnv(overlayEnv, true)
		o.hasEnv = true
		o.resolveWithEnv = true
	}
	return o, nil
}

// mergeEnv overlays the given key/value map onto the inherited environment, matching existing
// keys case-insensitively (Windows env-var names; see envKeyEqual).
func mergeEnv(overlay *value.OrderedMap) []string {
	return buildEnv(overlay, true)
}

func buildEnv(overlay *value.OrderedMap, inherit bool) []string {
	result := []string{}
	if inherit {
		result = append(result, os.Environ()...)
	}
	if overlay == nil {
		return result
	}
	keys, vals := overlay.Keys(), overlay.Vals()
	for i, k := range keys {
		key := k.Display()
		result = setEnvFold(result, key, vals[i].Display())
	}
	return result
}

// envKeyEqual reports whether two environment-variable names denote the same variable. Windows
// env-var names are case-insensitive, so names are compared case-folded.
func envKeyEqual(a, b string) bool { return strings.EqualFold(a, b) }

func setEnvFold(env []string, key, val string) []string {
	entry := key + "=" + val
	for i, e := range env {
		if eq := strings.IndexByte(e, '='); eq >= 0 && envKeyEqual(e[:eq], key) {
			env[i] = entry
			return env
		}
	}
	return append(env, entry)
}

func envLookupFold(env []string, key string) (string, bool) {
	for _, e := range env {
		if eq := strings.IndexByte(e, '='); eq >= 0 && envKeyEqual(e[:eq], key) {
			return e[eq+1:], true
		}
	}
	return "", false
}

func hasPathSeparator(s string) bool {
	return strings.ContainsAny(s, `\/`)
}

func lookPathInEnv(name, path string, env []string) (string, bool) {
	exts := []string{""}
	if filepath.Ext(name) == "" {
		pathext, ok := envLookupFold(env, "PATHEXT")
		if !ok || strings.TrimSpace(pathext) == "" {
			pathext = ".COM;.EXE;.BAT;.CMD"
		}
		exts = strings.Split(pathext, string(os.PathListSeparator))
		for i, ext := range exts {
			if ext != "" && !strings.HasPrefix(ext, ".") {
				exts[i] = "." + ext
			}
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = "."
		}
		for _, ext := range exts {
			cand := filepath.Join(dir, name+ext)
			if isExecutableFile(cand) {
				return cand, true
			}
		}
	}
	return "", false
}

func isExecutableFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// execError converts an os/exec failure into a catchable Err value: a child that
// ran carries its exit code; a child that could not start carries code 127.
func execError(cmd string, err error, stderrText string) value.Value {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code := int64(ee.ExitCode())
		if code < 1 {
			code = 1
		}
		msg := fmt.Sprintf("%s exited with code %d", cmd, code)
		if s := strings.TrimSpace(stderrText); s != "" {
			msg += ": " + s
		}
		return value.MakeErr(msg, code)
	}
	return value.MakeErr(fmt.Sprintf("%s: %v", cmd, err), 127)
}
