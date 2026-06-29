//go:build windows

package main

import (
	"bufio"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Adversarial coverage for the reaper supervision mechanism: the failure modes that don't
// show up in the happy path — concurrency, fd hygiene, clean-exit semantics, deregister
// leaks, and a hostile reaper stdin. All Windows-runnable (real processes).

// startAndReadPids runs `drang -e script`, which must print one child PID per line followed
// by a "READY" line, then returns the still-running parent and the collected PIDs.
func startAndReadPids(t *testing.T, drang, script string) (*exec.Cmd, []int) {
	t.Helper()
	cmd := exec.Command(drang, "-e", script)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ch := make(chan []int, 1)
	go func() {
		var pids []int
		sc := bufio.NewScanner(pipe)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "READY" {
				break
			}
			if pid, err := strconv.Atoi(line); err == nil {
				pids = append(pids, pid)
			}
		}
		ch <- pids
	}()
	select {
	case pids := <-ch:
		return cmd, pids
	case <-time.After(25 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("timed out reading child PIDs / READY")
		return nil, nil
	}
}

func waitAllDead(pids []int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		anyAlive := false
		for _, p := range pids {
			if pidAlive(p) {
				anyAlive = true
				break
			}
		}
		if !anyAlive {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func killAll(pids ...int) {
	for _, p := range pids {
		killProcTree(p)
	}
}

const ping60 = `start("ping","-n","60","127.0.0.1"`

// Concurrency: a pmap fan-out of supervised launches registers from many goroutines at once.
// All children must die when the parent is hard-killed (exercises the sync.Once spawn, the
// mutex'd registrations, and the reaper killing many trees).
func TestAdvConcurrentFanout(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real processes")
	}
	drang := buildDrang(t)
	script := `$ps := [1,2,3,4,5,6,7,8] |> pmap(|$i| ` + ping60 + `, {supervise: true}))
for $p in $ps { say(pid($p)) }
say("READY")
sleep(60)`
	cmd, pids := startAndReadPids(t, drang, script)
	if len(pids) != 8 {
		killAll(pids...)
		_ = cmd.Process.Kill()
		t.Fatalf("expected 8 child PIDs from the pmap fan-out, got %d: %v", len(pids), pids)
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	if !waitAllDead(pids, 12*time.Second) {
		killAll(pids...)
		t.Fatalf("not all 8 supervised children died after parent death: %v", pids)
	}
}

// fd hygiene (the linchpin): a co-resident UNsupervised child must neither pin the pipe write
// end open (which would delay EOF and keep the supervised child alive) nor be wrongly reaped.
func TestAdvFDHygieneAndCoexistence(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real processes")
	}
	drang := buildDrang(t)
	script := `$a := ` + ping60 + `, {supervise: true})
$b := ` + ping60 + `)
say(pid($a))
say(pid($b))
say("READY")
sleep(60)`
	cmd, pids := startAndReadPids(t, drang, script)
	if len(pids) != 2 {
		killAll(pids...)
		_ = cmd.Process.Kill()
		t.Fatalf("expected 2 PIDs, got %v", pids)
	}
	sup, unsup := pids[0], pids[1]
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	if !waitAllDead([]int{sup}, 6*time.Second) {
		killAll(sup, unsup)
		t.Fatalf("supervised child %d did not die promptly — the unsupervised child may have pinned the write end", sup)
	}
	alive := pidAlive(unsup)
	killProcTree(unsup)
	if !alive {
		t.Fatalf("unsupervised child %d was wrongly killed by the reaper", unsup)
	}
}

// Clean-exit semantics: a clean parent exit (no kill) must also take supervised children down
// — the EOF fires the same way. (Documented behavior: "supervise" means "die with me".)
func TestAdvCleanExitKillsChild(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real processes")
	}
	drang := buildDrang(t)
	script := `$p := ` + ping60 + `, {supervise: true})
say(pid($p))
say("READY")
sleep(1)`
	cmd, pids := startAndReadPids(t, drang, script)
	if len(pids) != 1 {
		killAll(pids...)
		_ = cmd.Process.Kill()
		t.Fatalf("expected 1 PID, got %v", pids)
	}
	_, _ = cmd.Process.Wait() // let drang finish and exit cleanly
	if !waitAllDead(pids, 8*time.Second) {
		killAll(pids...)
		t.Fatalf("supervised child %d survived a CLEAN parent exit", pids[0])
	}
}

// Deregister: a supervised child that exits cleanly is deregistered, so a later parent death
// must not leave it haunting the registry (and a still-running sibling must still die).
func TestAdvDeregisteredSiblingDoesNotLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real processes")
	}
	drang := buildDrang(t)
	script := `run("cmd","/c","exit","0",{supervise: true})
$p := ` + ping60 + `, {supervise: true})
say(pid($p))
say("READY")
sleep(60)`
	cmd, pids := startAndReadPids(t, drang, script)
	if len(pids) != 1 {
		killAll(pids...)
		_ = cmd.Process.Kill()
		t.Fatalf("expected 1 PID, got %v", pids)
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	if !waitAllDead(pids, 8*time.Second) {
		killAll(pids...)
		t.Fatalf("supervised child %d survived after a deregistered sibling", pids[0])
	}
}

// Hostile reaper input: the reaper is the last line of defense and must be unkillable by bad
// data. Empty and malformed stdin must both make it exit cleanly and promptly, never hang.
func TestAdvReapBinaryRobustness(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes")
	}
	drang := buildDrang(t)
	run := func(name, stdin string) {
		cmd := exec.Command(drang, "--reap")
		cmd.Stdin = strings.NewReader(stdin)
		done := make(chan error, 1)
		go func() { done <- cmd.Run() }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("%s: drang --reap exited with error: %v", name, err)
			}
		case <-time.After(6 * time.Second):
			_ = cmd.Process.Kill()
			t.Fatalf("%s: drang --reap hung", name)
		}
	}
	run("empty", "")
	// malformed lines + a bogus high PID that does not exist (taskkill just no-ops on it)
	run("malformed", "garbage\n+ abc\nxyz 5\n+ 999999990\n- 1\n+\n-\n")
}
