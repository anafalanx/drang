package eval

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/anafalanx/drang/internal/parser"
)

// TestErrOperandShortCircuits: an unhandled Err flowing into arithmetic, ordering comparison,
// <=>, unary minus, or len returns the Err VALUE (message preserved, recoverable by //) rather
// than hard-aborting — matching what index/field reads already do — on both backends.
func TestErrOperandShortCircuits(t *testing.T) {
	assertBoth(t, `say((fail("boom") + 1) // "R")`, "R\n")
	assertBoth(t, `say((fail("boom") - 1) // "R")`, "R\n")
	assertBoth(t, `say((fail("boom") * 2) // "R")`, "R\n")
	assertBoth(t, `say((fail("boom") < 1) // "R")`, "R\n")
	assertBoth(t, `say((fail("boom") >= 1) // "R")`, "R\n")
	assertBoth(t, `say((fail("boom") <=> 1) // "R")`, "R\n")
	assertBoth(t, `say((1 + fail("boom")) // "R")`, "R\n") // right operand too
	assertBoth(t, `say((-fail("boom")) // "R")`, "R\n")
	// The original message is preserved through the operation (not a generic type error).
	assertBoth(t, `say(err_msg(fail("boom") + 1))`, "boom\n")
	assertBoth(t, `say(err_msg(len(fail("boom"))))`, "boom\n")
	// == / != remain total (structural), not short-circuited: an Err is simply unequal to a non-Err.
	assertBoth(t, `say(fail("boom") == 1)`, "false\n")
	assertBoth(t, `say(fail("boom") != 1)`, "true\n")
}

// TestForInErrPropagates: iterating an unhandled Err propagates it (for is a statement, so it
// cannot yield a value) with the original message preserved and recoverable at the call boundary.
func TestForInErrPropagates(t *testing.T) {
	src := `fn .f() { for $x in fail("root cause") { }  "done" }
say(.f() // "recovered")`
	assertBoth(t, src, "recovered\n")

	// At the top level it aborts with the ORIGINAL message, not a generic "cannot iterate" one.
	for _, vm := range []bool{false, true} {
		_, err := runBackend(t, `for $x in fail("root cause") { }`, vm)
		if err == nil {
			t.Fatalf("vm=%v: expected top-level for-over-Err to surface an error", vm)
		}
		if !strings.Contains(err.Error(), "root cause") {
			t.Errorf("vm=%v: error lost the original message: %v", vm, err)
		}
	}
}

// bothBackends runs src on the tree-walker and the VM and returns their outputs, failing
// on any runtime error. It is the parity harness for the correctness fixes below.
func bothBackends(t *testing.T, src string) (walker, vm string) {
	t.Helper()
	wOut, wErr := runBackend(t, src, false)
	if wErr != nil {
		t.Fatalf("walker error for %q: %v", src, wErr)
	}
	vOut, vErr := runBackend(t, src, true)
	if vErr != nil {
		t.Fatalf("vm error for %q: %v", src, vErr)
	}
	return wOut, vOut
}

func assertBoth(t *testing.T, src, want string) {
	t.Helper()
	w, v := bothBackends(t, src)
	if w != want {
		t.Errorf("walker: got %q, want %q\nsrc: %s", w, want, src)
	}
	if v != want {
		t.Errorf("vm: got %q, want %q\nsrc: %s", v, want, src)
	}
}

// TestRecursionGuardCatchable proves unbounded recursion becomes a catchable Err rather
// than a fatal Go stack overflow — on BOTH backends. Reaching the guard also exercises
// maxCallDepth frames on each backend, so if the limit were set too high to be safe this
// test would crash the process (the walker uses the most Go frames per call).
func TestRecursionGuardCatchable(t *testing.T) {
	// Recovered with // — the program completes normally.
	assertBoth(t, `fn .f($n) { .f($n + 1) }  say(.f(0) // "BOUNDED")`, "BOUNDED\n")
	// Inspected as an ordinary error value.
	assertBoth(t, `fn .f($n) { .f($n + 1) }
$r := .f(0)
say(is_err($r))`, "true\n")
}

// TestRecursionGuardMessage checks the Err is self-describing.
func TestRecursionGuardMessage(t *testing.T) {
	w, v := bothBackends(t, `fn .f($n) { .f($n + 1) }  say(err_msg(.f(0)))`)
	for _, got := range []string{w, v} {
		if got == "" || got[:4] != "call" {
			t.Errorf("depth Err message = %q, want it to start with \"call depth exceeded\"", got)
		}
	}
}

// TestRecursionGuardAllowsLegitDepth confirms the limit does not reject ordinary deep
// recursion: summing 1..2000 recurses 2000 deep and must complete on both backends.
func TestRecursionGuardAllowsLegitDepth(t *testing.T) {
	assertBoth(t, `fn .sum($n) { if $n == 0 { return 0 }  $n + .sum($n - 1) }  say(.sum(2000))`, "2001000\n")
}

