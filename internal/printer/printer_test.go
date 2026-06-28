package printer

import (
	"strings"
	"testing"

	"github.com/anafalanx/drang/internal/parser"
)

func mustFormat(t *testing.T, src string) string {
	t.Helper()
	out, err := Format(src)
	if err != nil {
		t.Fatalf("format %q: %v", src, err)
	}
	return out
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
	`[1] |> map(|$c| ({a: $c, b: 2}))`, // map-bodied lambda needs parens
	`({a: 1})`,                         // map literal as a statement needs parens
	`({a: 1}).x`,                       // brace-leading field access
	// wide constructs (these wrap): a pipe chain, an array, a call, a map
	`$out := [1, 2, 3, 4, 5, 6, 7, 8] |> map(|$e| $e * 2) |> filter(|$e| $e > 4) |> reduce(0, |$acc, $e| $acc + $e)`,
	`$xs := ["alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet", "kilo"]`,
	`say("one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve", "x")`,
	`$m := {"alpha": 1, "bravo": 2, "charlie": 3, "delta": 4, "echo": 5, "foxtrot": 6, "golf": 7, "hotel": 8, "i": 9}`,
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

// TestWrapping verifies wide constructs break across lines (pipe with trailing |>;
// call/array/map one-per-line) and short ones stay on a single line.
func TestWrapping(t *testing.T) {
	cases := []struct{ name, in, wantSub string }{
		{"pipe", `$o := [1, 2, 3, 4, 5, 6, 7, 8] |> map(|$e| $e * 2) |> filter(|$e| $e > 4) |> reduce(0, |$a, $e| $a + $e)`, "|>\n"},
		{"array", `$xs := ["alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet", "kilo"]`, "[\n"},
		{"call", `say("one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve", "x")`, "(\n"},
		{"map", `$m := {"alpha": 1, "bravo": 2, "charlie": 3, "delta": 4, "echo": 5, "foxtrot": 6, "golf": 7, "hotel": 8, "i": 9}`, "{\n"},
	}
	for _, c := range cases {
		got := mustFormat(t, c.in)
		if !strings.Contains(got, c.wantSub) {
			t.Errorf("%s: expected wrapping (%q) in:\n%s", c.name, c.wantSub, got)
		}
	}
	if s := mustFormat(t, `say(1, 2, 3)`); strings.Contains(s, "\n\t") {
		t.Errorf("short call should not wrap:\n%s", s)
	}
}

// TestCommentPlacement locks in the fixes from the final review: comments must stay with
// the construct they document — inside multi-line collections/calls (not evicted to the
// next statement), inside a lambda body (which then keeps its block form), on the correct
// statement of a ;-joined line, and on a then-block's } rather than leaking into else.
func TestCommentPlacement(t *testing.T) {
	cases := []struct{ name, in, wantSub string }{
		{"call-arg-interior", "foo(\n\t1,  # c\n\t2\n)", "1,  # c"},
		{"array-interior", "$xs := [\n\t1,  # one\n\t2\n]", "1,  # one"},
		{"map-interior", "$m := {\n\t\"a\": 1,  # alpha\n\t\"b\": 2\n}", "\"a\": 1,  # alpha"},
		{"lambda-body-comment", "$f := |$x| {\n\t# inner\n\t$x + 1\n}", "# inner"},
		{"semicolon-trailing-last", "a(); b(); c()  # x", "c()  # x"},
		{"else-brace-comment", "if x {\n\ta()\n}  # done\nelse {\n\tb()\n}", "}  # done"},
	}
	for _, c := range cases {
		got := mustFormat(t, c.in)
		if !strings.Contains(got, c.wantSub) {
			t.Errorf("%s: expected %q in:\n%s", c.name, c.wantSub, got)
		}
		if again := mustFormat(t, got); again != got {
			t.Errorf("%s: not idempotent:\n--- once ---\n%s--- twice ---\n%s", c.name, got, again)
		}
	}
	// the lambda with an interior comment must NOT collapse to |x| expr
	if got := mustFormat(t, "$f := |$x| {\n\t# inner\n\t$x + 1\n}"); !strings.Contains(got, "|$x| {") {
		t.Errorf("lambda with interior comment should keep block form:\n%s", got)
	}
	// the ;-joined comment must NOT land on the first statement
	if got := mustFormat(t, "a(); b(); c()  # x"); strings.Contains(got, "a()  # x") {
		t.Errorf("trailing comment wrongly attached to first statement:\n%s", got)
	}
}

// TestComments verifies comments survive formatting (the drop-guard) and land in
// sensible places: leading, same-line trailing, inside blocks, before a closing brace,
// floating, and at EOF — and that the result is idempotent.
func TestComments(t *testing.T) {
	src := "# file header\n" +
		"$x := 1  # trailing decl\n" +
		"\n" +
		"fn .f($a) {\n" +
		"\t# leading inside\n" +
		"\tsay($a)  # trailing inside\n" +
		"\t# before close\n" +
		"}\n" +
		"\n" +
		"# floating block\n" +
		"say($x)  # final\n" +
		"# eof\n"

	out := mustFormat(t, src) // Format's drop-guard already fails if any comment is lost
	for _, c := range []string{
		"# file header", "# trailing decl", "# leading inside",
		"# trailing inside", "# before close", "# floating block", "# final", "# eof",
	} {
		if !strings.Contains(out, c) {
			t.Errorf("comment %q missing from:\n%s", c, out)
		}
	}
	if again := mustFormat(t, out); again != out {
		t.Errorf("comments not idempotent:\n--- once ---\n%s--- twice ---\n%s", out, again)
	}
	// trailing comments must stay on the same line as their code
	if !strings.Contains(out, "$x := 1  # trailing decl") {
		t.Errorf("trailing decl comment not attached:\n%s", out)
	}
	if !strings.Contains(out, "say($a)  # trailing inside") {
		t.Errorf("trailing in-block comment not attached:\n%s", out)
	}
	t.Logf("formatted:\n%s", out)
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
		{"map-lambda-body", `xs |> map(|$c| ({a: $c}))`, "xs |> map(|$c| ({a: $c}))\n"},
		{"map-statement", `({a: 1})`, "({a: 1})\n"},
	}
	for _, c := range cases {
		if got := mustFormat(t, c.in); got != c.want {
			t.Errorf("%s: format(%q) =\n%q\nwant\n%q", c.name, c.in, got, c.want)
		}
	}
}
