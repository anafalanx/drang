package eval

import (
	"testing"

	"github.com/anafalanx/drang/internal/printer"
)

func fmtSrc(t *testing.T, src string) string {
	t.Helper()
	out, err := printer.Format(src)
	if err != nil {
		t.Fatalf("format %q: %v", src, err)
	}
	return out
}

// TestFmtRunEquality is the semantic-fidelity invariant for the formatter: a program
// and its formatted form must produce identical output (run-before == run-after), the
// formatted form must re-run cleanly, and formatting must be idempotent. It reuses the
// VM parity corpus plus formatter-specific constructs (pipes, interpolation, postfix,
// qw, defaults, ranges).
func TestFmtRunEquality(t *testing.T) {
	progs := append([]string{}, vmSubset...)
	progs = append(progs,
		`say([1, 2, 3] |> map(|$x| $x * 2) |> reduce(0, |$a, $b| $a + $b))`,
		"$x := 5\nsay(\"v=$x ${$x + 1}\")",
		`say(qq{hi there})`,
		`say(qw{a b c}[2])`,
		`say(qr/abc/i)`,
		"fn .inc($x, $by = 1) { $x + $by }\nsay(.inc(5), .inc(5, 10))",
		`say("yes") if 1 == 1`,
		`say("no") unless 1 == 1`,
		"$m := {}\nsay($m[\"x\"] // -1)",
		`say(1..3)`,
		`say(-5, !true, - -3)`,
		"$s := 0\nsay($s + 1) for [1, 2, 3]",
		// a wide pipe chain (wraps to multiple lines with trailing |>) must still compute 66
		"$r := [1, 2, 3, 4, 5, 6, 7, 8] |> map(|$e| $e * 2) |> filter(|$e| $e > 4) |> reduce(0, |$a, $e| $a + $e)\nsay($r)",
		// pre-0.4 hardening (Bug 4): a pipe stage that is a call-of-a-call must keep its
		// outer () through a reformat, and x |> f()? must stay (x |> f())?. The run-before
		// == run-after invariant catches the formatter silently changing the parse.
		"fn .make() { |$x| $x * 100 }\nsay(7 |> .make()())",
		"fn .dbl($x) { $x * 2 }\nsay(10 |> .dbl()?)",
		"fn .id($x) { $x }\nsay((5 |> .id()) == 5)",
	)
	for _, src := range progs {
		want := run(t, src)
		f := fmtSrc(t, src)
		if twice := fmtSrc(t, f); twice != f {
			t.Errorf("not idempotent for:\n%s\n once=%q\n twice=%q", src, f, twice)
		}
		if got := run(t, f); got != want {
			t.Errorf("run mismatch for:\n%s\nformatted to:\n%s\n want=%q got=%q", src, f, want, got)
		}
	}
}