// TestRecursionThroughHOF proves recursion that re-enters through a higher-order function
// (map, here) is still bounded — a naive per-call-site counter would reset at each map and
// let the Go stack overflow. It must terminate with a catchable Err on both backends.
func TestRecursionThroughHOF(t *testing.T) {
	assertBoth(t, `fn .f($n) { map([$n], .f) }  say(is_err(.f(0)))`, "true\n")
}

// TestBranchingRunawayRecursionAborts is the regression for the branching-recursion
// hang: the depth guard bounds each recursion PATH, but base-case-less BRANCHING
// recursion (.f($n){.f($n-1)*.f($n-2)}) explores ~2^maxCallDepth sibling paths — each
// terminates, the tree does not, so the program effectively hung forever (and made
// FuzzBackendParity / the verify.dr release gate flaky). A storm of depth-guard fires
// now escalates to a LOUD aborting error, quickly, on both backends.
func TestBranchingRunawayRecursionAborts(t *testing.T) {
	src := "fn .boom($n) { .boom($n - 1) * .boom($n - 2) }\nsay(.boom(15))"
	for _, vm := range []bool{false, true} {
		start := time.Now()
		_, err := runBackend(t, src, vm)
		elapsed := time.Since(start)
		if err == nil {
			t.Fatalf("vm=%v: branching runaway recursion should abort with an error, got success", vm)
		}
		if !strings.Contains(err.Error(), "runaway recursion") {
			t.Errorf("vm=%v: want the runaway-recursion escalation, got: %v", vm, err)
		}
		// The whole point: this used to hang effectively forever (~2^4000 calls). The
		// storm burns at most maxOverflowFires cheap guard-fires and stops.
		if elapsed > 30*time.Second {
			t.Errorf("vm=%v: escalation took %v; the storm is not being bounded", vm, elapsed)
		}
	}
}

// TestCallBudgetBoundsSlowFinitePrograms covers the test-only execution budget the
// fuzz harness uses: recursion that HAS a base case but never progresses on one branch
// (.fib($n-1) + .fib($n)) is finite yet astronomically slow — legal in production,
// unverifiable under fuzz. With the budget set it must abort promptly on both backends.
func TestCallBudgetBoundsSlowFinitePrograms(t *testing.T) {
	testCallBudget = 200_000
	defer func() { testCallBudget = 0 }()
	src := "fn .fib($n) { if $n < 2 { return $n }\n.fib($n - 1) + .fib($n) }\nsay(.fib(15))"
	for _, vm := range []bool{false, true} {
		testCallsUsed.Store(0)
		start := time.Now()
		_, err := runBackend(t, src, vm)
		if err == nil {
			t.Fatalf("vm=%v: expected the call budget to abort this program", vm)
		}
		if !strings.Contains(err.Error(), "test call budget exceeded") {
			t.Errorf("vm=%v: want the budget error, got: %v", vm, err)
		}
		if elapsed := time.Since(start); elapsed > 30*time.Second {
			t.Errorf("vm=%v: budget abort took %v", vm, elapsed)
		}
	}
}

// TestRepeatedCaughtOverflowsDoNotEscalate: deliberately catching a few deep-recursion
// overflows keeps working — the escalation threshold is far above legitimate use, and
// the budget resets between runs (including on a REUSED env, the REPL situation).
func TestRepeatedCaughtOverflowsDoNotEscalate(t *testing.T) {
	assertBoth(t, `fn .down($n) { .down($n + 1) }
$ok := 0
for $i in [1, 2, 3] {
  if (.down(0) // "x") == "x" { $ok = $ok + 1 }
}
say($ok)`, "3\n")

	// A second run on the same env starts with a fresh budget (the entry reset).
	env := NewEnv()
	src := `fn .down($n) { .down($n + 1) }
say(.down(0) // "caught")`
	for i := 0; i < 2; i++ {
		p := parser.New(src)
		prog := p.ParseProgram()
		if errs := p.Errors(); len(errs) > 0 {
			t.Fatalf("parse: %v", errs)
		}
		var buf bytes.Buffer
		old := stdout
		stdout = &buf
		err := RunProgram(prog, env)
		stdout = old
		if err != nil {
			t.Fatalf("run %d on a reused env: %v", i+1, err)
		}
		if buf.String() != "caught\n" {
			t.Errorf("run %d: got %q, want %q", i+1, buf.String(), "caught\n")
		}
	}
}

// TestSpawnStormBecomesTaskErrWithoutPoisoning: a storm inside a spawned task follows
// the task model — the worker's aborting error becomes the task's Err at await — and
// must NOT poison the budget: the escalation resets the counter as it fires, so the
// program's next legitimate single overflow is still catchable.
func TestSpawnStormBecomesTaskErrWithoutPoisoning(t *testing.T) {
	old := maxOverflowFires
	maxOverflowFires = 2000
	defer func() { maxOverflowFires = old }()
	// await(spawn(...)) in ONE expression: the main goroutine blocks in await while
	// the worker recurses, so this test does not open the (pre-existing, known)
	// shallow-snapshot window where a worker's by-name lookups race main-goroutine
	// defines on the live env.
	assertBoth(t, `fn .boom($n) { .boom($n - 1) * .boom($n - 2) }
fn .down($n) { .down($n + 1) }
$r := await(spawn(.boom, 15))
say(is_err($r))
say(.down(0) // "caught")`, "true\ncaught\n")
}

