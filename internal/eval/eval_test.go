package eval

import (
	"bytes"
	"strings"
	"testing"

	"github.com/anafalanx/lang3/internal/parser"
)

// run executes src and returns everything say printed.
func run(t *testing.T, src string) string {
	t.Helper()
	p := parser.New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse errors for %q: %v", src, errs)
	}
	var buf bytes.Buffer
	old := stdout
	stdout = &buf
	defer func() { stdout = old }()
	if err := RunProgram(prog, NewEnv()); err != nil {
		t.Fatalf("runtime error for %q: %v", src, err)
	}
	return buf.String()
}

// TestOutput drives whole programs and compares their say output. It locks in
// the collections slice: literals, indexing, autovivification, compound
// assignment, for-in over every iterable, the builtins, and the error model.
func TestOutput(t *testing.T) {
	cases := []struct{ name, src, want string }{
		// scalars & arithmetic (regression)
		{"arith-precedence", `say(1 + 2 * 3)`, "7\n"},
		{"comparison", `say(2 < 3)`, "true\n"},

		// arrays
		{"array-display", `say([1, 2, 3])`, "[1, 2, 3]\n"},
		{"array-index", `say([10, 20, 30][1])`, "20\n"},
		{"array-negative", `say([10, 20, 30][-1])`, "30\n"},
		{"array-oob-err", `say([1, 2][5])`, "error: index 5 out of range (len 2)\n"},
		{"array-set", `$a := [1, 2, 3]; $a[0] = 9; say($a)`, "[9, 2, 3]\n"},
		{"array-push-grow", `$a := [1]; $a[1] = 2; say($a)`, "[1, 2]\n"},
		{"array-alias", `$a := [1]; $b := $a; $a[0] = 9; say($b)`, "[9]\n"},
		{"array-struct-eq", `say([1, 2] == [1, 2])`, "true\n"},
		{"array-struct-neq", `say([1, 2] == [1, 2, 3])`, "false\n"},

		// maps
		{"map-display", `say({"a": 1, "b": 2})`, "{a: 1, b: 2}\n"},
		{"map-index", `$m := {"x": 10}; say($m["x"])`, "10\n"},
		{"map-field", `$m := {"x": 10}; say($m.x)`, "10\n"},
		{"map-miss-undef", `$m := {}; say($m["z"])`, "nil\n"},
		{"map-miss-defor", `$m := {}; say($m["z"] // -1)`, "-1\n"},
		{"map-set", `$m := {}; $m["k"] = 5; say($m)`, "{k: 5}\n"},
		{"map-insertion-order", `$m := {}; $m["b"] = 1; $m["a"] = 2; say(keys($m))`, "[b, a]\n"},
		{"map-int-float-key-collide", `$m := {}; $m[1] = "a"; $m[1.0] = "b"; say($m)`, "{1: b}\n"},
		{"map-struct-eq-order-indep", `say({"a": 1, "b": 2} == {"b": 2, "a": 1})`, "true\n"},

		// autovivification
		{"autoviv-nested", `$s := {}; $s.by_ip["ip"] += 1; $s.by_ip["ip"] += 1; say($s)`, "{by_ip: {ip: 2}}\n"},
		{"autoviv-array-in-map", `$m := {}; $m.xs[0] = 7; say($m)`, "{xs: [7]}\n"},

		// compound assignment
		{"compound-scalar", `$x := 5; $x += 3; $x *= 2; say($x)`, "16\n"},
		{"compound-undef-seed", `$h := {}; $h["c"] += 1; say($h["c"])`, "1\n"},

		// ranges
		{"range-display", `say(1..5)`, "1..5\n"},
		{"range-len", `say(len(1..10))`, "10\n"},

		// for-in
		{"for-array", `for $x in [1, 2, 3] { say($x) }`, "1\n2\n3\n"},
		{"for-array-2var", `for $i, $x in [9, 8] { say($i) }`, "0\n1\n"},
		{"for-range", `for $n in 1..3 { say($n) }`, "1\n2\n3\n"},
		{"for-range-empty", `for $n in 5..1 { say($n) }`, ""},
		{"for-map-values", `$m := {"a": 1, "b": 2}; for $v in $m { say($v) }`, "1\n2\n"},
		{"for-map-2var", `$m := {"a": 1}; for $k, $v in $m { say($k); say($v) }`, "a\n1\n"},
		{"for-string-rune", `for $c in "héi" { say($c) }`, "h\né\ni\n"},
		{"for-postfix", `say($_) for 1..3`, "1\n2\n3\n"},
		{"for-accumulate", `$s := 0; for $n in 1..5 { $s += $n }; say($s)`, "15\n"},
		{"for-snapshot", `$a := [1, 2, 3]; for $x in $a { push($a, 0) }; say(len($a))`, "6\n"},

		// builtins
		{"len-array", `say(len([1, 2, 3]))`, "3\n"},
		{"len-string-runes", `say(len("héi"))`, "3\n"},
		{"push-pop", `$a := [1, 2]; push($a, 3); say(pop($a)); say($a)`, "3\n[1, 2]\n"},
		{"keys-values-pairs", `$m := {"a": 1, "b": 2}; say(keys($m)); say(values($m)); say(pairs($m))`, "[a, b]\n[1, 2]\n[[a, 1], [b, 2]]\n"},
		{"has", `$m := {"a": 1}; say(has($m, "a")); say(has($m, "z"))`, "true\nfalse\n"},
		{"delete", `$m := {"a": 1, "b": 2}; delete($m, "a"); say($m)`, "{b: 2}\n"},
		{"chars", `say(chars("abc"))`, "[a, b, c]\n"},
		{"contains-array", `say(contains([1, 2, 3], 2))`, "true\n"},
		{"contains-string", `say(contains("hello", "ell"))`, "true\n"},

		// error model (regression)
		{"recover-default", `say(int("x") // -1)`, "-1\n"},
		{"defor-on-error", `say(int("x") // 0)`, "0\n"},

		// const interior mutability
		{"const-interior", `$c ::= [1, 2, 3]; $c[0] = 9; say($c)`, "[9, 2, 3]\n"},

		// review-confirmed regressions
		{"range-len-saturate", `say(len(0..9223372036854775807))`, "9223372036854775807\n"},
		{"range-huge-truthy", `if 0..9223372036854775807 { say("y") } else { say("n") }`, "y\n"},
		{"for-range-at-maxint", `for $n in 9223372036854775805..9223372036854775807 { say($n) }`, "9223372036854775805\n9223372036854775806\n9223372036854775807\n"},
		{"for-map-snapshot-delete", `$m := {"a": 1, "b": 2, "c": 3}; for $k, $v in $m { say($k); delete($m, "b") }`, "a\nb\nc\n"},
		{"for-array-snapshot-overwrite", `$a := [1, 2, 3]; for $x in $a { if $x == 1 { $a[2] = 99 }; say($x) }`, "1\n2\n3\n"},
		{"keys-type-error-catchable", `say(keys(5) // "caught")`, "caught\n"},

		// bare-identifier map keys are their name as a string (== quoted keys)
		{"map-bare-ident-key", `$m := {a: 1, b: 2}; say($m["a"]); say($m.b)`, "1\n2\n"},
		{"map-bare-vs-quoted-eq", `say({foo: 1} == {"foo": 1})`, "true\n"},
		{"map-dollar-key-evaluates", `$k := "dyn"; say({$k: 9}["dyn"])`, "9\n"},

		// strings
		{"split-sep", `say(split("a,b,c", ","))`, "[a, b, c]\n"},
		{"split-ws", `say(split("a  b   c"))`, "[a, b, c]\n"},
		{"join-string", `say(join(["a", "b", "c"], "-"))`, "a-b-c\n"},
		{"replace", `say(replace("o_o", "o", "0"))`, "0_0\n"},
		{"trim", `say(trim("  hi  "))`, "hi\n"},
		{"upper-lower", `say(upper("aB"), lower("aB"))`, "AB ab\n"},
		{"starts-ends", `say(starts_with("hello", "he"), ends_with("hello", "lo"))`, "true true\n"},
		{"format", `say(format("{} and {}", 1, "two"))`, "1 and two\n"},
		{"format-literal-braces", `say(format("{{x}}={}", 5))`, "{x}=5\n"},
		{"lines-count", `say(len(lines("a\nb\nc")))`, "3\n"},
		{"repeat-huge-caught", `say(repeat("a", 9999999999999999) // "toobig")`, "toobig\n"},
		{"pipeline-split-len", `say("a,b,c,d" |> split(",") |> len())`, "4\n"},

		// string-literal escapes: known ones process, unknown ones keep the backslash
		{"escape-known", `say("a\tb")`, "a\tb\n"},
		{"escape-unknown-kept", `say("\d\w")`, "\\d\\w\n"},
		{"escape-windows-path", `say("C:\Users\x")`, "C:\\Users\\x\n"},

		// regex (RE2)
		{"regex-matches", `say(matches("foo123", "\d+"))`, "true\n"},
		{"regex-no-match", `say(matches("foo", "\d+"))`, "false\n"},
		{"regex-match-caps", `say(match("a1-b2", "(\w+)-(\w+)"))`, "[a1-b2, a1, b2]\n"},
		{"regex-match-nil", `say(match("xyz", "\d+"))`, "nil\n"},
		{"regex-find-all", `say(find_all("a1b2c3", "\d"))`, "[1, 2, 3]\n"},
		{"regex-gsub", `say(gsub("a1b2", "\d", "#"))`, "a#b#\n"},
		{"regex-gsub-backref", `say(gsub("John Smith", "(\w+) (\w+)", "$2 $1"))`, "Smith John\n"},
		{"regex-bad-pattern-caught", `say(matches("x", "(") // "bad")`, "bad\n"},

		// lambdas
		{"lambda-call", `$f := |$x| $x * 2; say($f(21))`, "42\n"},
		{"lambda-closure", `$n := 10; say((|$x| $x + $n)(5))`, "15\n"},
		{"lambda-multi", `say((|$a, $b| $a + $b)(3, 4))`, "7\n"},
		{"lambda-zero", `say((|| 99)())`, "99\n"},
		{"lambda-block", `$h := |$x| { $y := $x + 1; $y * 2 }; say($h(4))`, "10\n"},

		// higher-order functions
		{"hof-map", `say([1,2,3] |> map(|$x| $x * 10))`, "[10, 20, 30]\n"},
		{"hof-map-index", `say(["a","b"] |> map(|$x, $i| format("{}:{}", $i, $x)))`, "[0:a, 1:b]\n"},
		{"hof-filter", `say([1,2,3,4] |> filter(|$x| $x > 2))`, "[3, 4]\n"},
		{"hof-reject", `say([1,2,3,4] |> reject(|$x| $x > 2))`, "[1, 2]\n"},
		{"hof-find", `say([1,2,3] |> find(|$x| $x > 1))`, "2\n"},
		{"hof-find-miss", `say([1,2,3] |> find(|$x| $x > 9) // -1)`, "-1\n"},
		{"hof-any-all", `say([1,2,3] |> any(|$x| $x > 2), [1,2,3] |> all(|$x| $x > 0))`, "true true\n"},
		{"hof-count", `say([1,2,3,4] |> count(|$x| $x % 2 == 0))`, "2\n"},
		{"hof-reduce", `say([1,2,3,4] |> reduce(0, |$a, $x| $a + $x))`, "10\n"},
		{"hof-flatmap", `say([1,2,3] |> flat_map(|$x| [$x, $x * 10]))`, "[1, 10, 2, 20, 3, 30]\n"},
		{"hof-each-chains", `$xs := [1,2,3]; say($xs |> each(|$x| $x) |> map(|$x| $x + 1))`, "[2, 3, 4]\n"},
		{"arr-take-drop", `say(take([1,2,3,4,5], 2), drop([1,2,3,4,5], 2))`, "[1, 2] [3, 4, 5]\n"},
		{"arr-uniq", `say(uniq([1,2,2,3,1,3]))`, "[1, 2, 3]\n"},
		{"hof-error-propagates", `say([1,2,3] |> map(|$x| int("x")?) // "caught")`, "caught\n"},
		{"hof-nonfn-callback", `say(map([1,2,3], 5) // "notfn")`, "notfn\n"},
		{"hof-empty", `say([] |> map(|$x| $x), [] |> any(|$x| true), [] |> all(|$x| false))`, "[] false true\n"},
		{"hof-block-multiline", `[1, 2] |> each(|$x| {
			$y := $x * 10
			say($y)
		})`, "10\n20\n"},

		// boolean and / or / not (short-circuit, value-returning)
		{"bool-and", `say(2 > 1 and 3 > 2)`, "true\n"},
		{"bool-and-false", `say(2 > 1 and 3 < 2)`, "false\n"},
		{"bool-or", `say(1 > 2 or 3 > 2)`, "true\n"},
		{"bool-not", `say(not (1 > 2))`, "true\n"},
		{"bool-or-value", `say(0 or 5)`, "5\n"},
		{"bool-and-value", `say(7 and 9)`, "9\n"},
		{"bool-short-circuit", `say(false and (1 / 0))`, "false\n"},
		{"bool-precedence", `say(1 == 1 and 2 == 3 or 4 == 4)`, "true\n"},

		// pmap (parallel map): same contract as map, input-ordered results
		{"pmap-basic", `say([1, 2, 3, 4] |> pmap(|$x| $x * 10))`, "[10, 20, 30, 40]\n"},
		{"pmap-index", `say([10, 20] |> pmap(|$x, $i| $i))`, "[0, 1]\n"},
		{"pmap-empty", `say([] |> pmap(|$x| $x))`, "[]\n"},
		{"pmap-error-recover", `say([1, 2, 3] |> pmap(|$x| int("x")?) // "caught")`, "caught\n"},
		{"pmap-ordered-reduce", `say([1, 2, 3, 4, 5, 6, 7, 8] |> pmap(|$x| $x + 1) |> reduce(0, |$a, $b| $a + $b))`, "44\n"},

		// concurrency: spawn / await / channels
		{"spawn-await", `$t := spawn(|| 6 * 7); say(await($t))`, "42\n"},
		{"spawn-args", `$t := spawn(|$a, $b| $a + $b, 3, 4); say(await($t))`, "7\n"},
		{"chan-send-recv", `$c := chan(1); send($c, "hi"); say(recv($c))`, "hi\n"},
		{"recv2-closed", `$c := chan(); close($c); say(recv2($c))`, "[nil, false]\n"},
		{"spawn-error-await", `$t := spawn(|| fail("boom")); say(await($t) // "caught")`, "caught\n"},
		{"send-on-closed", `$c := chan(1); close($c); say(send($c, 1) // "closed")`, "closed\n"},
		{"send-copies", `$a := [1, 2]; $c := chan(1); send($c, $a); push($a, 99); say(recv($c))`, "[1, 2]\n"},
		{"fan-out-await", `say([1,2,3] |> map(|$x| spawn(|$n| $n * 10, $x)) |> map(|$t| await($t)))`, "[10, 20, 30]\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := run(t, c.src); got != c.want {
				t.Errorf("src %q:\n got %q\nwant %q", c.src, got, c.want)
			}
		})
	}
}

