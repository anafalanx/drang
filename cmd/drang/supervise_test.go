//go:build windows

package main

import (
	"bufio"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These drive a real drang binary end to end: start a long-lived child, hard-kill the parent
// (TerminateProcess — no graceful cleanup, simulating a crash/SIGKILL), and check what
// happens to the child. They are the proof that the reaper side-car actually reaps.

func buildDrang(t *testing.T) string {
	t.Helper()
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go.exe")
	out := filepath.Join(t.TempDir(), "drang.exe")
	if b, err := exec.Command(goBin, "build", "-o", out, ".").CombinedOutput(); err != nil {
		t.Fatalf("build drang: %v\n%s", err, b)
	}
	return out
}

// pidAlive reports whether a PID is running, language-independently (the PID appears quoted
// in tasklist's CSV only when a matching task exists).
func pidAlive(pid int) bool {
	out, _ := exec.Command("tasklist", "/FO", "CSV", "/NH", "/FI", "PID eq "+strconv.Itoa(pid)).CombinedOutput()
	return strings.Contains(string(out), "\""+strconv.Itoa(pid)+"\"")
}

// startChildUnder runs `drang -e <script>` (which must start a child and print its PID), and
// returns the parent cmd and the child PID, having confirmed the child is alive.
func startChildUnder(t *testing.T, drang, script string) (*exec.Cmd, int) {
	t.Helper()
	cmd := exec.Command(drang, "-e", script)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pidCh := make(chan int, 1)
	go func() {
		line, _ := bufio.NewReader(pipe).ReadString('\n')
		if pid, err := strconv.Atoi(strings.TrimSpace(line)); err == nil {
			pidCh <- pid
		} else {
			pidCh <- -1
		}
	}()
	var child int
	select {
	case child = <-pidCh:
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("timed out reading child PID from drang stdout")
	}
	if child <= 0 {
		_ = cmd.Process.Kill()
		t.Fatal("drang did not print a valid child PID")
	}
	if !pidAlive(child) {
		_ = cmd.Process.Kill()
		t.Fatalf("child %d should be alive immediately after start", child)
	}
	return cmd, child
}

const longChild = `start("ping", "-n", "60", "127.0.0.1"`

func TestSupervisedChildDiesWithParent(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real processes")
	}
	drang := buildDrang(t)
	cmd, child := startChildUnder(t, drang, `$p := `+longChild+`, {supervise: true})
say(pid($p))
sleep(30)`)

	// Hard-kill the parent (no cleanup runs). Only the reaper can now kill the child.
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !pidAlive(child) {
			return // the reaper killed it — success
		}
		time.Sleep(200 * time.Millisecond)
	}
	killProcTree(child) // cleanup before failing
	t.Fatalf("supervised child %d survived the parent's death", child)
}

func TestUnsupervisedChildSurvivesParent(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real processes")
	}
	drang := buildDrang(t)
	cmd, child := startChildUnder(t, drang, `$p := `+longChild+`)
say(pid($p))
sleep(30)`)

	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()

	// No supervision → no reaper → the orphaned child keeps running.
	time.Sleep(3 * time.Second)
	alive := pidAlive(child)
	killProcTree(child) // always clean up the orphan
	if !alive {
		t.Fatalf("unsupervised child %d should have survived the parent (orphaned), but it died", child)
	}
}
