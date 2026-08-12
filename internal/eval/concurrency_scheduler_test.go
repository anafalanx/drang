package eval

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/anafalanx/drang/internal/parser"
	"github.com/anafalanx/drang/internal/value"
)

func runConcurrencyProgramTimed(t *testing.T, src string, vm bool) string {
	t.Helper()
	p := parser.New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse errors: %v", errs)
	}
	var buf bytes.Buffer
	oldOut, oldVM := stdout, vmEnabled
	stdout, vmEnabled = &buf, vm
	defer func() { stdout, vmEnabled = oldOut, oldVM }()

	done := make(chan error, 1)
	go func() {
		if vm {
			done <- RunProgramVM(prog, NewEnv())
		} else {
			done <- RunProgram(prog, NewEnv())
		}
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runtime error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("program hung past scheduler deadline:\n%s", src)
	}
	return buf.String()
}

func assertConcurrencyBoth(t *testing.T, src string, check func(string) bool) {
	t.Helper()
	var outputs [2]string
	for i, vm := range []bool{false, true} {
		outputs[i] = runConcurrencyProgramTimed(t, src, vm)
		if !check(outputs[i]) {
			t.Fatalf("vm=%v unexpected output %q", vm, outputs[i])
		}
	}
	if outputs[0] != outputs[1] {
		t.Fatalf("walker/VM mismatch: walker=%q VM=%q", outputs[0], outputs[1])
	}
}

func TestAwaitBlockedReceiverReturnsCatchableDeadlock(t *testing.T) {
	src := `$c := chan()
$t := spawn(|| recv($c))
$r := await($t)
say(is_err($r), err_msg($r))`
	assertConcurrencyBoth(t, src, func(out string) bool {
		return strings.Contains(out, "true recv would deadlock")
	})
}

func TestBlockedSenderWakesWhenLastCounterpartyExits(t *testing.T) {
	src := `$c := chan()
$t := spawn(|| sleep(0.2))
$r := send($c, 1)
say(is_err($r), err_msg($r), await($t))`
	assertConcurrencyBoth(t, src, func(out string) bool {
		return strings.Contains(out, "true send would deadlock")
	})
}

func TestDelayedRunnableCounterpartiesStillMatch(t *testing.T) {
	cases := []string{
		`$c := chan()
$t := spawn(|| { sleep(0.05); recv($c) })
$sent := send($c, 41)
say($sent, await($t))`,
		`$c := chan()
$producer := spawn(|| { sleep(0.05); send($c, 10); send($c, 20); close($c) })
$result := pmap([1, 2], |$_| recv($c))
say(len($result), sum($result), await($producer))`,
	}
	for _, src := range cases {
		assertConcurrencyBoth(t, src, func(out string) bool {
			return strings.Contains(out, "true 41") || strings.Contains(out, "2 30")
		})
	}
}

func TestTopLevelAndPmapOrphansReturnCatchableDeadlock(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{`$r := send(chan(), 1)
say(is_err($r), err_msg($r))`, "true send would deadlock"},
		{`$r := recv(chan())
say(is_err($r), err_msg($r))`, "true recv would deadlock"},
		{`$c := chan()
$r := pmap([1, 2], |$_| recv($c))
say(is_err($r), err_msg($r))`, "true recv would deadlock"},
	}
	for _, tc := range cases {
		assertConcurrencyBoth(t, tc.src, func(out string) bool {
			return strings.Contains(out, tc.want)
		})
	}
}

func TestFirstClassConcurrencyBuiltinsKeepCallerContext(t *testing.T) {
	src := `$send := send
$recv := recv
$await := await
$c := chan()
$t := spawn(|| $recv($c))
$r := $await($t)
say(is_err($r), err_msg($r))
$orphan := $send(chan(), 1)
say(is_err($orphan), err_msg($orphan))`
	assertConcurrencyBoth(t, src, func(out string) bool {
		return strings.Contains(out, "true recv would deadlock") && strings.Contains(out, "true send would deadlock")
	})
}

func TestShadowedConcurrencyBuiltinsKeepCallerContext(t *testing.T) {
	src := `$raw_send := send
$raw_recv := recv
$raw_await := await
$send := |$ch, $v| $raw_send($ch, $v)
$recv := |$ch| $raw_recv($ch)
$await := |$task| $raw_await($task)
$c := chan()
$t := spawn(|| recv($c))
$r := await($t)
say(is_err($r), err_msg($r))
$orphan := send(chan(), 1)
say(is_err($orphan), err_msg($orphan))`
	assertConcurrencyBoth(t, src, func(out string) bool {
		return strings.Contains(out, "true recv would deadlock") && strings.Contains(out, "true send would deadlock")
	})
}

