package eval

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/anafalanx/drang/internal/value"
)

// execOpts holds the trailing {cwd, env, stdin, timeout} options for the process
// builtins.
type execOpts struct {
	cwd      string
	env      []string // nil = inherit the parent environment
	stdin    string
	hasStdin bool
	timeout  time.Duration // 0 = no limit
}

// newCmd builds the command, wiring a deadline when a timeout is set. The returned
// ctx is non-nil only when timed; callers compare ctx.Err() to detect a timeout.
func newCmd(argv []string, o execOpts) (*exec.Cmd, context.Context, context.CancelFunc) {
	if o.timeout > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		setTreeKill(cmd)
		return cmd, ctx, cancel
	}
	return exec.Command(argv[0], argv[1:]...), nil, func() {}
}

// setTreeKill makes a timeout kill the whole process TREE, not just the direct
// child. Without it, a child that spawns its own children (e.g. `cmd /c foo`)
// leaves a grandchild holding the stdout pipe, so Wait blocks until it finishes —
// defeating the timeout. On Windows that means `taskkill /T`; WaitDelay is a
// backstop so Wait can't hang if the kill is ignored.
func setTreeKill(cmd *exec.Cmd) {
	cmd.WaitDelay = 3 * time.Second
	if runtime.GOOS == "windows" {
		cmd.Cancel = func() error { _ = killTree(cmd); return nil }
	}
}

// killTree terminates cmd and its whole descendant tree, so a child that spawned
// its own children (a grandchild holding a pipe) can't survive. On Windows that is
// taskkill /T; elsewhere the direct process. Used by the timeout, each_line-abort,
// and kill() paths alike.
func killTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return fmt.Errorf("process not started")
	}
	if runtime.GOOS == "windows" {
		if err := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err == nil {
			return nil // tree terminated
		}
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
	cmd, ctx, cancel := newCmd(argv, opts)
	defer cancel()
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if opts.hasStdin {
		cmd.Stdin = strings.NewReader(opts.stdin)
	}
	applyExecOpts(cmd, opts)
	if runErr := cmd.Run(); runErr != nil {
		return finishErr(argv[0], runErr, "", ctx, opts), nil
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
	cmd := exec.Command(argv[0], argv[1:]...)
	applyExecOpts(cmd, opts) // cwd + env; stdio stays detached
	if opts.hasStdin {
		cmd.Stdin = strings.NewReader(opts.stdin)
	}
	if startErr := cmd.Start(); startErr != nil {
		return execError(argv[0], startErr, ""), nil
	}
	p := &Proc{cmd: cmd, done: make(chan struct{}), res: value.MakeBool(true)}
	go func() {
		defer close(p.done)
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
	cmd, ctx, cancel := newCmd(argv, opts)
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
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // tolerate long lines
	for scanner.Scan() {
		if _, cerr := callFunction(cb, []value.Value{value.MakeStr(scanner.Text())}); cerr != nil {
			_ = killTree(cmd) // callback aborted — stop the child (and its tree)
			_ = cmd.Wait()
			return value.MakeNil(), cerr
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
	cmd, ctx, cancel := newCmd(argv, opts)
	defer cancel()
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if opts.hasStdin {
		cmd.Stdin = strings.NewReader(opts.stdin)
	}
	applyExecOpts(cmd, opts)
	if runErr := cmd.Run(); runErr != nil {
		return finishErr(argv[0], runErr, errBuf.String(), ctx, opts), nil
	}
	return value.MakeStr(strings.TrimSpace(out.String())), nil
}

// builtinPipe runs a streaming pipeline: each stage is an array [cmd, args...],
// wired stdout->stdin through real OS pipes (no full-buffering between stages). It
// returns the LAST stage's trimmed stdout on success, or a catchable Err — code 127
// if a stage can't start, 124 on timeout, else the last stage's exit code (bash's
// default pipeline semantics; an intermediate non-zero exit is not itself an error).
// A trailing {cwd, env, stdin, timeout} map applies to the whole pipeline (stdin
// feeds the first stage).
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
		if ctx != nil {
			cmds[i] = exec.CommandContext(ctx, av[0], av[1:]...)
			setTreeKill(cmds[i])
		} else {
			cmds[i] = exec.Command(av[0], av[1:]...)
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
	closePipes() // drop the parent's copies so EOF propagates between children

	var lastWaitErr error
	for i := range cmds {
		if werr := cmds[i].Wait(); i == n-1 {
			lastWaitErr = werr
		}
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
	if o.env != nil {
		cmd.Env = o.env
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
		case "env":
			if vals[i].Tag() != value.Map {
				return o, fmt.Errorf("%s: env option must be a map", name)
			}
			o.env = mergeEnv(vals[i].Obj().(*value.OrderedMap))
		default:
			return o, fmt.Errorf("%s: unknown option %q", name, k.AsStr())
		}
	}
	return o, nil
}

// mergeEnv overlays the given key/value map onto the inherited environment,
// matching existing keys case-insensitively (Windows semantics).
func mergeEnv(overlay *value.OrderedMap) []string {
	result := append([]string(nil), os.Environ()...)
	keys, vals := overlay.Keys(), overlay.Vals()
	for i, k := range keys {
		key := k.Display()
		entry := key + "=" + vals[i].Display()
		replaced := false
		for j, e := range result {
			if eq := strings.IndexByte(e, '='); eq >= 0 && strings.EqualFold(e[:eq], key) {
				result[j] = entry
				replaced = true
				break
			}
		}
		if !replaced {
			result = append(result, entry)
		}
	}
	return result
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
