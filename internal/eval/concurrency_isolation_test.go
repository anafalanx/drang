package eval

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/anafalanx/drang/internal/value"
)

func TestClosureSnapshotSharesSynchronizedCapabilities(t *testing.T) {
	env := NewEnv()
	cases := []struct {
		name string
		v    value.Value
	}{
		{"channel", value.MakeObj(value.Chan, &Chan{})},
		{"task", value.MakeObj(value.Task, &Task{})},
		{"process", value.MakeObj(value.Proc, &Proc{})},
		{"store", value.MakeObj(value.Store, &Store{})},
	}
	for _, tc := range cases {
		if err := env.define(tc.name, tc.v, false); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := env.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		got, ok := snapshot.get(tc.name)
		if !ok || got.Obj() != tc.v.Obj() {
			t.Errorf("%s capability was cloned instead of shared", tc.name)
		}
	}
}

// These tests are intentionally mutation-heavy. Under -race they exercise the
// ownership boundary itself, not merely the final value: caller and worker both
// mutate what began as the same captured object after an explicit channel rendezvous.
func TestSpawnCapturedContainerIsolation(t *testing.T) {
	src := `$a := []
$gate := chan()
$task := spawn(|| {
  send($gate, true)
  for $i in 1..1000 { push($a, $i) }
  len($a)
})
recv($gate)
for $i in 1..1000 { push($a, -$i) }
say(await($task), len($a))`
	if got := run(t, src); got != "1000 1000\n" {
		t.Fatalf("captured array was not isolated: got %q", got)
	}
}

func TestSpawnNestedCaptureAndAliasIsolation(t *testing.T) {
	src := `$leaf := []
$box := {left: $leaf, right: $leaf}
$gate := chan()
$task := spawn(|| {
  send($gate, true)
  for $i in 1..1000 { push($box.left, $i) }
  len($box.right)
})
recv($gate)
for $i in 1..1000 { push($leaf, -$i) }
say(await($task), len($leaf))`
	if got := run(t, src); got != "1000 1000\n" {
		t.Fatalf("nested capture lost isolation or aliasing: got %q", got)
	}
}

func TestSpawnCapturedCallGraphIsolation(t *testing.T) {
	src := `$box := []
$gate := chan()
fn .touch($n) { push($box, $n) }
fn .through($n) { .touch($n) }
$task := spawn(|| {
  send($gate, true)
  for $i in 1..1000 { .through($i) }
  len($box)
})
recv($gate)
for $i in 1..1000 { push($box, -$i) }
say(await($task), len($box))`
	if got := run(t, src); got != "1000 1000\n" {
		t.Fatalf("reachable captured functions retained the caller Env: got %q", got)
	}
}

func TestSpawnCaptureGraphPreservesCyclesAndAliases(t *testing.T) {
	src := `$a := []
push($a, $a)
$b := $a
$task := spawn(|| {
  push($a[0], 7)
  [len($a), len($b), $a == $b]
})
say(await($task), len($a))`
	if got := run(t, src); got != "[2, 2, true] 1\n" {
		t.Fatalf("snapshot did not preserve alias/cycle topology: got %q", got)
	}
}

func TestSpawnFunctionArgumentGetsReboundCapture(t *testing.T) {
	src := `$state := []
$task := spawn(|$callback| $callback(), || { push($state, 1); len($state) })
say(await($task), len($state))`
	if got := run(t, src); got != "1 0\n" {
		t.Fatalf("function argument retained its producer Env: got %q", got)
	}
}

func TestSpawnFunctionInsideFrozenCaptureGetsRebound(t *testing.T) {
	src := `$state := []
$callbacks ::= [|| { push($state, 1); len($state) }]
$task := spawn(|| $callbacks[0]())
say(await($task), len($state))`
	if got := run(t, src); got != "1 0\n" {
		t.Fatalf("function inside frozen capture retained its producer Env: got %q", got)
	}
}

func TestPmapCapturedContainerIsolation(t *testing.T) {
	src := `$items := []
for $i in 1..256 { push($items, $i) }
$captured := []
$result := pmap($items, |$x| { push($captured, $x); $x * 2 })
say(len($captured), len($result), $result[255])`
	if got := run(t, src); got != "0 256 512\n" {
		t.Fatalf("pmap workers shared a captured container: got %q", got)
	}
}