func TestExecutionStrandsInheritAndRenewAtTaskBoundaries(t *testing.T) {
	env := NewEnv()
	root := env.executionStrand()
	if root == nil {
		t.Fatal("root environment has no execution strand")
	}
	if got := env.child().executionStrand(); got != root {
		t.Fatal("lexical child unexpectedly renewed its execution strand")
	}

	first, err := newClosureSnapshot().cloneEnv(env, 0)
	if err != nil {
		t.Fatalf("first task snapshot: %v", err)
	}
	second, err := newClosureSnapshot().cloneEnv(env, 0)
	if err != nil {
		t.Fatalf("second task snapshot: %v", err)
	}
	if first.executionStrand() == root || second.executionStrand() == root || first.executionStrand() == second.executionStrand() {
		t.Fatal("independent task snapshots did not receive distinct execution strands")
	}

	sequential, err := newClosureSnapshotForStrand(root).cloneEnv(env, 0)
	if err != nil {
		t.Fatalf("same-strand snapshot: %v", err)
	}
	if got := sequential.executionStrand(); got != root {
		t.Fatal("same-strand snapshot unexpectedly renewed its execution strand")
	}

	boundBuiltin := &Function{Name: "store_set", Env: env, Builtin: builtins["store_set"]}
	clonedBuiltin, err := newClosureSnapshot().cloneFunction(boundBuiltin, 0)
	if err != nil {
		t.Fatalf("env-bound builtin snapshot: %v", err)
	}
	if clonedBuiltin == boundBuiltin || clonedBuiltin.Env == env || clonedBuiltin.Env.executionStrand() == root {
		t.Fatal("env-bound builtin retained its source environment or execution strand")
	}
	statelessBuiltin := &Function{Name: "chan", Builtin: builtins["chan"]}
	clonedStateless, err := newClosureSnapshot().cloneFunction(statelessBuiltin, 0)
	if err != nil || clonedStateless != statelessBuiltin {
		t.Fatalf("stateless builtin snapshot = %p, %v; want shared %p", clonedStateless, err, statelessBuiltin)
	}
}

func TestTaskCompletionAtomicallyHandsOffToAwaiter(t *testing.T) {
	src := `$received := []
for $i in 0..40 {
  $c := chan()
  $sender := spawn(|$ch, $v| send($ch, $v), $c, $i)
  sleep(0.001)
  $short := spawn(|| sleep(0.001))
  await($short)
  push($received, recv($c))
  await($sender)
}
say(len($received), sum($received))`
	assertConcurrencyBoth(t, src, func(out string) bool { return out == "41 820\n" })
}

func TestTaskCompletionHandsOffToMultipleAwaiters(t *testing.T) {
	src := `$task := spawn(|| { sleep(0.02); 7 })
$c := chan()
$first := spawn(|| send($c, await($task)))
$second := spawn(|| send($c, await($task)))
$main := await($task)
$sum := recv($c) + recv($c)
say($main, $sum, await($first), await($second))`
	assertConcurrencyBoth(t, src, func(out string) bool { return out == "7 14 true true\n" })
}

func TestPmapCompletionAtomicallyHandsOffToCaller(t *testing.T) {
	src := `$received := []
for $i in 0..40 {
  $c := chan()
  $sender := spawn(|$ch, $v| send($ch, $v), $c, $i)
  sleep(0.001)
  pmap([1], |$x| $x)
  push($received, recv($c))
  await($sender)
}
say(len($received), sum($received))`
	assertConcurrencyBoth(t, src, func(out string) bool { return out == "41 820\n" })
}

func TestDirectConcurrencyWrappersSupplySchedulerContext(t *testing.T) {
	bareChan := &Function{Name: "chan", Builtin: builtins["chan"]}
	channel, err := callFunction(bareChan, nil, 0)
	if err != nil || channel.Tag() != value.Chan {
		t.Fatalf("nil-Env first-class chan = %s, %v", channel.Display(), err)
	}

	identity := &Function{Name: "identity", Builtin: func(args []value.Value) (value.Value, error) {
		return args[0], nil
	}}

	spawned, err := evalSpawn([]value.Value{
		value.MakeObj(value.Func, identity),
		value.MakeInt(9),
	})
	if err != nil || spawned.Tag() != value.Task {
		t.Fatalf("direct evalSpawn = %s, %v", spawned.Display(), err)
	}
	got, err := builtinAwait([]value.Value{spawned})
	if err != nil || got.Tag() != value.Int || got.AsInt() != 9 {
		t.Fatalf("direct evalSpawn/await = %s, %v", got.Display(), err)
	}

	mapped, err := hofPmap(&value.Array{Elems: []value.Value{value.MakeInt(3), value.MakeInt(4)}}, identity, 0)
	if err != nil || mapped.IsErr() || mapped.Obj().(*value.Array).Len() != 2 {
		t.Fatalf("direct hofPmap = %s, %v", mapped.Display(), err)
	}
}