// runErr executes src expecting a runtime (Go) error and returns its message.
func runErr(t *testing.T, src string) string {
	t.Helper()
	p := parser.New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		return "parse: " + strings.Join(errs, "; ")
	}
	old := stdout
	stdout = &bytes.Buffer{}
	defer func() { stdout = old }()
	err := RunProgram(prog, NewEnv())
	if err == nil {
		t.Fatalf("expected an error for %q, got none", src)
	}
	return err.Error()
}

// TestRuntimeErrors covers the aborting (non-catchable) failures: programming
// mistakes that should stop the program rather than yield an Err value.
func TestRuntimeErrors(t *testing.T) {
	cases := []struct{ name, src, wantSubstr string }{
		{"const-rebind", `$c ::= 1; $c = 2`, "constant"},
		{"gap-write", `$a := [1]; $a[5] = 9`, "past end"},
		{"non-iterable", `for $x in 42 { say($x) }`, "cannot iterate"},
		{"assign-undeclared", `$x = 5`, "undefined"},
		{"keys-arity-aborts", `keys()`, "expects 1 argument"},
		{"values-arity-aborts", `values({}, {})`, "expects 1 argument"},
		{"pairs-arity-aborts", `pairs()`, "expects 1 argument"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := runErr(t, c.src); !strings.Contains(got, c.wantSubstr) {
				t.Errorf("src %q: error %q does not contain %q", c.src, got, c.wantSubstr)
			}
		})
	}
}
