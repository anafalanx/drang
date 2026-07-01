package eval

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/anafalanx/drang/internal/value"
	"github.com/anafalanx/drang/internal/winjob"
)

// jobCmd spawns a command via winjob into a per-command Job Object (born-in-job: die-with-parent +
// race-free whole-tree kill), wiring io.Reader/io.Writer stdio through the same file-or-pipe-and-
// copy discipline os/exec uses. It replaces exec.Cmd for drang's process builtins now that
// supervision is native to the Job Object rather than a portable reaper side-car.
//
// The per-command job is KILL_ON_JOB_CLOSE: while drang holds its handle the child (and its whole
// tree) dies if drang dies, and drang can tree-kill it via killTree; closing the handle after the
// child has exited is a no-op.
type jobCmd struct {
	// Inputs, set before start:
	exe         string    // resolved executable path (see resolveExe)
	argv        []string  // the argv the child sees (argv[0] is the presented program name)
	dir         string    // working directory ("" inherits ours)
	env         []string  // child environment (nil inherits ours)
	stdin       io.Reader // nil => the null device
	stdout      io.Writer // nil => the null device
	stderr      io.Writer // nil => the null device
	timeout     time.Duration
	killOnClose bool // whether the per-command job is KILL_ON_JOB_CLOSE (die-with-parent)

	// Runtime state:
	job         *winjob.Job
	proc        *winjob.Process
	childFiles  []*os.File // the child's stdio ends; closed by us after start
	parentPipes []*os.File // our pipe ends; closed after wait
	copiers     []func() error
	copyDone    chan error
	timer       *time.Timer
	timedOut    atomic.Bool
}

// killTree terminates the command's whole job (the process and every descendant) — the tree-kill
// that replaces `taskkill /F /T`. Safe any time after start; a no-op before start.
func (c *jobCmd) killTree() {
	if c.job != nil {
		_ = c.job.Terminate(1)
	}
}

// start resolves stdio, creates the per-command job, and launches the child born into it. On
// success the child is running and the stdio copiers are draining; call wait next.
func (c *jobCmd) start() error {
	stdinF, err := c.childStdin()
	if err != nil {
		c.cleanupFiles()
		return err
	}
	stdoutF, err := c.writerDescriptor(c.stdout)
	if err != nil {
		c.cleanupFiles()
		return err
	}
	stderrF, err := c.childStderr(stdoutF)
	if err != nil {
		c.cleanupFiles()
		return err
	}

	job, err := winjob.New(c.killOnClose)
	if err != nil {
		c.cleanupFiles()
		return err
	}
	c.job = job

	proc, err := winjob.LaunchExe(c.exe, c.argv, c.dir, c.env, []*winjob.Job{job}, winjob.Stdio{Stdin: stdinF, Stdout: stdoutF, Stderr: stderrF})
	if err != nil {
		job.Close()
		c.job = nil
		c.cleanupFiles()
		return err
	}
	c.proc = proc

	// The parent no longer needs the child's stdio ends.
	for _, f := range c.childFiles {
		f.Close()
	}
	c.childFiles = nil

	// Feed/drain the pipe-backed stdio in the background.
	if len(c.copiers) > 0 {
		c.copyDone = make(chan error, len(c.copiers))
		for _, fn := range c.copiers {
			go func(fn func() error) { c.copyDone <- fn() }(fn)
		}
	}

	// Arm the timeout: terminating the job kills the whole tree, and wait reports code 124.
	if c.timeout > 0 {
		c.timer = time.AfterFunc(c.timeout, func() {
			c.timedOut.Store(true)
			_ = c.job.Terminate(124)
		})
	}
	return nil
}

// wait blocks for the child to exit, drains the stdio copiers, and releases resources. It returns
// the child's exit code, whether the timeout fired, and any system/copy error (never the exit code
// itself).
func (c *jobCmd) wait() (code int, timedOut bool, err error) {
	code, werr := c.proc.Wait()
	if c.timer != nil {
		c.timer.Stop()
	}
	var copyErr error
	for i := 0; i < len(c.copiers); i++ {
		if e := <-c.copyDone; e != nil && copyErr == nil {
			copyErr = e
		}
	}
	for _, f := range c.parentPipes {
		f.Close()
	}
	c.parentPipes = nil
	if c.job != nil {
		c.job.Close()
	}
	if werr != nil {
		return code, c.timedOut.Load(), werr
	}
	return code, c.timedOut.Load(), copyErr
}