func TestPmapFunctionElementsGetReboundCaptures(t *testing.T) {
	src := `$state := []
$callbacks := []
for $i in 1..64 { push($callbacks, || { push($state, 1); len($state) }) }
$result := pmap($callbacks, |$callback| $callback())
say(len($state), len($result), reduce($result, 0, |$a, $b| $a + $b))`
	if got := run(t, src); got != "0 64 64\n" {
		t.Fatalf("pmap function elements retained producer captures: got %q", got)
	}
}

func waitForChannelWaiters(t *testing.T, c *Chan, senders, receivers int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		gotSenders, gotReceivers := len(c.senders), len(c.receivers)
		c.mu.Unlock()
		if gotSenders == senders && gotReceivers == receivers {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("channel waiters did not settle at senders=%d receivers=%d", senders, receivers)
}

func TestChannelCloseRejectsBlockedSendAndDrainsCommittedBuffer(t *testing.T) {
	c := &Chan{capacity: 1}
	if err := c.send(value.MakeInt(1)); err != nil {
		t.Fatal(err)
	}
	ctx := newExecutionContext()
	owned := ctx.beginRun()
	defer ctx.endRun(owned)

	sent := make(chan error, 1)
	ctx.addRunnable(1)
	go func() {
		defer ctx.exitRunnable()
		sent <- c.sendContext(value.MakeInt(2), ctx)
	}()
	waitForChannelWaiters(t, c, 1, 0)
	c.doClose()
	if err := <-sent; err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("blocked send after close = %v, want closed error", err)
	}
	v, open, err := c.recv()
	if err != nil || !open || v.AsInt() != 1 {
		t.Fatalf("committed buffered value after close = (%v, %v, %v)", v.Display(), open, err)
	}
	if _, open, err := c.recv(); err != nil || open {
		t.Fatalf("closed drained channel = (open %v, err %v)", open, err)
	}
}

func TestChannelSendCloseLinearizable(t *testing.T) {
	ctx := newExecutionContext()
	owned := ctx.beginRun()
	defer ctx.endRun(owned)

	for i := 0; i < 500; i++ {
		c := &Chan{}
		type recvResult struct {
			v    value.Value
			open bool
			err  error
		}
		received := make(chan recvResult, 1)
		ctx.addRunnable(1)
		go func() {
			defer ctx.exitRunnable()
			v, open, err := c.recvContext(ctx)
			received <- recvResult{v: v, open: open, err: err}
		}()
		waitForChannelWaiters(t, c, 0, 1)

		start := make(chan struct{})
		sent := make(chan error, 1)
		closed := make(chan struct{})
		go func() { <-start; sent <- c.sendContext(value.MakeInt(int64(i)), ctx) }()
		go func() { <-start; c.doClose(); close(closed) }()
		close(start)
		sendErr := <-sent
		r := <-received
		<-closed
		if r.err != nil {
			t.Fatalf("iteration %d receive error: %v", i, r.err)
		}
		if sendErr == nil {
			if !r.open || r.v.AsInt() != int64(i) {
				t.Fatalf("iteration %d successful send was not received: open=%v value=%s", i, r.open, r.v.Display())
			}
		} else if r.open || !strings.Contains(sendErr.Error(), "closed") {
			t.Fatalf("iteration %d rejected send/receive outcome: send=%v open=%v", i, sendErr, r.open)
		}
	}
}

func TestChanCapacityCeiling(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int64
		code int64
	}{
		{"negative", -1, 1},
		{"over-limit", maxChannelCapacity + 1, 137},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := builtinChan([]value.Value{value.MakeInt(tc.n)})
			if err != nil {
				t.Fatalf("chan(%d) aborted instead of returning Err: %v", tc.n, err)
			}
			if !got.IsErr() || got.ErrCode() != tc.code || !strings.Contains(got.Display(), fmt.Sprint(maxChannelCapacity)) {
				t.Fatalf("chan(%d) = %s (code %d), want bounded Err code %d", tc.n, got.Display(), got.ErrCode(), tc.code)
			}
		})
	}

	for _, n := range []int64{0, maxChannelCapacity} {
		got, err := builtinChan([]value.Value{value.MakeInt(n)})
		if err != nil || got.IsErr() || got.Obj().(*Chan).capacity != int(n) {
			t.Fatalf("chan(%d) = %s, %v", n, got.Display(), err)
		}
	}
}

