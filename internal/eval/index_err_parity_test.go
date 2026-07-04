package eval

import "testing"

// TestIndexErrOperandParity locks the fix for a VM/walker divergence on indexing an Err
// base with an ERRORING index. The walker used to short-circuit on an Err base BEFORE
// evaluating the index, so `errBase[0%0]` flowed the base error on the walker but aborted
// (modulo by zero) on the VM (which computes the index into a register before OpIndex).
// Now both evaluate the index, so a hard abort in the index aborts on both backends — and
// an Err base with a non-erroring index still flows the base error on both.
func TestIndexErrOperandParity(t *testing.T) {
	// Err base + hard-aborting index -> both backends abort identically (found by fuzzing).
	for _, src := range []string{
		`[][0][0%0]`,             // out-of-range Err base, modulo-by-zero index
		`[][0][5%0]`,             // same, different modulo
		`$a := [1]; $a[9][0%0]`,  // out-of-range base via a variable, then erroring index
		`{}["missing"][0][0%0]`,  // nested: map miss is nil, [0] on nil -> Err, then erroring index
	} {
		wOut, wErr := runBackend(t, src, false)
		vOut, vErr := runBackend(t, src, true)
		if (wErr == nil) != (vErr == nil) {
			t.Errorf("error-outcome mismatch for %q: walker=%v vm=%v", src, wErr, vErr)
		}
		if wOut != vOut {
			t.Errorf("output mismatch for %q: walker=%q vm=%q", src, wOut, vOut)
		}
	}

	// An Err base with a NON-erroring index still flows the base error (both backends).
	assertBoth(t, `say([][0][5] // "flowed")`, "flowed\n")
	assertBoth(t, `$e := [][0][5]
say(is_err($e))`, "true\n")
	// The index's own side effect now runs even under an Err base (matching the VM).
	assertBoth(t, `$log := []
$r := [][0][push($log, "idx")]
say(is_err($r) ~ " " ~ str(len($log)))`, "true 1\n")
}
