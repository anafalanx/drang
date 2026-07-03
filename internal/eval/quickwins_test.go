package eval

import (
	"strings"
	"testing"
)

// (reverse's polymorphism + guard is exercised in TestPrelude, where the prelude —
// which defines reverse — is loaded.)

// TestChannelDeadlockCatchable: a send/recv that could only ever deadlock (no
// counterparty, no other running task) is a catchable Err instead of a raw Go
// "all goroutines are asleep" fatal that aborts the process.
func TestChannelDeadlockCatchable(t *testing.T) {
	assertBoth(t, `say(is_err(send(chan(), 1)))`, "true\n")
	assertBoth(t, `say(is_err(recv(chan())))`, "true\n")
	// The error is catchable and recoverable, and names the problem.
	out := run(t, `say(send(chan(), 1) // "recovered")`)
	if strings.TrimSpace(out) != "recovered" {
		t.Errorf("deadlock Err should be recoverable with //, got %q", out)
	}
	out = run(t, `say(err_msg(send(chan(), 1)))`)
	if !strings.Contains(out, "deadlock") {
		t.Errorf("deadlock Err should name the problem, got %q", out)
	}
}

// TestChannelHandoffStillWorks: the deadlock guard must not break a legitimate
// blocking handoff — with a worker alive, send/recv block and rendezvous as before.
func TestChannelHandoffStillWorks(t *testing.T) {
	assertBoth(t, `$c := chan()
$t := spawn(|| send($c, 99))
$v := recv($c)
await($t)
say($v)`, "99\n")
	// A buffered channel takes a send with no receiver without blocking (no false deadlock).
	assertBoth(t, `$c := chan(1)
send($c, 7)
say(recv($c))`, "7\n")
}

// TestTrig covers the new trig/extended-math builtins on both backends: values,
// constants, domain errors, and type errors (catchable Err, not a silent NaN).
func TestTrig(t *testing.T) {
	assertBoth(t, `say(sin(0), cos(0), atan(0), exp(0))`, "0 1 0 1\n")
	assertBoth(t, `say(round(pi() * 1000000))`, "3141593\n")
	assertBoth(t, `say(round(atan2(1, 1) * 4 / pi()))`, "1\n") // atan2(1,1) == pi/4
	// domain / type errors are catchable Err values
	assertBoth(t, `say(is_err(asin(2)), is_err(acos(-2)))`, "true true\n")
	assertBoth(t, `say(is_err(sin("x")), is_err(exp("y")))`, "true true\n")
	// wrong arity still aborts (Go-error), so is_err can't catch it — the program fails
	for _, vm := range []bool{false, true} {
		if _, err := runBackend(t, `sin(1, 2)`, vm); err == nil {
			t.Errorf("vm=%v: sin with 2 args should abort (arity)", vm)
		}
	}
}

// TestCompoundAssignTrio locks in %=, ~=, //= on both backends, including the
// seeding rules (0 for arithmetic, "" for ~=) and //='s defined-or semantics.
func TestCompoundAssignTrio(t *testing.T) {
	assertBoth(t, `$n := 17
$n %= 5
say($n)`, "2\n")
	assertBoth(t, `$s := "a"
$s ~= "b"
$s ~= "c"
say($s)`, "abc\n")
	// //= keeps a present (non-nil, non-error) value, even a falsy one like 0
	assertBoth(t, `$x := 0
$x //= 42
say($x)`, "0\n")
	// //= takes the rhs when the slot is nil (fresh map key) or an error
	assertBoth(t, `$m := {}
$m.k //= "default"
say($m.k)`, "default\n")
	assertBoth(t, `$e := int("x")
$e //= "recovered"
say($e)`, "recovered\n")
	// ~= seeds a fresh key with "" (not nil-stringified); += still seeds with 0
	assertBoth(t, `$g := {}
$g["h"] ~= "x"
$g["h"] ~= "y"
say($g["h"])`, "xy\n")
	assertBoth(t, `$c := {}
$c["n"] += 1
$c["n"] += 1
say($c["n"])`, "2\n")
	// %= on an array slot, exercising the compound Index path too
	assertBoth(t, `$a := [17, 3]
$a[0] %= 5
say(str($a))`, "[2, 3]\n")
}
