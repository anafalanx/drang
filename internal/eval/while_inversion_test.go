package eval

import "testing"

// TestWhileInversionParity locks the register-mode (inverted / bottom-testing) while loop
// against the tree-walker oracle. EVERY case is wrapped in a function so it compiles in
// register mode (top-level while stays Env-mode / top-testing, and FuzzBackendParity
// excludes while entirely), which means these deterministic assertBoth cases are the ONLY
// thing that exercises the inverted OpJmpTrue* back-edge byte-for-byte against the walker.
func TestWhileInversionParity(t *testing.T) {
	// Basic bottom-testing loop: OpJmpTrueLt back-edge.
	assertBoth(t, `fn .sum($n) { $s := 0; $i := 0; while $i < $n { $s += $i; $i += 1 }; $s }
say(.sum(5))`, "10\n")

	// Zero-iteration: the one-time entry jump lands on the bottom test first, so a
	// false-on-entry condition never runs the body — same as the top-testing walker.
	assertBoth(t, `fn .z($n) { $i := 5; while $i < $n { $i += 1 }; $i }
say(.z(0))`, "5\n")

	// A while as the function tail yields nil (OpLoadNil stays before the entry jump).
	assertBoth(t, `fn .nilret() { $i := 0; while $i < 3 { $i += 1 } }
say(.nilret() // "NIL")`, "NIL\n")
	// ...even when a prior statement wrote the result slot.
	assertBoth(t, `fn .nilret2() { $r := 99; $i := 0; while $i < 2 { $i += 1 }; while $i < 1 { $i += 1 } }
say(.nilret2() // "NIL")`, "NIL\n")

	// next re-tests the BOTTOM condition and skips the trailing statement (would loop
	// forever or return a wrong value if next were mis-targeted to the body top).
	assertBoth(t, `fn .nxt() { $i := 0; while $i < 3 { $i += 1; next; $i += 100 }; $i }
say(.nxt())`, "3\n")
	// next mid-body inside a nested if, with a trailing statement that must be skipped.
	assertBoth(t, `fn .nxt2() { $i := 0; $t := 0; while $i < 4 { $i += 1; if $i == 2 { next }; $t += 1 }; str($i) ~ "," ~ str($t) }
say(.nxt2())`, "4,3\n")

	// break exits the innermost loop only (correct level under inversion).
	assertBoth(t, `fn .brk() { $i := 0; while $i < 10 { if $i == 3 { break }; $i += 1 }; $i }
say(.brk())`, "3\n")
	// Nested while: inner break + outer continues; verifies break/next bind to c.loops[-1].
	assertBoth(t, `fn .nest() { $o := ""; $i := 0; while $i < 3 { $j := 0; while $j < 3 { if $j == 1 { break }; $o = $o ~ str($i) ~ str($j); $j += 1 }; $o = $o ~ "|"; $i += 1 }; $o }
say(.nest())`, "00|10|20|\n")
	// Nested with inner next AND outer next.
	assertBoth(t, `fn .nest2() { $o := ""; $i := 0; while $i < 4 { $i += 1; if $i == 2 { next }; $j := 0; while $j < 2 { $j += 1; if $j == 1 { next }; $o = $o ~ str($i) ~ ":" ~ str($j) ~ " " }; }; $o }
say(.nest2())`, "1:2 3:2 4:2 \n")

	// All four ordered OpJmpTrue* back-edges as decrementing/incrementing loops.
	assertBoth(t, `fn .le() { $i := 5; $a := ""; while 0 <= $i { $a = $a ~ str($i); $i -= 1 }; $a }
say(.le())`, "543210\n")
	assertBoth(t, `fn .gt() { $i := 5; $a := ""; while $i > 0 { $a = $a ~ str($i); $i -= 1 }; $a }
say(.gt())`, "54321\n")
	assertBoth(t, `fn .ge() { $i := 5; $a := ""; while $i >= 1 { $a = $a ~ str($i); $i -= 1 }; $a }
say(.ge())`, "54321\n")

	// Equality conditions -> OpJmpTrueEq / OpJmpTrueNe.
	assertBoth(t, `fn .ne() { $i := 0; while $i != 5 { $i += 1 }; $i }
say(.ne())`, "5\n")

	// Non-comparison (truthy) condition -> OpJumpIfTruthy fallback back-edge.
	assertBoth(t, `fn .truthy() { $n := 3; while $n { $n -= 1 }; $n }
say(.truthy())`, "0\n")

	// A function-call condition (also OpJumpIfTruthy) with a SIDE EFFECT: the condition
	// must be evaluated the same number of times as the top-testing walker (once per
	// would-continue decision, before the first body). $e counts evals.
	assertBoth(t, `$e := 0
fn .c($i, $n) { $e = $e + 1; $i < $n }
fn .run() { $o := 0; $i := 0; while .c($i, 2) { $j := 0; while .c($j, 3) { $o = $o + 1; $j = $j + 1 }; $i = $i + 1 }; $o }
say("o=" ~ str(.run()) ~ " e=" ~ str($e))`, "o=6 e=11\n")

	// Float condition: non-int operands fall through to compare() (never the int fast
	// path), so a terminating float loop behaves identically on both backends.
	assertBoth(t, `fn .fl() { $x := 0.0; $c := 0; while $x < 2.5 { $x += 0.5; $c += 1 }; $c }
say(.fl())`, "5\n")

	// A body-declared variable must not leak between iterations or after the loop.
	assertBoth(t, `fn .noleak() { $i := 0; while $i < 3 { $x := $i * 10; $i += 1 }; $i }
say(.noleak())`, "3\n")
}

// TestWhileInversionEmitsJmpTrue is a structural guard: it proves a register-mode
// function's while actually compiles to the INVERTED (bottom-testing) form, so the
// assertBoth cases above genuinely exercise it. Without this, a future refactor that
// pushed the loop back to Env-mode / top-testing would leave every parity case passing
// while silently testing the wrong code path (the fuzzer can't catch it — while is
// excluded there).
func TestWhileInversionEmitsJmpTrue(t *testing.T) {
	prog := mustParseProg(t, `fn .f($n) { $i := 0; while $i < $n { $i += 1 }; $i }
.f(3)`)
	p, ok := compileProgram(prog)
	if !ok {
		t.Fatal("program did not compile to the VM")
	}
	var fProto *Proto
	for _, tpl := range p.Protos {
		if tpl.Name == ".f" && tpl.Proto != nil {
			fProto = tpl.Proto
		}
	}
	if fProto == nil {
		t.Fatal(".f did not compile to a register-mode Proto")
	}
	if !fProto.RegLocals {
		t.Fatal(".f is not RegLocals; its while would not be inverted")
	}
	hasJmpTrue, hasBackwardJump := false, false
	for i, in := range fProto.Code {
		if in.Op >= OpJmpTrueLt && in.Op <= OpJmpTrueNe {
			hasJmpTrue = true
		}
		if in.Op == OpJump && in.B < int32(i) { // a backward OpJump = a top-testing back-edge
			hasBackwardJump = true
		}
	}
	if !hasJmpTrue {
		t.Errorf(".f's while did not emit an OpJmpTrue* back-edge — not inverted")
	}
	if hasBackwardJump {
		t.Errorf(".f still has a backward OpJump — a top-testing back-edge survived inversion")
	}
}
