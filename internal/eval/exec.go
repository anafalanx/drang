package eval

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/anafalanx/drang/internal/value"
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
	supervise      bool          // tie the child's lifetime to ours via the reaper (see supervise.go)
}

// newCmd builds the command, wiring a deadline when a timeout is set. The returned
// ctx is non-nil only when timed; callers compare ctx.Err() to detect a timeout.
func newCmd(argv []string, o execOpts) (*exec.Cmd, context.Context, context.CancelFunc, error) {
	exe, preserveArg0, err := resolveExecPath(argv[0], o)
	if err != nil {
		return nil, nil, func() {}, err
	}
	if o.timeout > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
		cmd := exec.CommandContext(ctx, exe, argv[1:]...)
		if preserveArg0 {
			cmd.Args[0] = argv[0]
		}
		setTreeKill(cmd)
		return cmd, ctx, cancel, nil
	}
	cmd := exec.Command(exe, argv[1:]...)
	if preserveArg0 {
		cmd.Args[0] = argv[0]
	}
	return cmd, nil, func() {}, nil
}

func newUntimedCmd(argv []string, o execOpts) (*exec.Cmd, error) {
	exe, preserveArg0, err := resolveExecPath(argv[0], o)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, argv[1:]...)
	if preserveArg0 {
		cmd.Args[0] = argv[0]
	}
	return cmd, nil
}

// setTreeKill makes a timeout kill the whole process TREE, not just the direct
// child. Without it, a child that spawns its own children (e.g. `cmd /c foo`)
// leaves a grandchild holding the stdout pipe, so Wait blocks until it finishes —
// defeating the timeout. It cancels via taskkill /T; WaitDelay is a backstop so
// Wait can't hang if the kill is ignored.
func setTreeKill(cmd *exec.Cmd) {
	cmd.WaitDelay = 3 * time.Second
	cmd.Cancel = func() error { _ = killTree(cmd); return nil }
}

// killTree terminates cmd and its whole descendant tree, so a child that spawned
// its own children (a grandchild holding a pipe) can't survive. It uses taskkill /T
// (which walks the PID tree), falling back to killing the direct process if taskkill
// is unavailable. Used by the timeout, each_line-abort, and kill() paths alike.
func killTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return fmt.Errorf("process not started")
	}
	if err := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err == nil {
		return nil // tree terminated
	}
	return cmd.Process.Kill()
}

// finishErr turns a failed run into a catchable Err: a deadline hit is a timeout
// (code 124, like GNU timeout); otherwise it carries the child's exit code.
func finishErr(name string, runErr error, stderrText string, ctx context.Context, o execOpts) value.Value {
	if ctx != nil && ctx.Err() == context.DeadlineExceeded {
		return value.MakeErr(fmt.Sprintf("%s timed out after %s", name, o.timeout), 124)
	}
	return execError(name, runErr, stderrText)
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
	cmd  *exec.Cmd
	done chan struct{}
	res  value.Value // exit status: true on 0, else a catchable Err carrying the code
}

func (p *Proc) TypeName() string                           { return "process" }
func (p *Proc) Display() string                            { return fmt.Sprintf("<process %d>", p.cmd.Process.Pid) }
func (p *Proc) Len() int                                   { return 0 }
func (p *Proc) DeepCopy(map[value.Obj]value.Obj) value.Obj { return p }

