package eval

import (
	"fmt"
	"io"
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
	ownedStdin  *os.File  // a file drang opened for {stdin_file}; passed to the child, closed after start
	stdout      io.Writer // nil => the null device
	stderr      io.Writer // nil => the null device
	timeout     time.Duration
	killOnClose bool          // whether the per-command job is KILL_ON_JOB_CLOSE (die-with-parent)
	limits      winjob.Limits // kernel-enforced resource caps applied to the per-command job (zero = none)
	mergeStderr bool          // route the child's stderr to its stdout descriptor (2>&1)

	// Runtime state:
	job         *winjob.Job
	proc        *winjob.Process
	childFiles  []*os.File // the child's stdio ends; closed by us after start
	parentPipes []*os.File // our pipe ends; closed after wait
	copiers     []func() error
	copyDone    chan error
	timer       *time.Timer
	timedOut    atomic.Bool
	monitor     *winjob.Monitor // non-nil when limits are set: watches for breach events
	monDone     chan struct{}   // closed when drainLimitEvents returns
	monitorStop sync.Once       // stopMonitor may be reached by competing cleanup paths
	limitHit    atomic.Pointer[string]
}

// drainLimitEvents records the first resource-limit breach the monitor reports (which cap fired),
// so wait()/start can name it. It runs until the monitor is closed in wait()/the start reaper.
func (c *jobCmd) drainLimitEvents() {
	defer close(c.monDone)
	for ev := range c.monitor.Events() {
		var which string
		switch ev.Kind {
		case winjob.EventProcessMemoryLimit, winjob.EventJobMemoryLimit:
			which = "memory"
		case winjob.EventProcessTimeLimit, winjob.EventJobTimeLimit:
			which = "CPU-time"
		case winjob.EventActiveProcessLimit:
			which = "process-count"
		default:
			continue
		}
		w := which
		if c.limitHit.CompareAndSwap(nil, &w) {
			// Several Job limits reject the operation that crossed the cap but do not
			// guarantee that the job's root process exits. Turn every observed breach into
			// one decisive outcome: terminate the whole tree and let wait report code 137.
			c.killTree()
		}
	}
}

// stopMonitor closes the breach monitor (if any) and waits for its drain goroutine, so limitHit is
// final before the caller reads it. Idempotent enough for the single call sites (wait / start reaper).
func (c *jobCmd) stopMonitor() {
	c.monitorStop.Do(func() {
		if c.monitor != nil {
			_ = c.monitor.Close()
			<-c.monDone
		}
	})
}

// breachErr returns a catchable limit-breach Err (code 137) if a resource cap fired, else the zero
// Value. Call stopMonitor first so limitHit is final.
func (c *jobCmd) breachErr(name string) (value.Value, bool) {
	if w := c.limitHit.Load(); w != nil {
		return value.MakeErr(fmt.Sprintf("%s exceeded its %s limit", name, *w), 137), true
	}
	return value.Value{}, false
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

	// Apply kernel-enforced resource caps and start watching for a breach BEFORE the child is
	// born, so it starts already limited and no breach event is missed. A memory/CPU breach has no
	// reliable exit code (the kernel fails allocations or terminates without a sentinel), so the
	// Monitor is required to detect, name, and decisively terminate on every configured breach.
	if !c.limits.IsZero() {
		if lerr := job.SetLimits(c.limits); lerr != nil {
			job.Close()
			c.job = nil
			c.cleanupFiles()
			return lerr
		}
		mon, merr := winjob.NewMonitor()
		if merr != nil {
			job.Close()
			c.job = nil
			c.cleanupFiles()
			return fmt.Errorf("resource-limit monitor: %w", merr)
		}
		if _, werr := mon.Watch(job); werr != nil {
			_ = mon.Close()
			job.Close()
			c.job = nil
			c.cleanupFiles()
			return fmt.Errorf("resource-limit monitor: %w", werr)
		}
		c.monitor = mon
		c.monDone = make(chan struct{})
		go c.drainLimitEvents()
	}

	proc, err := winjob.LaunchExe(c.exe, c.argv, c.dir, c.env, []*winjob.Job{job}, winjob.Stdio{Stdin: stdinF, Stdout: stdoutF, Stderr: stderrF})
	if err != nil {
		// If limits were requested the monitor and its drain goroutine already exist.
		// Close and join them explicitly; the drain goroutine retains c, so relying on
		// GC here would create a permanent handle/goroutine cycle.
		c.stopMonitor()
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
	c.stopMonitor() // finalize limitHit before any caller reads breachErr
	// The root process may have launched a descendant that inherited stdout/stderr and then
	// exited. Synchronous forms own the whole tree, so end the Job before joining pipe copiers;
	// otherwise that descendant can keep the inherited write handles open forever and hang run /
	// capture / capture_all / pipe even though the process we waited for is already gone.
	if c.job != nil {
		_ = c.job.Terminate(1)
		_ = c.job.Close()
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
	// Close is idempotent. Keep c.job non-nil so a timeout callback that raced timer.Stop sees a
	// closed Job (whose Terminate is a no-op) rather than racing a nil pointer.
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
	if c.ownedStdin != nil {
		// {stdin_file}: hand the file straight to the child (zero-copy). It is drang's to close,
		// so it joins childFiles and is closed after the spawn (the child inherited its own dup).
		c.childFiles = append(c.childFiles, c.ownedStdin)
		return c.ownedStdin, nil
	}
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
	// {merge_stderr}: route stderr to the same descriptor as stdout so the child's output is
	// interleaved in original order (like a shell's 2>&1) — also the path when the two sinks are
	// literally the same writer.
	if c.mergeStderr || (c.stderr != nil && interfaceEqual(c.stderr, c.stdout)) {
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
		// *exec.Error already renders as `exec: "name": <reason>`; return it unwrapped so the
		// message has a single `exec:` prefix (matching the env-PATH branch), not a nested one.
		return "", err
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
		if winjob.IsBatchTarget(exe) {
			// A batch file is run via `cmd.exe /c`, which controls argv[0]; there is no way to
			// present a different one. Reject rather than silently ignore the option.
			return nil, fmt.Errorf("arg0 is not supported for a batch (.bat/.cmd) target: cmd.exe controls argv[0]")
		}
		childArgv[0] = o.arg0
	}
	// Validate {cwd} early with a clean message, instead of letting CreateProcess fail deep in the
	// launcher and leak an internal "winjob.Launch: CreateProcess ..." string.
	if o.cwd != "" {
		if info, serr := os.Stat(o.cwd); serr != nil || !info.IsDir() {
			return nil, fmt.Errorf("cwd %q is not an existing directory", o.cwd)
		}
	}
	var env []string
	if o.hasEnv {
		env = o.env
	}
	if o.hasStdin && o.hasStdinFile {
		return nil, fmt.Errorf("stdin and stdin_file are mutually exclusive")
	}
	stdin := defaultStdin
	var owned *os.File
	switch {
	case o.hasStdinFile:
		f, oerr := os.Open(o.stdinFile)
		if oerr != nil {
			return nil, fmt.Errorf("stdin_file %q: %v", o.stdinFile, oerr)
		}
		owned = f // childStdin hands it to the child zero-copy and closes it after the spawn
	case o.hasStdin:
		stdin = strings.NewReader(o.stdin)
	}
	return &jobCmd{
		exe:         exe,
		argv:        childArgv,
		dir:         o.cwd,
		env:         env,
		stdin:       stdin,
		ownedStdin:  owned,
		stdout:      stdout,
		stderr:      stderr,
		timeout:     o.timeout,
		limits:      o.limits,
		mergeStderr: o.mergeStderr,
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