func TestServeHandlerOwnsAndReleasesRunnableStrand(t *testing.T) {
	src := `$handler := |$req| {
  $short := spawn(|| { sleep(0.002); "ready" })
  $ready := await($short)
  $c := chan()
  $sender := spawn(|$ch, $v| { sleep(0.002); send($ch, $v) }, $c, $ready)
  $got := recv($c)
  await($sender)
  $got
}`
	p := parser.New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse errors: %v", errs)
	}
	env := NewEnv()
	if err := RunProgram(prog, env); err != nil {
		t.Fatalf("define handler: %v", err)
	}
	handlerValue, ok := env.get("handler")
	if !ok {
		t.Fatal("handler binding missing")
	}
	handler, ok := asFunction(handlerValue)
	if !ok {
		t.Fatal("handler binding is not a function")
	}

	type callResult struct {
		v   value.Value
		err error
	}
	done := make(chan callResult, 1)
	go func() {
		v, err := callHandlerSafely(handler, value.MakeMap())
		done <- callResult{v: v, err: err}
	}()
	select {
	case got := <-done:
		if got.err != nil || got.v.Tag() != value.Str || got.v.AsStr() != "ready" {
			t.Fatalf("handler result = %s, %v", got.v.Display(), got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handler await/channel accounting hung")
	}

	ctx := env.executionContext()
	ctx.mu.Lock()
	runnable, waiters, mainActive := ctx.runnable, len(ctx.waiters), ctx.mainActive
	ctx.mu.Unlock()
	if runnable != 0 || waiters != 0 || mainActive {
		t.Fatalf("handler leaked scheduler state: runnable=%d waiters=%d mainActive=%v", runnable, waiters, mainActive)
	}
}

func TestServeHandlerPreservesSerializedCapturedState(t *testing.T) {
	src := `$state := []
$next := || { push($state, 1); len($state) }
$handler := |$req| $next()`
	p := parser.New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse errors: %v", errs)
	}
	env := NewEnv()
	if err := RunProgram(prog, env); err != nil {
		t.Fatalf("define stateful handler: %v", err)
	}
	handlerValue, ok := env.get("handler")
	if !ok {
		t.Fatal("handler binding missing")
	}
	handler, ok := asFunction(handlerValue)
	if !ok {
		t.Fatal("handler binding is not a function")
	}
	for want := int64(1); want <= 2; want++ {
		got, err := callHandlerSafely(handler, value.MakeMap())
		if err != nil || got.Tag() != value.Int || got.AsInt() != want {
			t.Fatalf("handler call %d = %s, %v", want, got.Display(), err)
		}
	}
}

func TestCrossContextChannelOperationsCanMatch(t *testing.T) {
	c := &Chan{}
	a, b := newExecutionContext(), newExecutionContext()
	aOwned, bOwned := a.beginRun(), b.beginRun()
	defer a.endRun(aOwned)
	defer b.endRun(bOwned)

	// Hold the channel lock until both contexts have announced an entering
	// operation. Whichever acquires it first must leave a matchable waiter for the
	// other rather than declaring the other run irrelevant.
	c.mu.Lock()
	received := make(chan value.Value, 1)
	recvErr := make(chan error, 1)
	sendErr := make(chan error, 1)
	go func() {
		v, _, err := c.recvContext(a)
		received <- v
		recvErr <- err
	}()
	go func() { sendErr <- c.sendContext(value.MakeInt(7), b) }()
	deadline := time.Now().Add(time.Second)
	for c.pending.Load() != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if c.pending.Load() != 2 {
		c.mu.Unlock()
		t.Fatal("cross-context operations did not both enter")
	}
	c.mu.Unlock()

	select {
	case err := <-sendErr:
		if err != nil {
			t.Fatalf("cross-context send: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cross-context send hung")
	}
	if err := <-recvErr; err != nil {
		t.Fatalf("cross-context recv: %v", err)
	}
	if v := <-received; v.AsInt() != 7 {
		t.Fatalf("cross-context received %s", v.Display())
	}
}

func TestSendAndAwaitUseBoundedClosureSnapshots(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{`$state := []
$c := chan(1)
send($c, || { push($state, 1); len($state) })
$copy := recv($c)
say($copy(), len($state))`, "1 0\n"},
		{`$state := []
$t := spawn(|| || { push($state, 1); len($state) })
$a := await($t)
$b := await($t)
say($a(), $b())`, "1 1\n"},
		{`$deep := 0
for $i in 0..520 { $deep = [$deep] }
$c := chan(1)
$r := send($c, $deep)
say(is_err($r), err_code($r), err_msg($r))`, "true 137 send: snapshot exceeds depth limit 512\n"},
		{`$t := spawn(|| {
  $deep := 0
  for $i in 0..520 { $deep = [$deep] }
  $deep
})
$r := await($t)
say(is_err($r), err_code($r), err_msg($r))`, "true 137 await: snapshot exceeds depth limit 512\n"},
	}
	for _, tc := range cases {
		assertConcurrencyBoth(t, tc.src, func(out string) bool { return out == tc.want })
	}
}
