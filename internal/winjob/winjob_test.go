package winjob

import (
	"os/exec"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestJobConcurrentTerminateClose is the D3 regression: Terminate and Close must serialize so a
// timeout/kill Terminate cannot race the reaping path's Close on the same handle (and, once closed,
// a stale handle value cannot be recycled and terminated by mistake). Many goroutines hammer both;
// under -race this must be clean, and no call may panic or double-close.
func TestJobConcurrentTerminateClose(t *testing.T) {
	job, err := New(true)
	if err != nil {
		t.Fatal(err)
	}
	const goroutines = 16
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_ = job.Terminate(1)
			} else {
				_ = job.Close()
			}
		}(i)
	}
	wg.Wait()
}

// TestJobClosedIdempotent: after Close, Close/Terminate are no-op successes and Assign reports the
// job is closed rather than acting on a released (possibly recycled) handle.
func TestJobClosedIdempotent(t *testing.T) {
	job, err := New(true)
	if err != nil {
		t.Fatal(err)
	}
	if err := job.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := job.Close(); err != nil {
		t.Errorf("second Close should be a no-op, got %v", err)
	}
	if err := job.Terminate(1); err != nil {
		t.Errorf("Terminate after Close should be a no-op, got %v", err)
	}
	if err := job.Assign(windows.Handle(0)); err != errClosedJob {
		t.Errorf("Assign after Close = %v, want errClosedJob", err)
	}
}

// A job terminates its members on Terminate — the whole-tree kill that replaces taskkill /F /T.
// This adopts a real child via OpenProcess to validate the kernel job semantics on their own,
// independent of the born-in-job launcher (M1b).
func TestJobTerminateKillsMember(t *testing.T) {
	job, err := New(true)
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()

	cmd := exec.Command("cmd", "/c", "ping", "-n", "30", "127.0.0.1") // ~30s; stdout discarded
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_SET_QUOTA, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatalf("OpenProcess: %v", err)
	}
	defer windows.CloseHandle(h)
	if err := job.Assign(h); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	if err := job.Terminate(1); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done: // killed -> Wait returned
	case <-time.After(10 * time.Second):
		t.Fatal("child was not killed by job Terminate within 10s")
	}
}

// A KILL_ON_JOB_CLOSE job kills its members when the last handle closes — the die-with-parent
// guarantee (here simulated by drang closing its only handle to the job).
func TestJobKillOnClose(t *testing.T) {
	job, err := New(true)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cmd", "/c", "ping", "-n", "30", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_SET_QUOTA, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatalf("OpenProcess: %v", err)
	}
	defer windows.CloseHandle(h)
	if err := job.Assign(h); err != nil {
		t.Fatalf("Assign: %v", err)
	}

	_ = job.Close() // the only handle -> members are terminated

	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("child was not killed when the job handle closed within 10s")
	}
}
