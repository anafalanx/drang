package winjob

import (
	"os"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Test-only Win32 helpers not wrapped by x/sys (reusing modkernel32 from launch.go).
var (
	procGetHandleInformation  = modkernel32.NewProc("GetHandleInformation")
	procGetProcessHandleCount = modkernel32.NewProc("GetProcessHandleCount")
)

func handleFlags(t *testing.T, h windows.Handle) uint32 {
	t.Helper()
	var flags uint32
	r, _, err := procGetHandleInformation.Call(uintptr(h), uintptr(unsafe.Pointer(&flags)))
	if r == 0 {
		t.Fatalf("GetHandleInformation: %v", err)
	}
	return flags
}

func processHandleCount(t *testing.T) uint32 {
	t.Helper()
	var n uint32
	r, _, err := procGetProcessHandleCount.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		t.Fatalf("GetProcessHandleCount: %v", err)
	}
	return n
}

func nul(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// D1: an explicit empty environment must produce a DOUBLE-NUL block, not a single NUL (which
// would be an under-terminated block CreateProcess reads past).
func TestMakeEnvBlockEmpty(t *testing.T) {
	p, err := makeEnvBlock([]string{})
	if err != nil {
		t.Fatal(err)
	}
	if p == nil {
		t.Fatal("empty (non-nil) env must return a non-nil block pointer")
	}
	got := (*[2]uint16)(unsafe.Pointer(p))
	if got[0] != 0 || got[1] != 0 {
		t.Errorf("empty env block = [%d %d], want [0 0] (double NUL)", got[0], got[1])
	}
	if np, _ := makeEnvBlock(nil); np != nil {
		t.Error("nil env must return a nil pointer (inherit)")
	}
}

// D2: Launch must NOT mutate the caller's stdio handles (it duplicates instead). A non-inheritable
// file passed as stdio must remain non-inheritable after Launch — success and failure paths.
func TestLaunchDoesNotMutateCallerHandle(t *testing.T) {
	f := nul(t) // opened non-inheritable by Go
	defer f.Close()
	before := handleFlags(t, windows.Handle(f.Fd())) & windows.HANDLE_FLAG_INHERIT
	if before != 0 {
		t.Fatalf("precondition: %s should be non-inheritable", os.DevNull)
	}

	job := mustJob(t, false)
	defer job.Close()
	p, err := Launch([]string{selfExe(t)}, "", childEnv("exit"), []*Job{job}, Stdio{Stdin: f, Stdout: f, Stderr: f})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if _, err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	if after := handleFlags(t, windows.Handle(f.Fd())) & windows.HANDLE_FLAG_INHERIT; after != 0 {
		t.Error("Launch made the caller's handle inheritable (should have duplicated instead)")
	}

	// Failure path (bad exe) must also leave the caller's handle untouched.
	_, _ = Launch([]string{`C:\nope\missing.exe`}, "", childEnv("exit"), []*Job{job}, Stdio{Stdin: f, Stdout: f, Stderr: f})
	if after := handleFlags(t, windows.Handle(f.Fd())) & windows.HANDLE_FLAG_INHERIT; after != 0 {
		t.Error("failed Launch left the caller's handle inheritable")
	}
}

// Regression: a GC racing an in-flight Wait must not let the AddCleanup safety net close the
// process handle out from under Wait (KeepAlive guards it).
func TestProcessWaitSurvivesGC(t *testing.T) {
	job := mustJob(t, false)
	defer job.Close()
	f := nul(t)
	defer f.Close()
	for i := 0; i < 30; i++ {
		p, err := Launch([]string{selfExe(t), "0"}, "", childEnv("exit"), []*Job{job}, Stdio{Stdin: f, Stdout: f, Stderr: f})
		if err != nil {
			t.Fatal(err)
		}
		go runtime.GC() // race a GC against Wait to try to trigger a premature cleanup
		if code, err := p.Wait(); err != nil || code != 0 {
			t.Fatalf("iter %d: Wait = (%d, %v) — GC closed the handle mid-Wait?", i, code, err)
		}
	}
}

// D4: Wait is idempotent — a second Wait errors instead of double-closing, and Handle() returns 0
// after Wait.
func TestProcessWaitIdempotent(t *testing.T) {
	f := nul(t)
	defer f.Close()
	job := mustJob(t, false)
	defer job.Close()
	p, err := Launch([]string{selfExe(t), "0"}, "", childEnv("exit"), []*Job{job}, Stdio{Stdin: f, Stdout: f, Stderr: f})
	if err != nil {
		t.Fatal(err)
	}
	if code, err := p.Wait(); err != nil || code != 0 {
		t.Fatalf("first Wait = (%d, %v), want (0, nil)", code, err)
	}
	if _, err := p.Wait(); err == nil {
		t.Error("second Wait should return an error, not touch a closed handle")
	}
	if h := p.Handle(); h != 0 {
		t.Errorf("Handle() after Wait = %v, want 0", h)
	}
}

// D4 (race): Handle() concurrent with Wait() must be data-race-free (validated under -race).
func TestProcessHandleWaitRace(t *testing.T) {
	f := nul(t)
	defer f.Close()
	job := mustJob(t, false)
	defer job.Close()
	p, err := Launch([]string{selfExe(t)}, "", childEnv("sleep"), []*Job{job}, Stdio{Stdin: f, Stdout: f, Stderr: f})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = p.Handle() }()
	}
	_ = job.Terminate(1)
	_, _ = p.Wait()
	wg.Wait()
}

