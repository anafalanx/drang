package eval

import (
	"strings"
	"testing"
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
