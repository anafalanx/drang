package printer

import "testing"

// fuzzFmtSeeds seed the formatter corpus with programs that stress the reprinter's
// provenance handling: every string form, qw/qr literals, interpolation, pipelines,
// postfix modifiers, comments, ranges, maps, and lambdas with defaults. Mutations of
// these probe whether the printer stays a fixed point as syntax degrades.
var fuzzFmtSeeds = []string{
	`say("hello")`,
	`$x:=1+2*3
	say( $x )`,
	`$xs := [1,2,3]
say(map($xs,|$y| $y*2))`,
	`fn .fib($n){if $n<2{return $n}
.fib($n-1)+.fib($n-2)}`,
	`$m:={a:1,b:2}
for $k,$v in $m{say($k,$v)}`,
	`$s := $"interp $x and ${1+2}"`,
	`say(qw(a b c), q{raw}, qq{esc}, 'single')`,
	`say(qr/\d+/i)`,
	`$r := int("x") // -1`,
	`$v := trim(read()?) |> upper()`,
	`say(1) if true
say(2) unless false`,
	"# leading comment\nsay(1) # trailing\n$x := 2",
	`example 1 + 1 == 2`,
	"$h := <<END\nhello\nEND",
	`$f := |$a, $b = 10| $a + $b`,
	`$r := 1..10`,
}

// FuzzFmtRoundTrip asserts the formatter is a well-behaved normal form. For any
// input Format accepts, two laws must hold:
//
//  1. Stability — the formatted output re-formats without error. Format already
//     re-parses its own output internally (the comment drop-guard), so a failure here
//     means a second full pass regressed: comment reattachment or a Fix diverged.
//  2. Idempotence — Format(Format(x)) == Format(x). Running `drang fmt` twice must
//     change nothing; a diff here means the printer has no fixed point.
//
// Inputs Format rejects (parse errors) are out of scope — the round-trip law only
// governs source the formatter is willing to rewrite.
func FuzzFmtRoundTrip(f *testing.F) {
	for _, s := range fuzzFmtSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		once, err := Format(src)
		if err != nil {
			return // unformattable: not subject to the round-trip law
		}
		twice, err := Format(once)
		if err != nil {
			t.Fatalf("formatted output no longer formats:\n--- input ---\n%s\n--- formatted ---\n%s\n--- error ---\n%v", src, once, err)
		}
		if once != twice {
			t.Fatalf("format is not idempotent:\n--- input ---\n%s\n--- first pass ---\n%q\n--- second pass ---\n%q", src, once, twice)
		}
	})
}
