package winjob

import (
	"runtime"
	"testing"
	"time"
)

// drainFor collects events until done(collected) is true or the deadline passes.
func drainFor(t *testing.T, m *Monitor, d time.Duration, done func([]Event) bool) []Event {
	t.Helper()
	deadline := time.After(d)
	var evs []Event
	for {
		if done(evs) {
			return evs
		}
		select {
		case e, ok := <-m.Events():
			if !ok {
				return evs
			}
			evs = append(evs, e)
		case <-deadline:
			return evs
		}
	}
}

func hasKind(evs []Event, k EventKind) bool {
	for _, e := range evs {
		if e.Kind == k {
			return true
		}
	}
	return false
}

func hasPidKind(evs []Event, pid int, k EventKind) bool {
	for _, e := range evs {
		if e.Pid == pid && e.Kind == k {
			return true
		}
	}
	return false
}

func hasKindKey(evs []Event, k EventKind, key uintptr) bool {
	for _, e := range evs {
		if e.Kind == k && e.Job == key {
			return true
		}
	}
	return false
}

func hasPidKindKey(evs []Event, pid int, k EventKind, key uintptr) bool {
	for _, e := range evs {
		if e.Pid == pid && e.Kind == k && e.Job == key {
			return true
		}
	}
	return false
}

func countKind(evs []Event, k EventKind) int {
	n := 0
	for _, e := range evs {
		if e.Kind == k {
			n++
		}
	}
	return n
}

// A watched job reports NEW_PROCESS and EXIT for its child and ACTIVE_ZERO when it drains.
func TestMonitorLifecycle(t *testing.T) {
	mon, err := NewMonitor()
	if err != nil {
		t.Fatal(err)
	}
	defer mon.Close()
	job := mustJob(t, false)
	defer job.Close()
	if _, err := mon.Watch(job); err != nil { // watch BEFORE launch, so NEW_PROCESS isn't missed
		t.Fatal(err)
	}
	f := nul(t)
	defer f.Close()
	p, err := Launch([]string{selfExe(t), "0"}, "", childEnv("exit"), []*Job{job}, Stdio{Stdin: f, Stdout: f, Stderr: f})
	if err != nil {
		t.Fatal(err)
	}
	pid := p.Pid()
	evs := drainFor(t, mon, 8*time.Second, func(e []Event) bool { return hasKind(e, EventActiveZero) })
	if !hasPidKind(evs, pid, EventNewProcess) {
		t.Errorf("no NEW_PROCESS for child pid %d; events=%+v", pid, evs)
	}
	if !hasPidKind(evs, pid, EventExit) && !hasPidKind(evs, pid, EventAbnormalExit) {
		t.Errorf("no EXIT for child pid %d; events=%+v", pid, evs)
	}
	if !hasKind(evs, EventActiveZero) {
		t.Errorf("no ACTIVE_ZERO (subtree drained); events=%+v", evs)
	}
}

// The port on a job sees the whole subtree: a grandchild the child forks also reports NEW_PROCESS,
// and ACTIVE_ZERO fires only after the entire tree is terminated.
func TestMonitorSubtree(t *testing.T) {
	mon, err := NewMonitor()
	if err != nil {
		t.Fatal(err)
	}
	defer mon.Close()
	job := mustJob(t, false)
	defer job.Close()
	if _, err := mon.Watch(job); err != nil {
		t.Fatal(err)
	}
	f := nul(t)
	defer f.Close()
	// spawn-grandchild: the child forks a cmd/ping grandchild, then both sleep.
	if _, err := Launch([]string{selfExe(t)}, "", childEnv("spawn-grandchild"), []*Job{job}, Stdio{Stdin: f, Stdout: f, Stderr: f}); err != nil {
		t.Fatal(err)
	}
	evs := drainFor(t, mon, 8*time.Second, func(e []Event) bool { return countKind(e, EventNewProcess) >= 2 })
	if n := countKind(evs, EventNewProcess); n < 2 {
		t.Errorf("expected >=2 NEW_PROCESS (child + grandchild), got %d; events=%+v", n, evs)
	}
	if err := job.Terminate(1); err != nil {
		t.Fatal(err)
	}
	evs = append(evs, drainFor(t, mon, 8*time.Second, func(e []Event) bool { return hasKind(e, EventActiveZero) })...)
	if !hasKind(evs, EventActiveZero) {
		t.Errorf("no ACTIVE_ZERO after terminate; events=%+v", evs)
	}
}