// TestModuleOverflowChargesImporterCounter: a module function's depth-guard fires are
// charged to the IMPORTING run's counter (which the entry points reset), not to a
// private module universe that no reset ever reaches. Two runs on one env must each
// see the same per-run count — not an accumulating one.
func TestModuleOverflowChargesImporterCounter(t *testing.T) {
	dir := t.TempDir()
	writeMod(t, dir, "deep.dr", "fn .deep($n) { .deep($n + 1) }\ne fn .probe() { .deep(0) // \"caught\" }")
	env := NewEnv()
	env.SetModuleDir(dir)
	var buf bytes.Buffer
	oldOut := stdout
	stdout = &buf
	defer func() { stdout = oldOut }()
	// Run 1 imports the module; run 2 calls the already-merged function (re-importing
	// on the same env would be a same-name collision by design).
	for i, src := range []string{"use \"./deep\"\nsay(.probe())", "say(.probe())"} {
		p := parser.New(src)
		prog := p.ParseProgram()
		if errs := p.Errors(); len(errs) > 0 {
			t.Fatalf("parse: %v", errs)
		}
		if err := RunProgramWithArgs(prog, env, nil); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
		// One .probe() = one guard fire, charged to THIS env's counter and reset at
		// each entry — so it reads 1 after every run, never 2 (the accumulation bug).
		if got := env.stormCounter().Load(); got != 1 {
			t.Errorf("run %d: importer counter = %d, want 1 (module fires must charge the importing run)", i+1, got)
		}
	}
	if buf.String() != "caught\ncaught\n" {
		t.Errorf("output = %q, want two caught lines", buf.String())
	}
}

// TestStreamPerLineOverflowReset: stream mode treats each input line as its own run
// for the storm budget — per-line caught overflows must never accumulate into a
// spurious mid-stream abort.
func TestStreamPerLineOverflowReset(t *testing.T) {
	old := maxOverflowFires
	maxOverflowFires = 5
	defer func() { maxOverflowFires = old }()
	// Each line catches 3 overflows (under the lowered budget of 5); three lines
	// total 9, so WITHOUT the per-line reset line 2 would cross the limit and abort.
	src := `BEGIN { fn .deep($n) { .deep($n + 1) } }
for $i in [1, 2, 3] { $x := .deep(0) // 0 }
say($nr)`
	p := parser.New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	var out bytes.Buffer
	oldOut := stdout
	stdout = &out // say() writes to the package stdout, not StreamOpts.Stdout
	defer func() { stdout = oldOut }()
	err := RunStream(prog, nil, StreamOpts{Stdin: strings.NewReader("a\nb\nc\n")})
	if err != nil {
		t.Fatalf("stream run: %v", err)
	}
	if got := out.String(); got != "1\n2\n3\n" {
		t.Errorf("stream output = %q, want all three lines processed", got)
	}
}

// TestIntEqualityExact locks in that two int64 values are compared as int64, not via
// float64 (which collapses values above 2^53). Mixed int/float comparison is unchanged.
func TestIntEqualityExact(t *testing.T) {
	assertBoth(t, `say(9007199254740993 == 9007199254740992)`, "false\n")
	assertBoth(t, `say(9007199254740993 == 9007199254740993)`, "true\n")
	assertBoth(t, `say(9007199254740993 != 9007199254740992)`, "true\n")
	assertBoth(t, `say(1 == 1.0)`, "true\n") // cross-type numeric equality preserved
}

// TestIntOrderingExact is the <=> / ordering counterpart: adjacent large ints must not
// order as equal.
func TestIntOrderingExact(t *testing.T) {
	assertBoth(t, `say(9007199254740993 <=> 9007199254740992)`, "1\n")
	assertBoth(t, `say(9007199254740992 <=> 9007199254740993)`, "-1\n")
	assertBoth(t, `say(9007199254740993 <=> 9007199254740993)`, "0\n")
	assertBoth(t, `say(9007199254740993 > 9007199254740992)`, "true\n")
}

// TestStructuralEqualityDAGIsLinear guards against exponential blow-up on values with
// shared substructure. Without the visited-pair memo in equalDepth, comparing two
// 30-level shared DAGs does not terminate in reasonable time and this test hangs.
func TestStructuralEqualityDAGIsLinear(t *testing.T) {
	assertBoth(t, `$x := [1]
$y := [1]
for $i in 0..30 {
  $x = [$x, $x]
  $y = [$y, $y]
}
say($x == $y)`, "true\n")
	// And a genuine mismatch deep in a shared DAG is still detected (returns false).
	assertBoth(t, `$x := [1]
$y := [2]
for $i in 0..30 {
  $x = [$x, $x]
  $y = [$y, $y]
}
say($x == $y)`, "false\n")
}
