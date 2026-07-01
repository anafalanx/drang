package winjob

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Stdio are the child's standard handles. All three must be non-nil concrete files: the caller
// chooses inheritance (os.Stdin/Stdout/Stderr), redirection (os.Pipe ends), or the null device
// (os.OpenFile(os.DevNull, ...)). Requiring concrete handles avoids the NULL-handle-in-
// HANDLE_LIST footgun, where a 0 handle silently empties the whole inherit list.
type Stdio struct {
	Stdin  *os.File
	Stdout *os.File
	Stderr *os.File
}

// procHandle owns the child's process handle and closes it exactly once — via Wait, Release, or
// GC cleanup — so an un-waited Process cannot leak the handle and it is never double-closed.
type procHandle struct {
	mu     sync.Mutex
	handle windows.Handle
}

func (ph *procHandle) get() windows.Handle {
	ph.mu.Lock()
	defer ph.mu.Unlock()
	return ph.handle
}

func (ph *procHandle) release() {
	ph.mu.Lock()
	defer ph.mu.Unlock()
	if ph.handle != 0 {
		windows.CloseHandle(ph.handle)
		ph.handle = 0
	}
}

// Process is a launched child. Tree-kill/limits go through the child's Job; this handle is for
// Wait and direct queries.
type Process struct {
	ph  *procHandle
	pid int
}

// Pid returns the child's process id.
func (p *Process) Pid() int { return p.pid }

// Handle returns the child's process handle, or 0 after Wait/Release.
func (p *Process) Handle() windows.Handle { return p.ph.get() }

// Release closes the process handle without waiting — for the tree-kill / job fan-out where a
// per-child Wait is skipped. Idempotent; do not call concurrently with an in-flight Wait.
func (p *Process) Release() { p.ph.release() }

var errWaitDone = errors.New("winjob: Wait already called")

// Wait blocks until the child exits, returns its exit code, and releases the process handle. Call
// it at most once; a second call returns an error rather than touching a closed handle.
func (p *Process) Wait() (int, error) {
	h := p.ph.get()
	if h == 0 {
		return -1, errWaitDone
	}
	defer p.ph.release()
	s, err := windows.WaitForSingleObject(h, windows.INFINITE)
	if err != nil {
		return -1, fmt.Errorf("WaitForSingleObject: %w", err)
	}
	if s != windows.WAIT_OBJECT_0 {
		return -1, fmt.Errorf("WaitForSingleObject: unexpected state 0x%x", s)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return -1, fmt.Errorf("GetExitCodeProcess: %w", err)
	}
	return int(code), nil
}

// Launch starts argv as a child born into jobs (root-first order) at CreateProcess time — before
// its first thread runs — so nothing the child spawns can escape those jobs (race-free). argv[0]
// must be a resolved executable path (Launch does NOT search PATH), and must be absolute when dir
// is set. dir is the working directory ("" inherits ours); env is the child environment (nil
// inherits ours). Exactly the three stdio handles are inherited (as private duplicates); no other
// handle leaks to the child, and the caller's own handles are never mutated.
func Launch(argv []string, dir string, env []string, jobs []*Job, io Stdio) (*Process, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("winjob.Launch: empty argv")
	}
	if io.Stdin == nil || io.Stdout == nil || io.Stderr == nil {
		return nil, fmt.Errorf("winjob.Launch: all three stdio handles must be non-nil")
	}
	if len(jobs) == 0 {
		return nil, fmt.Errorf("winjob.Launch: at least one job is required")
	}
	if dir != "" && !filepath.IsAbs(argv[0]) {
		// CreateProcess resolves a relative lpApplicationName against OUR cwd, not dir; refuse the
		// ambiguity rather than silently launch the wrong binary (or none).
		return nil, fmt.Errorf("winjob.Launch: argv[0] %q must be an absolute path when dir is set", argv[0])
	}

	appName, err := windows.UTF16PtrFromString(argv[0])
	if err != nil {
		return nil, fmt.Errorf("winjob.Launch: bad executable path %q: %w", argv[0], err)
	}
	cmdLine, err := windows.UTF16PtrFromString(makeCmdLine(argv))
	if err != nil {
		return nil, fmt.Errorf("winjob.Launch: bad command line: %w", err)
	}
	var dirPtr *uint16
	if dir != "" {
		if dirPtr, err = windows.UTF16PtrFromString(dir); err != nil {
			return nil, fmt.Errorf("winjob.Launch: bad dir %q: %w", dir, err)
		}
	}
	envBlock, err := makeEnvBlock(env)
	if err != nil {
		return nil, fmt.Errorf("winjob.Launch: bad environment: %w", err)
	}

	// Duplicate each distinct stdio handle into a private, inheritable copy (os/exec's discipline):
	// we never mutate the caller's handles, and only our dups can be inherited. Close them after
	// the spawn on every path.
	inH, outH, errH := windows.Handle(io.Stdin.Fd()), windows.Handle(io.Stdout.Fd()), windows.Handle(io.Stderr.Fd())
	origs := dedupHandles([]windows.Handle{inH, outH, errH})
	cur := windows.CurrentProcess()
	dupOf := make(map[windows.Handle]windows.Handle, len(origs))
	inheritList := make([]windows.Handle, 0, len(origs))
	closeDups := func() {
		for _, d := range inheritList {
			windows.CloseHandle(d)
		}
	}
	for _, h := range origs {
		if h == 0 || h == windows.InvalidHandle {
			closeDups()
			return nil, fmt.Errorf("winjob.Launch: invalid stdio handle")
		}
		var dup windows.Handle
		if err := windows.DuplicateHandle(cur, h, cur, &dup, 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
			closeDups()
			return nil, fmt.Errorf("winjob.Launch: DuplicateHandle: %w", err)
		}
		dupOf[h] = dup
		inheritList = append(inheritList, dup)
	}
	// Keep the caller's *os.Files alive through the Fd()->DuplicateHandle window above (their
	// finalizers close the source handles). The dups are independent thereafter.
	runtime.KeepAlive(io.Stdin)
	runtime.KeepAlive(io.Stdout)
	runtime.KeepAlive(io.Stderr)
	defer closeDups()

	jobHandles := make([]windows.Handle, len(jobs))
	for i, j := range jobs {
		jobHandles[i] = j.handle
	}

	al, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return nil, fmt.Errorf("winjob.Launch: NewProcThreadAttributeList: %w", err)
	}
	defer al.Delete()
	// JOB_LIST: born into the jobs at spawn time (root-first). HANDLE_LIST: restrict inheritance to
	// exactly the stdio dups. The container retains both pointers until Delete (post-spawn).
	if err := al.Update(procThreadAttributeJobList, unsafe.Pointer(&jobHandles[0]), uintptr(len(jobHandles))*unsafe.Sizeof(jobHandles[0])); err != nil {
		return nil, fmt.Errorf("winjob.Launch: Update(JOB_LIST): %w", err)
	}
	if err := al.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&inheritList[0]), uintptr(len(inheritList))*unsafe.Sizeof(inheritList[0])); err != nil {
		return nil, fmt.Errorf("winjob.Launch: Update(HANDLE_LIST): %w", err)
	}

	si := &windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Cb:        uint32(unsafe.Sizeof(windows.StartupInfoEx{})),
			Flags:     windows.STARTF_USESTDHANDLES,
			StdInput:  dupOf[inH],
			StdOutput: dupOf[outH],
			StdErr:    dupOf[errH],
		},
		ProcThreadAttributeList: al.List(),
	}
	var pi windows.ProcessInformation
	flags := uint32(windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT)
	err = windows.CreateProcess(appName, cmdLine, nil, nil, true, flags, envBlock, dirPtr, &si.StartupInfo, &pi)
	runtime.KeepAlive(jobHandles)
	runtime.KeepAlive(inheritList)
	if err != nil {
		return nil, fmt.Errorf("winjob.Launch: CreateProcess %q: %w", argv[0], err)
	}
	windows.CloseHandle(pi.Thread) // the child is already running; we never touch its thread

	ph := &procHandle{handle: pi.Process}
	p := &Process{ph: ph, pid: int(pi.ProcessId)}
	runtime.AddCleanup(p, (*procHandle).release, ph) // never leak the handle if Wait/Release is skipped
	return p, nil
}