// One monitor, two jobs: each job's events are tagged with its own key.
func TestMonitorMultiJob(t *testing.T) {
	mon, err := NewMonitor()
	if err != nil {
		t.Fatal(err)
	}
	defer mon.Close()
	jobA, jobB := mustJob(t, false), mustJob(t, false)
	defer jobA.Close()
	defer jobB.Close()
	keyA, err := mon.Watch(jobA)
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := mon.Watch(jobB)
	if err != nil {
		t.Fatal(err)
	}
	f := nul(t)
	defer f.Close()
	pA, err := Launch([]string{selfExe(t), "0"}, "", childEnv("exit"), []*Job{jobA}, Stdio{Stdin: f, Stdout: f, Stderr: f})
	if err != nil {
		t.Fatal(err)
	}
	pB, err := Launch([]string{selfExe(t), "0"}, "", childEnv("exit"), []*Job{jobB}, Stdio{Stdin: f, Stdout: f, Stderr: f})
	if err != nil {
		t.Fatal(err)
	}
	evs := drainFor(t, mon, 8*time.Second, func(e []Event) bool {
		return hasKindKey(e, EventActiveZero, keyA) && hasKindKey(e, EventActiveZero, keyB)
	})
	if !hasPidKindKey(evs, pA.Pid(), EventNewProcess, keyA) {
		t.Errorf("child A (pid %d) not tagged jobA (key %x); events=%+v", pA.Pid(), keyA, evs)
	}
	if !hasPidKindKey(evs, pB.Pid(), EventNewProcess, keyB) {
		t.Errorf("child B (pid %d) not tagged jobB (key %x); events=%+v", pB.Pid(), keyB, evs)
	}
	// Cross-check: A's pid must not be tagged with B's key.
	if hasPidKindKey(evs, pA.Pid(), EventNewProcess, keyB) {
		t.Error("child A's event was mis-tagged with jobB's key")
	}
}

// Close stops delivery and closes Events(); a second Close is safe.
func TestMonitorClose(t *testing.T) {
	mon, err := NewMonitor()
	if err != nil {
		t.Fatal(err)
	}
	if err := mon.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closed := make(chan struct{})
	go func() {
		for range mon.Events() { // drains until the channel is closed
		}
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Events() was not closed after Close")
	}
	if err := mon.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// D1: a Monitor dropped without Close() must not leak its port handle or its loop goroutine — the
// GC backstop reclaims both.
func TestMonitorNoLeakOnDrop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping monitor-leak scan in -short")
	}
	baseG := runtime.NumGoroutine()
	baseH := processHandleCount(t)
	for i := 0; i < 20; i++ {
		if _, err := NewMonitor(); err != nil { // created and immediately dropped, no Close
			t.Fatal(err)
		}
	}
	var gGrew, hGrew int
	for i := 0; i < 12; i++ { // give the cleanups + loop exits time to settle
		runtime.GC()
		time.Sleep(50 * time.Millisecond)
		gGrew = runtime.NumGoroutine() - baseG
		hGrew = int(processHandleCount(t)) - int(baseH)
		if gGrew <= 3 && hGrew <= 5 {
			return // reclaimed
		}
	}
	t.Errorf("after dropping 20 monitors, goroutines grew by %d and handles by %d — leak", gGrew, hGrew)
}

// D3: Watch after Close returns an error rather than associating a job with a closed/recycled port.
func TestMonitorWatchAfterCloseErrors(t *testing.T) {
	mon, err := NewMonitor()
	if err != nil {
		t.Fatal(err)
	}
	if err := mon.Close(); err != nil {
		t.Fatal(err)
	}
	job := mustJob(t, false)
	defer job.Close()
	if _, err := mon.Watch(job); err == nil {
		t.Error("Watch after Close should return an error")
	}
}