// D5 + general: launch+Wait many children with no unbounded handle growth.
func TestLaunchNoHandleLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping handle-leak scan in -short")
	}
	job := mustJob(t, false)
	defer job.Close()
	// warm up (first spawns lazily allocate runtime handles)
	for i := 0; i < 5; i++ {
		run(t, []*Job{job}, childEnv("exit"), "", selfExe(t), "0")
	}
	base := processHandleCount(t)
	const n = 50
	for i := 0; i < n; i++ {
		run(t, []*Job{job}, childEnv("exit"), "", selfExe(t), "0")
	}
	grew := int(processHandleCount(t)) - int(base)
	if grew > 20 { // a real per-launch leak would grow by ~n
		t.Errorf("handle count grew by %d over %d launch+Wait cycles — likely a leak", grew, n)
	}
}

// Multi-job born-in: a child born into [a,b,c] is a member of all three (nesting order preserved),
// and terminating the outermost kills it.
func TestLaunchDeepNesting(t *testing.T) {
	a, b, c := mustJob(t, false), mustJob(t, false), mustJob(t, false)
	defer a.Close()
	defer b.Close()
	defer c.Close()
	f := nul(t)
	defer f.Close()
	p, err := Launch([]string{selfExe(t)}, "", childEnv("sleep"), []*Job{a, b, c}, Stdio{Stdin: f, Stdout: f, Stderr: f})
	if err != nil {
		t.Fatalf("Launch into 3 nested jobs: %v", err)
	}
	for name, j := range map[string]*Job{"a": a, "b": b, "c": c} {
		if in, err := InJob(p.Handle(), j.handle); err != nil || !in {
			t.Errorf("child not in job %s (in=%v err=%v)", name, in, err)
		}
	}
	if err := a.Terminate(1); err != nil {
		t.Fatalf("Terminate outermost: %v", err)
	}
	assertDeadSoon(t, "child", p.Handle())
}

// D8: InJob's false path — a child is NOT reported as a member of an unrelated job.
func TestLaunchInJobFalsePath(t *testing.T) {
	member := mustJob(t, false)
	defer member.Close()
	other := mustJob(t, false)
	defer other.Close()
	f := nul(t)
	defer f.Close()
	p, err := Launch([]string{selfExe(t)}, "", childEnv("sleep"), []*Job{member}, Stdio{Stdin: f, Stdout: f, Stderr: f})
	if err != nil {
		t.Fatal(err)
	}
	defer member.Terminate(1)
	if in, err := InJob(p.Handle(), other.handle); err != nil || in {
		t.Errorf("InJob(child, unrelated) = (%v, %v), want (false, nil)", in, err)
	}
}

