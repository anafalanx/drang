package eval

import "testing"

// TestCompoundCompareIntSpec locks the byte-for-byte behavior of the int fast paths added
// to the compound-assignment opcodes (OpCompoundLocal/LocalK/Var) and the fused
// compare-branch opcodes (OpJmpFalseLt/Le/Gt/Ge). Each case runs on BOTH backends via
// assertBoth, so a fast path that ever diverges from the shared compound()/compare()
// helpers the walker uses fails here. The error-outcome locks live in vmParityErr.
func TestCompoundCompareIntSpec(t *testing.T) {
	// Fused compare-branch as a while BACK-EDGE for > and >= — previously exercised only
	// in if-position. These drive OpJmpFalseGt / OpJmpFalseGe every iteration; the
	// jump-if-false negation (`if !(a > b)`) must terminate the loop correctly.
	assertBoth(t, `$i := 5
$acc := ""
while $i > 0 { $acc ~= str($i); $i -= 1 }
say($acc)`, "54321\n")
	assertBoth(t, `$i := 5
$acc := ""
while $i >= 1 { $acc ~= str($i); $i -= 1 }
say($acc)`, "54321\n")
	// OpJmpFalseLe as a while back-edge (<= in a decrementing loop): counts down to 0.
	assertBoth(t, `$i := 5
$acc := ""
while 0 <= $i { $acc ~= str($i); $i -= 1 }
say($acc)`, "543210\n")

	// A RegLocals function body: $total and $i are register locals, so this drives the
	// OpCompoundLocal (`$total += $i * 2`, register rhs) and OpCompoundLocalK (`$i += 1`,
	// folded const rhs) fast paths, plus OpJmpFalseLt for the `while $i < $n` condition.
	assertBoth(t, `fn .sum($n) {
  $total := 0
  $i := 0
  while $i < $n { $total += $i * 2; $i += 1 }
  $total
}
say(.sum(5))`, "20\n")

	// Negative-operand modulo must keep Go truncated-division sign (the fast path uses the
	// same `a % b` operator arith() does).
	assertBoth(t, `$n := 0 - 17
$n %= 5
say($n)`, "-2\n")

	// Comparison exactness above 2^53: the fast path compares via AsInt() (int64), not
	// float64 — a float path would collapse these adjacent values to equal.
	assertBoth(t, `$hi := 9007199254740992
if $hi < 9007199254740993 { say("lt") } else { say("ge") }`, "lt\n")

	// Non-arithmetic compound ops must fall through (never enter the int fast path):
	// //= is defined-or (keeps a present int cur), ~= concatenates via Display(), and /=
	// is always float division even for two ints.
	assertBoth(t, `$x := 3
$x //= 99
say($x)`, "3\n")
	assertBoth(t, `$n := 7
$n ~= "!"
say($n)`, "7!\n")
	assertBoth(t, `$x := 7
$x /= 2
say($x)`, "3.5\n")

	// Mixed int|float compound falls through to the float branch (rhs Tag != Int).
	assertBoth(t, `$x := 3
$x += 1.5
say($x)`, "4.5\n")
}
