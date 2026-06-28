package printer

import (
	"testing"

	"github.com/anafalanx/drang/internal/parser"
)

func mustFormat(t *testing.T, src string) string {
	t.Helper()
	p := parser.New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse %q: %v", src, errs)
	}
	return Program(prog)
}

// corpus covers most node types; used for the reparse + idempotence invariants.
var corpus = []string{
	`say(1 + 2 * 3)`,
	`say((1 + 2) * 3)`,
	`say(1 - 2 - 3)`,
	`say(1 - (2 - 3))`,
	`$x := 5`,
	`$k ::= 10`,
	`$x = $x + 1`,
	`$a += 2`,
	`say("hello")`,
	`say("hi $name and ${1 + 2}")`,
	`say(qq{a $b})`,
	`say(q{raw})`,
	`say(qw{a b c})`,
	`say(qr/foo/i)`,
	`say([1, 2, 3])`,
	`say({"a": 1, "b": 2})`,
	`say(1..10)`,
	`say(-5, !true)`,
	`say(- -3)`,
	`say(a // b)`,
	`say(x?)`,
	`say(m.field)`,
	`say(arr[0])`,
	`[1, 2, 3] |> map(|$x| $x * 2) |> sum()`,
	`5 |> double`,
	`fn .add($a, $b = 1) { $a + $b }`,
	`fn .f($x) { return $x }`,
	`if x { say(1) } else { say(2) }`,
	`if x { say(1) } else if y { say(2) } else { say(3) }`,
	`while x { say(1) }`,
	`for $i, $v in xs { say($v) }`,
	`say(1) if cond`,
	`say(1) unless cond`,
	`say(1) while cond`,
	`say(1) until cond`,
	`say($x) for xs`,
	`while c { break }`,
	`while c { next }`,
	`fn .g() { return }`,
	`use "mod"`,
	`BEGIN { say(1) }`,
	`|$x| $x + 1`,
	`|$x| { say($x); $x }`,
}

// TestReparseAndIdempotent is the core correctness invariant: formatted output must
// re-parse cleanly, and formatting it again must be a no-op (fmt(fmt(x)) == fmt(x)).
func TestReparseAndIdempotent(t *testing.T) {
	for _, src := range corpus {
		once := mustFormat(t, src)
		p := parser.New(once)
		p.ParseProgram()
		if errs := p.Errors(); len(errs) > 0 {
			t.Errorf("formatted %q -> %q does not reparse: %v", src, once, errs)
			continue
		}
		if twice := mustFormat(t, once); twice != once {
			t.Errorf("not idempotent for %q:\n  once=%q\n twice=%q", src, once, twice)
		}
	}
}

// TestFormatGolden locks in the canonical style for representative constructs.
func TestFormatGolden(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"arith", `say(1+2*3)`, "say(1 + 2 * 3)\n"},
		{"parens-kept", `say((1+2)*3)`, "say((1 + 2) * 3)\n"},
		{"left-assoc-no-parens", `1-2-3`, "1 - 2 - 3\n"},
		{"right-assoc-parens", `1-(2-3)`, "1 - (2 - 3)\n"},
		{"decl", `$x:=5`, "$x := 5\n"},
		{"const", `$k::=10`, "$k ::= 10\n"},
		{"compound", `$a+=2`, "$a += 2\n"},
		{"pipe", `[1,2,3]|>map(|$x|$x*2)`, "[1, 2, 3] |> map(|$x| $x * 2)\n"},
		{"range", `1..10`, "1..10\n"},
		{"defor", `a//b`, "a // b\n"},
		{"qw", `qw{a b c}`, "qw{a b c}\n"},
		{"interp", `"hi $name"`, "\"hi $name\"\n"},
		{"lambda-expr-from-block", `|$x|{$x+1}`, "|$x| $x + 1\n"},
		{"fn", `fn .f($x){return $x}`, "fn .f($x) {\n\treturn $x\n}\n"},
		{"if-else", `if x{say(1)}else{say(2)}`, "if x {\n\tsay(1)\n} else {\n\tsay(2)\n}\n"},
		{"postfix-if", `say(1) if c`, "say(1) if c\n"},
		{"postfix-unless", `say(1) unless c`, "say(1) unless c\n"},
	}
	for _, c := range cases {
		if got := mustFormat(t, c.in); got != c.want {
			t.Errorf("%s: format(%q) =\n%q\nwant\n%q", c.name, c.in, got, c.want)
		}
	}
}