// D7: env entries are deduped case-insensitively, last value wins.
func TestLaunchEnvDedup(t *testing.T) {
	job := mustJob(t, false)
	defer job.Close()
	env := append(childEnv("print-env"), "MYVAR=first", "myvar=second")
	out, _, _ := run(t, []*Job{job}, env, "", selfExe(t), "MYVAR")
	if out != "second" {
		t.Errorf("case-insensitive dedup: child saw %q, want %q (last wins)", out, "second")
	}
}

// D6: a relative argv[0] with a non-empty dir is rejected (would otherwise resolve against the
// wrong cwd).
func TestLaunchRelativeArgvRejected(t *testing.T) {
	job := mustJob(t, false)
	defer job.Close()
	f := nul(t)
	defer f.Close()
	if _, err := Launch([]string{"child.exe"}, t.TempDir(), childEnv("exit"), []*Job{job}, Stdio{Stdin: f, Stdout: f, Stderr: f}); err == nil {
		t.Error("expected an error for a relative argv[0] with dir set")
	}
}

// Windows exit codes are 32-bit; values >255 must not be truncated (unlike Unix 8-bit codes).
func TestLaunchExitCodeLarge(t *testing.T) {
	job := mustJob(t, false)
	defer job.Close()
	for _, want := range []int{256, 300, 3000000000} {
		_, _, code := run(t, []*Job{job}, childEnv("exit"), "", selfExe(t), strconv.Itoa(want))
		if code != want {
			t.Errorf("exit(%d) => %d (truncated?)", want, code)
		}
	}
}

// The die-with-parent guarantee rests on the job handle being non-inheritable (else a child could
// pin the job open and defeat KILL_ON_JOB_CLOSE).
func TestJobHandleNotInheritable(t *testing.T) {
	job := mustJob(t, true)
	defer job.Close()
	if handleFlags(t, job.handle)&windows.HANDLE_FLAG_INHERIT != 0 {
		t.Error("job handle is inheritable — a child could pin the job open and defeat die-with-parent")
	}
}

// Terminating the job while a Wait is in flight must unblock Wait cleanly with the terminate code.
func TestTerminateWhileWaiting(t *testing.T) {
	job := mustJob(t, false)
	defer job.Close()
	f := nul(t)
	defer f.Close()
	p, err := Launch([]string{selfExe(t)}, "", childEnv("sleep"), []*Job{job}, Stdio{Stdin: f, Stdout: f, Stderr: f})
	if err != nil {
		t.Fatal(err)
	}
	type res struct {
		code int
		err  error
	}
	done := make(chan res, 1)
	go func() { c, e := p.Wait(); done <- res{c, e} }()
	time.Sleep(50 * time.Millisecond) // let Wait block
	if err := job.Terminate(7); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Wait errored after Terminate: %v", r.err)
		}
		if r.code != 7 {
			t.Errorf("exit code after Terminate(7) = %d, want 7", r.code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Wait did not unblock after Terminate")
	}
}

// The same *os.File used for all three stdio (dedupHandles collapses to one) must spawn cleanly.
func TestLaunchSharedStdio(t *testing.T) {
	job := mustJob(t, false)
	defer job.Close()
	f := nul(t)
	defer f.Close()
	p, err := Launch([]string{selfExe(t), "0"}, "", childEnv("exit"), []*Job{job}, Stdio{Stdin: f, Stdout: f, Stderr: f})
	if err != nil {
		t.Fatalf("shared-stdio Launch: %v", err)
	}
	if code, err := p.Wait(); err != nil || code != 0 {
		t.Fatalf("shared-stdio Wait = (%d, %v)", code, err)
	}
}