func TestSpawnLiveTaskCeilingIsCatchable(t *testing.T) {
	if live := liveSpawnTasks.Load(); live != 0 {
		t.Fatalf("test started with %d live spawn tasks", live)
	}
	if got := run(t, `say(await(spawn(|| 7)))`); got != "7\n" {
		t.Fatalf("ordinary spawn below ceiling = %q", got)
	}
	if live := liveSpawnTasks.Load(); live != 0 {
		t.Fatalf("await returned before spawn slot was released: %d still live", live)
	}
	for i := int64(0); i < maxLiveSpawnTasks; i++ {
		if !tryAcquireSpawnTask() {
			t.Fatalf("spawn slot %d rejected below limit %d", i, maxLiveSpawnTasks)
		}
	}
	defer liveSpawnTasks.Store(0)
	if tryAcquireSpawnTask() {
		t.Fatalf("spawn slot accepted above limit %d", maxLiveSpawnTasks)
	}

	src := `$r := spawn(|| 1)
say(is_err($r), err_code($r), err_msg($r))`
	want := fmt.Sprintf("true 137 spawn: too many live tasks (limit %d)\n", maxLiveSpawnTasks)
	if got := run(t, src); got != want {
		t.Fatalf("spawn ceiling was not a catchable resource Err: got %q, want %q", got, want)
	}
}

func TestSpawnSlotCASNeverExceedsCeiling(t *testing.T) {
	if live := liveSpawnTasks.Load(); live != 0 {
		t.Fatalf("test started with %d live spawn tasks", live)
	}
	attempts := int(maxLiveSpawnTasks * 4)
	start := make(chan struct{})
	results := make(chan bool, attempts)
	for i := 0; i < attempts; i++ {
		go func() {
			<-start
			results <- tryAcquireSpawnTask()
		}()
	}
	close(start)
	accepted := 0
	for i := 0; i < attempts; i++ {
		if <-results {
			accepted++
		}
	}
	defer liveSpawnTasks.Store(0)
	if accepted != int(maxLiveSpawnTasks) || liveSpawnTasks.Load() != maxLiveSpawnTasks {
		t.Fatalf("concurrent spawn slots accepted=%d live=%d, want %d", accepted, liveSpawnTasks.Load(), maxLiveSpawnTasks)
	}
}

func TestPmapWorkerBudgetIsProcessBounded(t *testing.T) {
	limit := cap(pmapWorkerBudget)
	if limit < 1 {
		t.Fatal("pmap worker budget has no slots")
	}
	got := acquirePmapWorkers(limit + 1)
	defer releasePmapWorkers(got)
	if got != limit {
		t.Fatalf("acquired %d pmap workers, want process limit %d", got, limit)
	}
	if extra := acquirePmapWorkers(1); extra != 0 {
		releasePmapWorkers(extra)
		t.Fatalf("acquired %d pmap workers above process limit", extra)
	}
}

func TestNestedPmapFallsBackWithoutDeadlockAndStaysOrdered(t *testing.T) {
	limit := cap(pmapWorkerBudget)
	items := make([]value.Value, limit)
	for i := range items {
		items[i] = value.MakeInt(int64(i + 1))
	}
	env := NewEnv()
	if err := env.define("items", value.MakeArray(items), false); err != nil {
		t.Fatal(err)
	}
	src := `$result := pmap($items, |$x| pmap([1, 2, 3], |$y| $x * 10 + $y))
say(len($result), $result[0], $result[len($result) - 1])`
	want := fmt.Sprintf("%d [11, 12, 13] [%d, %d, %d]\n", limit, limit*10+1, limit*10+2, limit*10+3)
	if got := runWithEnv(t, env, src); got != want {
		t.Fatalf("nested pmap result/order = %q, want %q", got, want)
	}
}

func TestConcurrentPmapSharesBudgetAndStaysOrdered(t *testing.T) {
	src := `$items := []
for $i in 1..128 { push($items, $i) }
$tasks := []
for $i in 1..16 {
  push($tasks, spawn(|$offset, $xs| pmap($xs, |$x| $offset + $x), $i * 1000, $items))
}
$results := map($tasks, |$task| await($task))
$ok := all($results, |$row, $i| len($row) == 128 and $row[0] == ($i + 1) * 1000 + 1 and $row[127] == ($i + 1) * 1000 + 128)
say($ok, len($results))`
	if got := run(t, src); got != "true 16\n" {
		t.Fatalf("concurrent pmap result/order = %q", got)
	}
}
