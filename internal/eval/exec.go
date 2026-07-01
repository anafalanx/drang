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
	arg0           string // present a different argv[0] than the launched executable
	hasArg0        bool
	timeout        time.Duration // 0 = no limit
	supervise      bool          // start(): tie a detached child's lifetime to ours (KILL_ON_JOB_CLOSE)
}

// builtinRun spawns a command with inherited stdio, returning true on success or
// a catchable Err carrying the child's exit code on a non-zero exit (127 if it
// could not be started). No shell — args are passed verbatim.
func builtinRun(args []value.Value) (value.Value, error) {
	argv, opts, err := splitExecArgs("run", args)
	if err != nil {
		return value.MakeNil(), err
	}
	c, err := newJobCmd(argv, opts, os.Stdin, stdout, stderr)
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
	mu   sync.Mutex  // guards job: kill's Terminate vs. the reaping goroutine's Close
	job  *winjob.Job // the command job — Terminate kills the tree, Close releases; nil once closed
	pid  int
	done chan struct{}
	res  value.Value // exit status: true on 0, else a catchable Err carrying the code
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
	// Detached: stdio goes to the null device (not the terminal), like `exec cmd &`.
	c, err := newJobCmd(argv, opts, nil, nil, nil)
	if err != nil {
		return execError(argv[0], err, ""), nil
	}
	c.timeout = 0                  // a detached process is not bounded by the {timeout} option
	c.killOnClose = opts.supervise // supervise:true ties it to drang's life; else it outlives drang
	if startErr := c.start(); startErr != nil {
		return execError(argv[0], startErr, ""), nil
	}
	p := &Proc{job: c.job, pid: c.proc.Pid(), done: make(chan struct{}), res: value.MakeBool(true)}
	go func() {
		defer close(p.done)
		code, werr := c.proc.Wait() // reap the child; the NUL stdio needs no draining
		p.closeJob()
		switch {
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

// builtinEachLine runs a command and invokes a callback with each line of its
// stdout AS IT STREAMS (not buffered) — for long-running or high-volume tools
// (build logs, tails). Shape: each_line(cmd, args..., {opts}?, |$line| ...). It
// returns true on success or a catchable Err (exit code / 124 timeout), after the
// command finishes. stderr stays on the terminal. It is a special form (like spawn
// and the HOFs) rather than a map builtin, because calling callFunction from a
// builtins-map entry would form a package initialization cycle.
func evalEachLine(args []value.Value) (value.Value, error) {
	if len(args) < 2 {
		return value.MakeNil(), fmt.Errorf("each_line expects a command and a callback")
	}
	cb, ok := asFunction(args[len(args)-1])
	if !ok {
		return value.MakeNil(), fmt.Errorf("each_line: last argument must be a function, got %s", args[len(args)-1].TypeName())
	}
	argv, opts, err := splitExecArgs("each_line", args[:len(args)-1])
	if err != nil {
		return value.MakeNil(), err
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return value.MakeErr(fmt.Sprintf("each_line: %v", err), 1), nil
	}
	c, err := newJobCmd(argv, opts, nil, pw, stderr) // stdout -> a pipe we scan; stderr stays on the terminal
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
		v, cerr := callFunction(cb, []value.Value{value.MakeStr(scanner.Text())})
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
		return value.MakeErr(fmt.Sprintf("each_line: %v", scanErr), 1), nil
	case timedOut:
		return value.MakeErr(fmt.Sprintf("%s timed out after %s", argv[0], opts.timeout), 124), nil
	case werr != nil:
		return value.MakeErr(fmt.Sprintf("each_line: %v", werr), 1), nil
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
		stderrW := stderr // intermediate diagnostics stay visible
		if i == n-1 {
			stderrW = &lastErr // the last stage's stderr folds into its Err, like capture
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

	var code int
	var timedOut bool
	var werr error
	for i := range stages {
		cd, to, we := stages[i].wait()
		if i == n-1 { // bash pipeline semantics: the last stage's status is the pipeline's
			code, timedOut, werr = cd, to, we
		}
	}
	switch {
	case timedOut:
		return value.MakeErr(fmt.Sprintf("%s timed out after %s", argvs[n-1][0], o.timeout), 124)
	case werr != nil:
		return value.MakeErr(fmt.Sprintf("pipe: %v", werr), 1)
	case code != 0:
		return execErrCode(argvs[n-1][0], code, lastErr.String())
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
		default:
			return o, fmt.Errorf("%s: unknown option %q", name, k.AsStr())
		}
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