// makeCmdLine builds a Windows command line from argv, quoting each argument by the same rules
// CreateProcess/CommandLineToArgvW use (syscall.EscapeArg), so the child parses back the exact
// argv drang intended — no shell, no word-splitting.
func makeCmdLine(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = syscall.EscapeArg(a)
	}
	return strings.Join(parts, " ")
}

// makeEnvBlock builds a CREATE_UNICODE_ENVIRONMENT block ("K=V\0K=V\0...\0"). A nil env returns a
// nil pointer (the child inherits ours); a non-nil env (even empty) returns an explicit block.
// Entries are deduped case-insensitively by key, last-wins (matching os/exec and Win32 lookup).
func makeEnvBlock(env []string) (*uint16, error) {
	if env == nil {
		return nil, nil
	}
	env = dedupEnvCase(env)
	if len(env) == 0 {
		block := []uint16{0, 0} // an empty environment block is a double NUL
		return &block[0], nil
	}
	var block []uint16
	for _, e := range env {
		if strings.IndexByte(e, 0) != -1 {
			return nil, fmt.Errorf("environment entry contains NUL: %q", e)
		}
		if !strings.ContainsRune(e, '=') {
			return nil, fmt.Errorf("environment entry is not KEY=VALUE: %q", e)
		}
		u, err := windows.UTF16FromString(e) // appends a trailing NUL
		if err != nil {
			return nil, err
		}
		block = append(block, u...)
	}
	block = append(block, 0) // final terminating NUL (double-NUL closes the block)
	return &block[0], nil
}

// dedupEnvCase keeps the last value for each key, matched case-insensitively (Windows env-var
// names), preserving first-seen order. Entries without '=' are passed through for the caller's
// KEY=VALUE check to reject.
func dedupEnvCase(env []string) []string {
	pos := make(map[string]int, len(env))
	out := make([]string, 0, len(env))
	for _, e := range env {
		i := strings.IndexByte(e, '=')
		if i < 0 {
			out = append(out, e)
			continue
		}
		key := strings.ToUpper(e[:i])
		if j, ok := pos[key]; ok {
			out[j] = e // last wins
		} else {
			pos[key] = len(out)
			out = append(out, e)
		}
	}
	return out
}

func dedupHandles(hs []windows.Handle) []windows.Handle {
	out := make([]windows.Handle, 0, len(hs))
	seen := make(map[windows.Handle]bool, len(hs))
	for _, h := range hs {
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}

// IsProcessInJob is not wrapped by x/sys/windows v0.46.0, so it is declared here.
var (
	modkernel32        = windows.NewLazySystemDLL("kernel32.dll")
	procIsProcessInJob = modkernel32.NewProc("IsProcessInJob")
)

// InJob reports whether process is a member of job. A zero job reports whether the process is in
// ANY job. Used to prove born-in-job membership.
func InJob(process, job windows.Handle) (bool, error) {
	var res int32
	r, _, err := procIsProcessInJob.Call(uintptr(process), uintptr(job), uintptr(unsafe.Pointer(&res)))
	if r == 0 {
		return false, err
	}
	return res != 0, nil
}
