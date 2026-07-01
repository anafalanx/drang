package winjob

import (
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

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