func (p *Proc) Equal(o value.Obj) bool {
	other, ok := o.(*Proc)
	return ok && other == p
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
	cmd, err := newUntimedCmd(argv, opts)
	if err != nil {
		return execError(argv[0], err, ""), nil
	}
	applyExecOpts(cmd, opts) // cwd + env; stdio stays detached
	if opts.hasStdin {
		cmd.Stdin = strings.NewReader(opts.stdin)
	}
	if startErr := cmd.Start(); startErr != nil {
		return execError(argv[0], startErr, ""), nil
	}
	dereg := superviseAfterStart(cmd, opts.supervise)
	p := &Proc{cmd: cmd, done: make(chan struct{}), res: value.MakeBool(true)}
	go func() {
		defer close(p.done)
		defer dereg() // tell the reaper the child is gone once we've reaped it
		if werr := cmd.Wait(); werr != nil {
			p.res = execError(argv[0], werr, "") // exit code as a catchable Err
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
	if err := killTree(p.cmd); err != nil { // whole tree, not just the direct child
		return value.MakeErr(fmt.Sprintf("kill: %v", err), 1), nil
	}
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
	return value.MakeInt(int64(p.cmd.Process.Pid)), nil
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
	cmd, ctx, cancel, err := newCmd(argv, opts)
	if err != nil {
		return execError(argv[0], err, ""), nil
	}
	defer cancel()
	cmd.WaitDelay = 3 * time.Second // backstop: a child holding the pipe can't hang Wait forever
	cmd.Stderr = stderr
	if opts.hasStdin {
		cmd.Stdin = strings.NewReader(opts.stdin)
	}
	applyExecOpts(cmd, opts)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return value.MakeErr(fmt.Sprintf("each_line: %v", err), 1), nil
	}
	if startErr := cmd.Start(); startErr != nil {
		return execError(argv[0], startErr, ""), nil
	}
	defer superviseAfterStart(cmd, opts.supervise)() // deregister once we return (child reaped)
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // tolerate long lines
	for scanner.Scan() {
		v, cerr := callFunction(cb, []value.Value{value.MakeStr(scanner.Text())})
		if cerr != nil {
			_ = killTree(cmd) // callback aborted (exit/die) — stop the child (and its tree)
			_ = cmd.Wait()
			return value.MakeNil(), cerr
		}
		if v.IsErr() {
			_ = killTree(cmd) // callback returned/propagated an Err — stop the child, surface it
			_ = cmd.Wait()
			return v, nil
		}
	}
	if serr := scanner.Err(); serr != nil {
		// e.g. a line beyond the 4MB cap: the child is still writing into an
		// undrained pipe, so kill it (else Wait would block forever) and report the
		// scan error distinctly rather than as a bogus timeout or silent success.
		_ = killTree(cmd)
		_ = cmd.Wait()
		return value.MakeErr(fmt.Sprintf("each_line: %v", serr), 1), nil
	}
	if waitErr := cmd.Wait(); waitErr != nil {
		return finishErr(argv[0], waitErr, "", ctx, opts), nil
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
	var ctx context.Context
	cancel := func() {}
	if o.timeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), o.timeout)
	}
	defer cancel()

	cmds := make([]*exec.Cmd, n)
	for i, av := range argvs {
		exe, preserveArg0, err := resolveExecPath(av[0], o)
		if err != nil {
			return value.MakeErr(fmt.Sprintf("pipe: cannot start stage %d (%s): %v", i+1, av[0], err), 127)
		}
		if ctx != nil {
			cmds[i] = exec.CommandContext(ctx, exe, av[1:]...)
			setTreeKill(cmds[i])
		} else {
			cmds[i] = exec.Command(exe, av[1:]...)
		}
		if preserveArg0 {
			cmds[i].Args[0] = av[0]
		}
		applyExecOpts(cmds[i], o)
		cmds[i].Stderr = stderr // intermediate diagnostics stay visible
	}
	if o.hasStdin {
		cmds[0].Stdin = strings.NewReader(o.stdin)
	}

	var pipes []*os.File
	closePipes := func() {
		for _, f := range pipes {
			f.Close()
		}
	}
	for i := 0; i < n-1; i++ {
		r, w, err := os.Pipe()
		if err != nil {
			closePipes()
			return value.MakeErr(fmt.Sprintf("pipe: %v", err), 1)
		}
		cmds[i].Stdout = w
		cmds[i+1].Stdin = r
		pipes = append(pipes, r, w)
	}
	var out, lastErr bytes.Buffer
	cmds[n-1].Stdout = &out
	cmds[n-1].Stderr = &lastErr // the last stage's stderr folds into its Err, like capture

	started := 0
	for i := range cmds {
		if err := cmds[i].Start(); err != nil {
			closePipes()
			for j := 0; j < started; j++ {
				_ = cmds[j].Process.Kill()
				_ = cmds[j].Wait()
			}
			return value.MakeErr(fmt.Sprintf("pipe: cannot start stage %d (%s): %v", i+1, argvs[i][0], err), 127)
		}
		started++
	}
	// All stages started: register each for supervision (no-op when off). Registering only
	// after a full successful start means the start-failure cleanup above has nothing to undo.
	var deregs []func()
	for i := range cmds {
		deregs = append(deregs, superviseAfterStart(cmds[i], o.supervise))
	}
	closePipes() // drop the parent's copies so EOF propagates between children

	var lastWaitErr error
	for i := range cmds {
		if werr := cmds[i].Wait(); i == n-1 {
			lastWaitErr = werr
		}
	}
	for _, d := range deregs {
		d() // children reaped — clear them from the supervisor
	}
	if lastWaitErr != nil {
		return finishErr(argvs[n-1][0], lastWaitErr, lastErr.String(), ctx, o)
	}
	return value.MakeStr(strings.TrimSpace(out.String()))
}

func applyExecOpts(cmd *exec.Cmd, o execOpts) {
	if o.cwd != "" {
		cmd.Dir = o.cwd
	}
	if o.hasEnv {
		cmd.Env = o.env
	}
	if o.hasArg0 && len(cmd.Args) > 0 {
		cmd.Args[0] = o.arg0 // child sees this as argv[0]; cmd.Path still launches the real exe
	}
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

func resolveExecPath(name string, o execOpts) (string, bool, error) {
	if !o.resolveWithEnv || hasPathSeparator(name) || filepath.IsAbs(name) {
		return name, false, nil
	}
	path, _ := envLookupFold(o.env, "PATH")
	if path == "" {
		return "", false, fmt.Errorf("exec: %q: executable file not found in PATH", name)
	}
	if found, ok := lookPathInEnv(name, path, o.env); ok {
		return found, true, nil
	}
	return "", false, fmt.Errorf("exec: %q: executable file not found in PATH", name)
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
