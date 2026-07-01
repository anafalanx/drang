package main

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A deliberately hostile battery: the nastier failure modes for the reaper supervision —
// grandchild tree-kill, an external taskkill /T of the parent, heavy concurrency, churn, a
// failed start, and killing the parent the instant the child exists.

func readFirstPid(t *testing.T, pipe io.Reader) int {
	t.Helper()
	ch := make(chan int, 1)
	go func() {
		sc := bufio.NewScanner(pipe)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "READY" {
				break
			}
			if pid, err := strconv.Atoi(line); err == nil {
				ch <- pid
				return
			}
		}
		ch <- -1
	}()
	select {
	case p := <-ch:
		return p
	case <-time.After(15 * time.Second):
		t.Fatal("timed out reading child PID")
		return -1
	}
}

func pollGrandPid(t *testing.T, file string) int {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(file); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatal("grandchild PID file never appeared")
	return -1
}

// startGrandchildTree launches a supervised CHILD drang that itself starts a GRANDCHILD
// (ping) and writes the grandchild PID to a file. Returns the parent cmd, the child PID, and
// the grandchild PID. The raw '...' string form keeps the Windows backslash paths literal.
func startGrandchildTree(t *testing.T, drang string) (parent *exec.Cmd, childPid, grandPid int) {
	t.Helper()
	dir := t.TempDir()
	gpidFile := filepath.Join(dir, "gpid.txt")
	childDr := filepath.Join(dir, "child.dr")
	topDr := filepath.Join(dir, "top.dr")
	childSrc := "$g := start(\"ping\", \"-n\", \"60\", \"127.0.0.1\")\nwrite_file('" + gpidFile + "', str(pid($g)))\nsleep(60)\n"
	topSrc := "$p := start('" + drang + "', '" + childDr + "', {supervise: true})\nsay(pid($p))\nsay(\"READY\")\nsleep(60)\n"
	if err := os.WriteFile(childDr, []byte(childSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(topDr, []byte(topSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(drang, topDr)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	childPid = readFirstPid(t, pipe)
	if childPid <= 0 {
		_ = cmd.Process.Kill()
		t.Fatal("did not read a child PID")
	}
	grandPid = pollGrandPid(t, gpidFile)
	return cmd, childPid, grandPid
}

// The reaper must kill the whole TREE, not just the direct child: when the parent is
// hard-killed, the supervised child AND its grandchild must both die (taskkill /T).
func TestHardGrandchildTreeKill(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real processes")
	}
	drang := buildDrang(t)
	cmd, child, grand := startGrandchildTree(t, drang)
	if !pidAlive(child) || !pidAlive(grand) {
		killAll(child, grand)
		_ = cmd.Process.Kill()
		t.Fatalf("child %d / grandchild %d should both be alive before the kill", child, grand)
	}
	_ = cmd.Process.Kill() // TerminateProcess the parent only — the reaper must do the tree
	_, _ = cmd.Process.Wait()
	if !waitAllDead([]int{child, grand}, 12*time.Second) {
		killAll(child, grand)
		t.Fatalf("the reaper did not kill the whole tree (child %d, grandchild %d)", child, grand)
	}
}

// An EXTERNAL taskkill /F /T of the parent (the OS killing the parent's whole tree, reaper
// included) must still leave nothing alive.
func TestHardExternalTaskkillTree(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real processes")
	}
	drang := buildDrang(t)
	cmd, child, grand := startGrandchildTree(t, drang)
	_ = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	_, _ = cmd.Process.Wait()
	if !waitAllDead([]int{child, grand}, 12*time.Second) {
		killAll(child, grand)
		t.Fatalf("after taskkill /T of the parent, child %d / grandchild %d survived", child, grand)
	}
}

// Heavy concurrency: 16 supervised children launched from a pmap fan-out (workers registering
// at once), all must die when the parent is hard-killed.
func TestHardMassiveConcurrency(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real processes")
	}
	drang := buildDrang(t)
	script := `$xs := []
for $i in 1..16 { $xs = push($xs, $i) }
$ps := $xs |> pmap(|$i| start("ping", "-n", "60", "127.0.0.1", {supervise: true}))
for $p in $ps { say(pid($p)) }
say("READY")
sleep(60)`
	cmd, pids := startAndReadPids(t, drang, script)
	if len(pids) != 16 {
		killAll(pids...)
		_ = cmd.Process.Kill()
		t.Fatalf("expected 16 PIDs from the fan-out, got %d: %v", len(pids), pids)
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	if !waitAllDead(pids, 15*time.Second) {
		killAll(pids...)
		t.Fatalf("not all 16 supervised children died: %v", pids)
	}
}

// Rapid churn: many register/deregister cycles must not hang, leak, or deadlock the reaper —
// the program must finish and exit 0.
func TestHardRapidChurn(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real processes")
	}
	drang := buildDrang(t)
	script := `for $i in 1..25 { run("cmd", "/c", "exit", "0", {supervise: true}) }
say("DONE")`
	cmd := exec.Command(drang, "-e", script)
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	doneCh := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(out)
		doneCh <- strings.TrimSpace(string(b))
	}()
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	select {
	case err := <-waitCh:
		got := <-doneCh
		if err != nil {
			t.Fatalf("churn run failed: %v", err)
		}
		if !strings.Contains(got, "DONE") {
			t.Fatalf("churn run did not finish cleanly, output: %q", got)
		}
	case <-time.After(25 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("churn run hung (reaper deadlock under register/deregister churn?)")
	}
}

// A failed supervised start must be a clean catchable Err that leaves no phantom registration
// — a later real supervised child must still be reaped normally.
func TestHardFailedStartNoPhantom(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real processes")
	}
	drang := buildDrang(t)
	script := `say(is_err(start("definitely-not-a-real-binary-zzz", {supervise: true})))
$p := start("ping", "-n", "60", "127.0.0.1", {supervise: true})
say(pid($p))
say("READY")
sleep(60)`
	cmd := exec.Command(drang, "-e", script)
	pipe, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// first line is the is_err(...) bool, then the real child PID
	ch := make(chan int, 1)
	go func() {
		sc := bufio.NewScanner(pipe)
		_ = sc.Scan() // "true" from the failed start
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "READY" {
				break
			}
			if pid, err := strconv.Atoi(line); err == nil {
				ch <- pid
				return
			}
		}
		ch <- -1
	}()
	var child int
	select {
	case child = <-ch:
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("timed out")
	}
	if child <= 0 {
		_ = cmd.Process.Kill()
		t.Fatal("no real child PID after a failed start")
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	if !waitAllDead([]int{child}, 8*time.Second) {
		killAll(child)
		t.Fatalf("child %d survived — a failed start may have corrupted supervision", child)
	}
}

