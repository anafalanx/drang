package eval

import (
	"bytes"
	"strings"
	"testing"

	"github.com/anafalanx/drang/internal/parser"
)

// These lock in the fixes from the pre-0.4 core-hardening pass. Each corresponds to a
// confirmed bug; the comment names it.

// Bug 1 (CRITICAL): a pmap callback that ASSIGNS a captured outer variable used to race
// the single shared fn.Env (concurrent map read/write → fatal crash). Each worker now
// runs over its own env snapshot. Run the suite with -race to exercise the guarantee.
func TestPmapSharedVarNoRace(t *testing.T) {
	got := run(t, "$a := 0\n$r := [1, 2, 3, 4, 5, 6, 7, 8] |> pmap(|$x| { $a = $x; $x * 2 })\nsay($r)")
	if got != "[2, 4, 6, 8, 10, 12, 14, 16]\n" {
		t.Errorf("pmap with a captured-var assignment: got %q", got)
	}
}

// Bug 2 (HIGH): cyclic structures used to overflow Go's (unrecoverable) stack in Display
// and Equal. A cycle now renders as a placeholder and self-compares via an identity fast
// path; a merely-shared (acyclic) sub-value still renders in full.
func TestCyclicValueNoStackOverflow(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"cyclic-display", "$a := [1]\npush($a, $a)\nsay($a)", "[1, [...]]\n"},
		{"cyclic-self-equal", "$a := [1]\npush($a, $a)\nsay($a == $a)", "true\n"},
		{"shared-not-cyclic", "$x := [1]\nsay([$x, $x])", "[[1], [1]]\n"},
		{"cyclic-map", "$m := {}\n$m[\"self\"] = $m\nsay($m)", "{self: {...}}\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := run(t, c.src); got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

// Bug 3 (HIGH): the VM used to silently swallow a top-level `return` (exit 0, rest of the
// program skipped). It now errors like the walker.
func TestTopLevelReturnErrorsOnVM(t *testing.T) {
	p := parser.New("say(1)\nreturn\nsay(2)")
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	err := RunProgramVM(prog, NewEnv())
	if err == nil {
		t.Fatal("a top-level return on the VM must error, not silently exit")
	}
	if !strings.Contains(err.Error(), "return outside") {
		t.Errorf("unexpected error: %v", err)
	}
}

// Bug 5 (HIGH): x |> f()? means (x |> f())? — pipe, then propagate the result.
func TestPipeThenPropagate(t *testing.T) {
	if got := run(t, "fn .dbl($x) { $x * 2 }\nsay(10 |> .dbl()?)"); got != "20\n" {
		t.Errorf("x |> f()?: got %q want \"20\\n\"", got)
	}
}

// Bug 6 (MED): spawn(fn, $a, $a) must preserve intra-call argument aliasing (one shared
// visited map across the copied args), matching a direct call.
func TestSpawnArgAliasing(t *testing.T) {
	got := run(t, "fn .work($x, $y) { push($x, 9); len($y) }\n$a := [1, 2]\nsay(.work($a, $a))\n$b := [1, 2]\nsay(await(spawn(.work, $b, $b)))")
	if got != "3\n3\n" {
		t.Errorf("spawn must preserve arg aliasing like a direct call: got %q want \"3\\n3\\n\"", got)
	}
}

// Bug 8 (MED): a returned/?-propagated Err from an each_line callback must surface as the
// result and stop the child, not be silently dropped (the loop checked only Go errors).
func TestEachLineCallbackErrSurfaces(t *testing.T) {
	out := runWithEnv(t, NewEnv(), "$r := each_line(\"cmd\", \"/c\", \"echo a& echo b& echo c\", |$l| { if trim($l) == \"b\" { return fail(\"stop\") } 0 })\nsay(is_err($r), err_msg($r))")
	if !strings.Contains(out, "true") || !strings.Contains(out, "stop") {
		t.Errorf("each_line must surface a callback Err: %q", out)
	}
}

// Bug 9 (LOW): a leading UTF-8 BOM is stripped so the first statement still parses.
func TestLeadingBOMStripped(t *testing.T) {
	if got := run(t, "\ufeffsay(42)"); got != "42\n" {
		t.Errorf("leading BOM: got %q want \"42\\n\"", got)
	}
}

// VM/walker parity for the new pipe forms (Bugs 4/5): the call-of-a-call stage, the
// pipe-then-propagate, and a parenthesized pipe compared to a value.
func TestPipeFormsBackendParity(t *testing.T) {
	srcs := []string{
		"fn .make() { |$x| $x * 100 }\nsay(7 |> .make()())",
		"fn .dbl($x) { $x * 2 }\nsay(10 |> .dbl()?)",
		"fn .id($x) { $x }\nsay((5 |> .id()) == 5)",
	}
	for _, src := range srcs {
		walk := run(t, src)
		p := parser.New(src)
		prog := p.ParseProgram()
		if errs := p.Errors(); len(errs) > 0 {
			t.Fatalf("parse %q: %v", src, errs)
		}
		var buf bytes.Buffer
		old := stdout
		stdout = &buf
		err := RunProgramVM(prog, NewEnv())
		stdout = old
		if err != nil {
			t.Fatalf("VM run %q: %v", src, err)
		}
		if buf.String() != walk {
			t.Errorf("parity %q: walker=%q vm=%q", src, walk, buf.String())
		}
	}
}
