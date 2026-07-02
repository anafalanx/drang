package parser

import "testing"

// fuzzParseSeeds seed the parser corpus with a spread of real drang surface forms
// (so the fuzzer starts from valid coverage and mutates outward) plus a handful of
// adversarial fragments that historically trip lexers/parsers: unterminated
// strings/regex, lone sigils, deep nesting, and NUL bytes.
var fuzzParseSeeds = []string{
	// well-formed programs
	`say("hello")`,
	`$x := 1 + 2 * 3
say($x)`,
	`$xs := [1, 2, 3]
say(map($xs, |$y| $y * 2))`,
	`fn .fib($n) {
	if $n < 2 { return $n }
	.fib($n - 1) + .fib($n - 2)
}
say(.fib(10))`,
	`$m := {a: 1, b: 2}
for $k, $v in $m { say($k, $v) }`,
	`$s := $"interp $x and ${1 + 2}"`,
	`say(qw(a b c))`,
	`say(q{raw}, qq{esc}, '  single  ')`,
	`say(qr/\d+/i)`,
	`$r := int("x") // -1`,
	`$v := read()? |> trim() |> upper()`,
	`while $i < 10 { $i = $i + 1 }`,
	`say(1) if true
say(2) unless false
say(3) for 1..3`,
	`example 1 + 1 == 2`,
	"$h := <<END\nhello\nEND\nsay($h)",
	`$f := |$a, $b = 10| $a + $b`,
	`BEGIN { $n := 0 }
END { say($n) }`,
	// adversarial / malformed
	`$"unterminated`,
	`qw(a b`,
	`/unclosed`,
	`qr/`,
	`{{{{{{{{`,
	`((((((((((`,
	`[[[[[[[[[[`,
	`$`,
	`.`,
	`fn`,
	`fn .`,
	`|$x|`,
	`1..`,
	`say(`,
	`$x :=`,
	"\x00",
	"a\x00b",
	`==`,
	`}{}{`,
}

// FuzzParse asserts the front end is total: New(src).ParseProgram() must never
// panic or hang on ANY input — a well-formed program or a wall of garbage. A parse
// failure is a normal outcome (it lands in Errors()); a crash is a bug. This is the
// cheapest, broadest safety net drang has — the lexer and Pratt parser get shown
// millions of mutated byte strings, and every one must terminate with either an AST
// or a clean error list, never a runtime panic.
func FuzzParse(f *testing.F) {
	for _, s := range fuzzParseSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		p := New(src)
		prog := p.ParseProgram()
		errs := p.Errors()
		// ParseProgram always returns a (possibly empty) Program, even on error.
		if prog == nil {
			t.Fatalf("ParseProgram returned nil for %q (errors: %v)", src, errs)
		}
		// Rendering every node's S-expression form must also be total — this walks
		// the whole tree and catches nil-field panics in String() methods.
		_ = prog.String()
	})
}