func (c *jobCmd) cleanupFiles() {
	for _, f := range c.childFiles {
		f.Close()
	}
	for _, f := range c.parentPipes {
		f.Close()
	}
	c.childFiles, c.parentPipes = nil, nil
}

// --- stdio descriptors, mirroring os/exec's childStdin / writerDescriptor ---

func (c *jobCmd) childStdin() (*os.File, error) {
	if c.stdin == nil {
		f, err := os.Open(os.DevNull)
		if err != nil {
			return nil, err
		}
		c.childFiles = append(c.childFiles, f)
		return f, nil
	}
	if f, ok := c.stdin.(*os.File); ok {
		return f, nil
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	c.childFiles = append(c.childFiles, pr)
	c.parentPipes = append(c.parentPipes, pw)
	c.copiers = append(c.copiers, func() error {
		_, _ = io.Copy(pw, c.stdin) // a child that closes stdin early is fine
		return pw.Close()
	})
	return pr, nil
}

func (c *jobCmd) writerDescriptor(w io.Writer) (*os.File, error) {
	if w == nil {
		f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			return nil, err
		}
		c.childFiles = append(c.childFiles, f)
		return f, nil
	}
	if f, ok := w.(*os.File); ok {
		return f, nil
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	c.childFiles = append(c.childFiles, pw)
	c.parentPipes = append(c.parentPipes, pr)
	c.copiers = append(c.copiers, func() error {
		_, err := io.Copy(w, pr)
		pr.Close()
		return err
	})
	return pw, nil
}

func (c *jobCmd) childStderr(stdoutFile *os.File) (*os.File, error) {
	if c.stderr != nil && interfaceEqual(c.stderr, c.stdout) {
		return stdoutFile, nil // both streams to one descriptor, avoiding concurrent writes to one Writer
	}
	return c.writerDescriptor(c.stderr)
}

// interfaceEqual reports whether a and b are the same writer, tolerating non-comparable dynamic
// types (which panic on ==).
func interfaceEqual(a, b io.Writer) bool {
	defer func() { _ = recover() }()
	return a == b
}

// resolveExe resolves name to an executable path for winjob, which does not search PATH: a
// qualified name (absolute or containing a separator) is returned as-is; with a custom env its
// PATH is searched; otherwise the process PATH is searched.
func resolveExe(name string, o execOpts) (string, error) {
	if hasPathSeparator(name) || filepath.IsAbs(name) {
		return name, nil
	}
	if o.resolveWithEnv {
		path, _ := envLookupFold(o.env, "PATH")
		if path == "" {
			return "", fmt.Errorf("exec: %q: executable file not found in PATH", name)
		}
		if found, ok := lookPathInEnv(name, path, o.env); ok {
			return found, nil
		}
		return "", fmt.Errorf("exec: %q: executable file not found in PATH", name)
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("exec: %q: %v", name, err)
	}
	return p, nil
}

// newJobCmd builds a jobCmd from argv + opts, wiring the given stdio. The child's presented argv[0]
// is the arg0 override when set, else argv[0]; defaultStdin is used unless the {stdin} option
// supplies one.
func newJobCmd(argv []string, o execOpts, defaultStdin io.Reader, stdout, stderr io.Writer) (*jobCmd, error) {
	exe, err := resolveExe(argv[0], o)
	if err != nil {
		return nil, err
	}
	childArgv := append([]string(nil), argv...)
	if o.hasArg0 {
		childArgv[0] = o.arg0
	}
	var env []string
	if o.hasEnv {
		env = o.env
	}
	stdin := defaultStdin
	if o.hasStdin {
		stdin = strings.NewReader(o.stdin)
	}
	return &jobCmd{
		exe:         exe,
		argv:        childArgv,
		dir:         o.cwd,
		env:         env,
		stdin:       stdin,
		stdout:      stdout,
		stderr:      stderr,
		timeout:     o.timeout,
		killOnClose: true, // synchronous forms die with drang; start overrides via {supervise}
	}, nil
}

// execErrCode builds a catchable Err from a nonzero exit code and optional stderr text.
func execErrCode(name string, code int, stderrText string) value.Value {
	if code < 1 {
		code = 1
	}
	msg := fmt.Sprintf("%s exited with code %d", name, code)
	if s := strings.TrimSpace(stderrText); s != "" {
		msg += ": " + s
	}
	return value.MakeErr(msg, int64(code))
}