// Kill the parent the instant the child exists: probes the registration-vs-EOF ordering. A
// "+ PID" written just before the parent dies is still buffered in the pipe and must be read
// by the reaper before it sees EOF, so the child must die with no grace period.
func TestHardKillImmediately(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real processes")
	}
	drang := buildDrang(t)
	for trial := 0; trial < 3; trial++ {
		cmd, pids := startAndReadPids(t, drang, `$p := start("ping", "-n", "60", "127.0.0.1", {supervise: true})
say(pid($p))
say("READY")
sleep(60)`)
		if len(pids) != 1 {
			killAll(pids...)
			_ = cmd.Process.Kill()
			t.Fatalf("trial %d: expected 1 PID, got %v", trial, pids)
		}
		_ = cmd.Process.Kill() // immediately, no grace
		_, _ = cmd.Process.Wait()
		if !waitAllDead(pids, 8*time.Second) {
			killAll(pids...)
			t.Fatalf("trial %d: child %d survived an immediate parent kill", trial, pids[0])
		}
	}
}

// findReaperPids locates the reaper side-car(s) for a specific drang binary: a drang.exe
// process whose command line carries --reap and whose image is exactly this test's build.
// All-single-quote PowerShell avoids nested-quote argv hazards.
func findReaperPids(drang string) []int {
	q := `Get-CimInstance Win32_Process | Where-Object { $_.Name -eq 'drang.exe' -and $_.CommandLine -like '*--reap*' -and $_.ExecutablePath -eq '` + drang + `' } | Select-Object -ExpandProperty ProcessId`
	out, _ := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", q).Output()
	var pids []int
	for _, f := range strings.Fields(string(out)) {
		if pid, err := strconv.Atoi(f); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// The documented ceiling, drawn precisely: supervision fails ONLY if the reaper itself is
// killed first. Kill the reaper, then the parent — the supervised child must SURVIVE (nothing
// else tears it down). If it died here, there'd be a teardown path we didn't account for.
func TestHardReaperKilledCeiling(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real processes")
	}
	drang := buildDrang(t)
	cmd, pids := startAndReadPids(t, drang, `$p := start("ping", "-n", "60", "127.0.0.1", {supervise: true})
say(pid($p))
say("READY")
sleep(60)`)
	if len(pids) != 1 {
		killAll(pids...)
		_ = cmd.Process.Kill()
		t.Fatalf("expected 1 PID, got %v", pids)
	}
	child := pids[0]

	var reapers []int
	for i := 0; i < 25 && len(reapers) == 0; i++ {
		reapers = findReaperPids(drang)
		if len(reapers) == 0 {
			time.Sleep(200 * time.Millisecond)
		}
	}
	if len(reapers) == 0 {
		killProcTree(child)
		_ = cmd.Process.Kill()
		t.Fatal("no reaper side-car found for a supervised child (expected exactly one)")
	}

	// Kill the reaper FIRST — remove supervision's safety net.
	for _, r := range reapers {
		killProcTree(r)
	}
	if !waitAllDead(reapers, 5*time.Second) {
		killProcTree(child)
		_ = cmd.Process.Kill()
		t.Fatal("could not kill the reaper")
	}

	// Now kill the parent. With no reaper, the child is orphaned but alive.
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	time.Sleep(3 * time.Second)
	alive := pidAlive(child)
	killProcTree(child) // clean up the orphan regardless
	if !alive {
		t.Fatal("child died with the reaper pre-killed — an unaccounted-for teardown path exists")
	}
	// Pass: confirms the ONLY boundary is a killed reaper; everything else reaps correctly.
}