// D2 contract: a process killed by TerminateJobObject emits NO per-pid exit event — the only drain
// signal for a job-terminated subtree is EventActiveZero (pid-less).
func TestMonitorTreeKillDrainSignal(t *testing.T) {
	mon, err := NewMonitor()
	if err != nil {
		t.Fatal(err)
	}
	defer mon.Close()
	job := mustJob(t, false)
	defer job.Close()
	if _, err := mon.Watch(job); err != nil {
		t.Fatal(err)
	}
	f := nul(t)
	defer f.Close()
	p, err := Launch([]string{selfExe(t)}, "", childEnv("sleep"), []*Job{job}, Stdio{Stdin: f, Stdout: f, Stderr: f})
	if err != nil {
		t.Fatal(err)
	}
	pid := p.Pid()
	evs := drainFor(t, mon, 5*time.Second, func(e []Event) bool { return hasPidKind(e, pid, EventNewProcess) })
	if err := job.Terminate(1); err != nil {
		t.Fatal(err)
	}
	evs = append(evs, drainFor(t, mon, 8*time.Second, func(e []Event) bool { return hasKind(e, EventActiveZero) })...)
	if !hasKind(evs, EventActiveZero) {
		t.Error("tree-kill produced no EventActiveZero drain signal")
	}
	if hasPidKind(evs, pid, EventExit) || hasPidKind(evs, pid, EventAbnormalExit) {
		t.Error("tree-kill unexpectedly produced a per-pid exit event (contract: only EventActiveZero)")
	}
}

// Close must unblock the loop even while it is genuinely parked in GetQueuedCompletionStatus (a
// long-lived child, no further events pending).
func TestMonitorCloseUnblocksBusyPort(t *testing.T) {
	mon, err := NewMonitor()
	if err != nil {
		t.Fatal(err)
	}
	job := mustJob(t, false)
	defer job.Close()
	if _, err := mon.Watch(job); err != nil {
		t.Fatal(err)
	}
	f := nul(t)
	defer f.Close()
	p, err := Launch([]string{selfExe(t)}, "", childEnv("sleep"), []*Job{job}, Stdio{Stdin: f, Stdout: f, Stderr: f})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Release()
	defer job.Terminate(1)
	time.Sleep(100 * time.Millisecond) // let the loop park after the NEW_PROCESS
	_ = mon.Close()
	closed := make(chan struct{})
	go func() {
		for range mon.Events() {
		}
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not unblock the parked loop")
	}
}

// D6: associating a port replays NEW_PROCESS for a process already in the job — Watch after Launch
// still sees the existing member.
func TestMonitorWatchAfterLaunch(t *testing.T) {
	mon, err := NewMonitor()
	if err != nil {
		t.Fatal(err)
	}
	defer mon.Close()
	job := mustJob(t, false)
	defer job.Close()
	f := nul(t)
	defer f.Close()
	p, err := Launch([]string{selfExe(t)}, "", childEnv("sleep"), []*Job{job}, Stdio{Stdin: f, Stdout: f, Stderr: f})
	if err != nil {
		t.Fatal(err)
	}
	defer job.Terminate(1)
	time.Sleep(200 * time.Millisecond) // child is running before we associate
	if _, err := mon.Watch(job); err != nil {
		t.Fatal(err)
	}
	evs := drainFor(t, mon, 5*time.Second, func(e []Event) bool { return hasPidKind(e, p.Pid(), EventNewProcess) })
	if !hasPidKind(evs, p.Pid(), EventNewProcess) {
		t.Error("Watch after Launch did not replay NEW_PROCESS for the pre-existing child")
	}
}

// D5/D8: no message id that carries a pid may classify to EventOther (which would leave callers
// unable to tell whether Event.Pid is meaningful).
func TestClassifyContract(t *testing.T) {
	for _, msg := range []uint32{1, 2, 3, 4, 6, 7, 8, 9, 10, 11, 12} {
		if isProcessMsg(msg) && classifyMsg(msg) == EventOther {
			t.Errorf("msg %d carries a pid but classifies to EventOther (ambiguous shape)", msg)
		}
	}
}
