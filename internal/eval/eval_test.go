package eval

import (
	"bytes"
	"strings"
	"testing"

	"github.com/anafalanx/drang/internal/parser"
	"github.com/anafalanx/drang/internal/value"
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

// TestPipeFaithful covers the faithful Pipe node (|> is no longer desugared at parse
// time). Behavior must stay identical to f(lhs, args...) across callee shapes, chains,
// and evaluation order — on both backends (the VM compiles Pipe to the same bytecode).
func TestPipeFaithful(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"bare-pipe-builtin", `say([1, 2, 3] |> len)`, "3\n"},
		{"method-callee-args", `fn .add($a, $b) { $a + $b }
say(3 |> .add(4))`, "7\n"},
		{"var-callee", `$f := |$x| $x * 2
say(3 |> $f())`, "6\n"},
		{"chain", `say([1, 2, 3] |> map(|$x| $x * 2) |> reduce(0, |$a, $x| $a + $x))`, "12\n"},
		{"lhs-evaluated-first-once", `$n := 0
fn .bump() { $n = $n + 1; $n }
fn .id($x) { $x }
say(.bump() |> .id())`, "1\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(t, tc.src); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestInterpFaithful covers the faithful Interp node (interpolation is no longer folded
// into a ~-chain at parse time). Values must match the old behavior, and the capture
// analysis must still see vars inside interpolations (the closure case is the regression
// trap) — on both backends (the VM compiles Interp to the same ~-fold bytecode).
func TestInterpFaithful(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"literal-var-expr", `$x := 5
say($"v=$x ${$x + 1}")`, "v=5 6\n"},
		{"leading-interp-forces-string", `$x := 7
say($"$x!")`, "7!\n"},
		{"escaped-dollar", `$x := 1
say($"\$x is $x")`, "$x is 1\n"},
		{"qq-interp", `$n := "bob"
say($qq{hi $n})`, "hi bob\n"},
		{"closure-captures-interp-var", `fn .make($x) { || $"got $x" }
$f := .make(42)
say($f())`, "got 42\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(t, tc.src); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestStdlibWalls covers the first stdlib batch: the conversion family (str/float/bool/
// type), the math additions (sqrt/pow/log/div), and index_of — values and catchable
// error cases.
func TestStdlibWalls(t *testing.T) {
	cases := []struct{ name, src, want string }{
		// conversions
		{"str", `say(str(5), str(1.5), str(true), str([1, 2, 3]))`, "5 1.5 true [1, 2, 3]\n"},
		{"float-parse", `say(float("  2.5  ") + 1)`, "3.5\n"},
		{"float-widen", `say(float(3))`, "3\n"},
		{"float-bad", `say(float("x") // "bad")`, "bad\n"},
		{"bool", `say(bool(0), bool(""), bool([]), bool(5), bool("x"))`, "false false false true true\n"},
		{"type", `say(type(5), type(1.5), type("a"), type(true), type([1]), type({"a": 1}), type(1..2))`, "int float string bool array map range\n"},
		// math
		{"sqrt", `say(sqrt(9), sqrt(2))`, "3 1.4142135623730951\n"},
		{"sqrt-neg", `say(sqrt(-1) // "neg")`, "neg\n"},
		// NaN must not leak silently (NaN sourced from inf - inf); float() stays finite
		{"sqrt-nan", `say(sqrt(pow(0.0, -1) - pow(0.0, -1)) // "nan")`, "nan\n"},
		{"log-nan", `say(log(pow(0.0, -1) - pow(0.0, -1)) // "nan")`, "nan\n"},
		{"float-inf", `say(float("inf") // "noninf")`, "noninf\n"},
		{"float-nan", `say(float("nan") // "nonnan")`, "nonnan\n"},
		{"pow-int", `say(pow(2, 10))`, "1024\n"},
		{"pow-float", `say(pow(9, 0.5))`, "3\n"},
		{"pow-overflow", `say(pow(2, 100) // "of")`, "of\n"},
		{"log", `say(round(log(8, 2)), round(log(1000, 10)))`, "3 3\n"},
		{"div", `say(div(17, 5), div(-17, 5), div(17, -5), div(-17, -5))`, "3 -3 -3 3\n"},
		{"div-zero", `say(div(1, 0) // "dz")`, "dz\n"},
		// index_of (rune-indexed)
		{"index-of", `say(index_of("hello", "ll"), index_of("hello", "z"))`, "2 -1\n"},
		{"index-of-rune", `say(index_of("héllo", "llo"))`, "2\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(t, tc.src); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestStdlibWalls2 covers the second wall batch: UTC time (an opts flag), os/arch/home,
// and tempfile/tempdir + write_file append.
func TestStdlibWalls2(t *testing.T) {
	// epoch 0 is 1970-01-01 00:00:00 UTC regardless of the machine's local zone
	if got := run(t, `say(strftime(0, "%Y-%m-%d %H:%M:%S", {utc: true}))`); got != "1970-01-01 00:00:00\n" {
		t.Errorf("strftime utc = %q", got)
	}
	if got := run(t, "$p := date_parts(0, {utc: true})\nsay($p.year, $p.month, $p.day, $p.hour)"); got != "1970 1 1 0\n" {
		t.Errorf("date_parts utc = %q", got)
	}
	// parse_time + strftime round-trip in UTC (zone-independent)
	rt := "$e := parse_time(\"2021-07-04 12:30:00\", \"%Y-%m-%d %H:%M:%S\", {utc: true})\nsay(strftime($e, \"%Y-%m-%d %H:%M:%S\", {utc: true}))"
	if got := run(t, rt); got != "2021-07-04 12:30:00\n" {
		t.Errorf("parse_time/strftime utc round-trip = %q", got)
	}
	// os/arch/home are non-empty
	if got := run(t, `say(len(os()) > 0, len(arch()) > 0, len(home()) > 0)`); got != "true true true\n" {
		t.Errorf("os/arch/home = %q", got)
	}
	// tempfile + write + append + read + cleanup
	tf := "$f := tempfile()\nwrite_file($f, \"a\")\nwrite_file($f, \"b\", {append: true})\n$r := read_file($f) // \"ERR\"\nrm($f)\nsay($r)"
	if got := run(t, tf); got != "ab\n" {
		t.Errorf("tempfile + append = %q", got)
	}
	// tempdir creates a real directory
	td := "$d := tempdir()\n$ok := is_dir($d)\nrm($d)\nsay($ok)"
	if got := run(t, td); got != "true\n" {
		t.Errorf("tempdir = %q", got)
	}
}

// TestFirstClassBuiltins verifies a bare builtin name is a first-class function value:
// it can be passed to HOFs (closing the long-standing map($xs, basename) wart), bound to
// a variable, and called — while a user binding of the same name still shadows it.
func TestFirstClassBuiltins(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"map-basename", `say(["/a/b.txt", "/c/d.md"] |> map(basename))`, "[b.txt, d.md]\n"},
		{"map-len", `say(["x", "yy"] |> map(len))`, "[1, 2]\n"},
		{"filter-bool", `say([0, 1, "", "x"] |> filter(bool))`, "[1, x]\n"},
		{"reduce-max", `say([3, 1, 2] |> reduce(0, max))`, "3\n"},
		{"bare-value-called", "$f := upper\nsay($f(\"hi\"))", "HI\n"},
		{"type-is-function", `say(type(len))`, "function\n"},
		{"passed-to-fn", "fn .ap($f, $x) { $f($x) }\nsay(.ap(upper, \"hi\"))", "HI\n"},
		{"user-binding-shadows", "$len := 99\nsay($len)", "99\n"},
		{"display", `say(str(upper))`, "<builtin upper>\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(t, tc.src); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestFirstClassBuiltinPanicRecovered locks in the review fix: a panicking builtin reached
// via a first-class value must yield a catchable Err (not crash the process), the same
// guarantee by-name dispatch gives — both route through safeBuiltin.
func TestFirstClassBuiltinPanicRecovered(t *testing.T) {
	builtins["zzpanic"] = func(args []value.Value) (value.Value, error) { panic("boom") }
	defer delete(builtins, "zzpanic")
	if got := run(t, "$f := zzpanic\nsay($f(1) // \"caught\")"); got != "caught\n" {
		t.Errorf("direct value call: got %q, want \"caught\"", got)
	}
	if got := run(t, `say(map([1], zzpanic) // "caught")`); got != "caught\n" {
		t.Errorf("via HOF: got %q, want \"caught\"", got)
	}
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
		{"format-printf-habit-errs", `say(is_err(format("%s", 42)))`, "true\n"},
		{"format-too-few-args-errs", `say(is_err(format("{} {}", "a")))`, "true\n"},
		{"format-mismatch-recover", `say(format("%s", 42) // "bad")`, "bad\n"},
		{"fmt-float-prec", `say(format("{:.2f}", 3.14159))`, "3.14\n"},
		{"fmt-align-right", `say(format("[{:>5}]", "x"))`, "[    x]\n"},
		{"fmt-align-left", `say(format("[{:<5}]", "x"))`, "[x    ]\n"},
		{"fmt-center-fill", `say(format("[{:*^7}]", "hi"))`, "[**hi***]\n"},
		{"fmt-zero-pad-signed", `say(format("{:08.2f}", -3.1))`, "-0003.10\n"},
		{"fmt-hex-alt", `say(format("{:#x}", 255))`, "0xff\n"},
		{"fmt-int-default-right", `say(format("[{:5}]", 42))`, "[   42]\n"},
		{"fmt-plus-sign", `say(format("{:+d}", 7))`, "+7\n"},
		{"fmt-percent", `say(format("{:.1%}", 0.1234))`, "12.3%\n"},
		{"fmt-str-truncate", `say(format("{:.3}", "hello"))`, "hel\n"},
		{"fmt-literal-then-spec", `say(format("{{x}}={:03d}", 7))`, "{x}=007\n"},
		{"fmt-hex-zero-alt", `say(format("{:#06x}", 255))`, "0x00ff\n"},
		{"fmt-octal-alt", `say(format("{:#o}", 8))`, "0o10\n"},
		{"fmt-zero-under-align", `say(format("{:>05}", 7))`, "00007\n"},
		{"dt-roundtrip", `$e := parse_time("2026-06-27 13:45:09", "%Y-%m-%d %H:%M:%S"); say(strftime($e, "%Y-%m-%d %H:%M:%S"))`, "2026-06-27 13:45:09\n"},
		{"dt-parts", `$p := date_parts(parse_time("2026-06-27 13:45:09", "%Y-%m-%d %H:%M:%S")); say(format("{}/{}/{} {}:{}:{}", $p.year, $p.month, $p.day, $p.hour, $p.minute, $p.second))`, "2026/6/27 13:45:9\n"},
		{"dt-arith", `$e := parse_time("2026-06-27 00:00:00", "%Y-%m-%d %H:%M:%S"); say(strftime($e + 3600, "%H:%M"))`, "01:00\n"},
		{"dt-weekday", `say(date_parts(parse_time("2026-06-27", "%Y-%m-%d")).weekday)`, "6\n"},
		{"dt-now-positive", `say(now() > 1700000000.0)`, "true\n"},
		{"dt-sleep-zero", `sleep(0.0); say("ok")`, "ok\n"},
		{"sha256", `say(sha256("abc"))`, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad\n"},
		{"md5", `say(md5("abc"))`, "900150983cd24fb0d6963f7d28e17f72\n"},
		{"base64-roundtrip", `say(from_base64(to_base64("hello, world")))`, "hello, world\n"},
		{"base64-encode", `say(to_base64("hi"))`, "aGk=\n"},
		{"hex-roundtrip", `say(from_hex(to_hex("AB")))`, "AB\n"},
		{"url-encode", `say(url_encode("a b&c=d"))`, "a+b%26c%3Dd\n"},
		{"rand-range", `$r := rand(); say($r >= 0.0 and $r < 1.0)`, "true\n"},
		{"rand-int-range", `$x := rand_int(5, 8); say($x >= 5 and $x < 8)`, "true\n"},
		{"rand-int-wide", `$x := rand_int(-1, 9223372036854775807); say($x >= -1)`, "true\n"},
		{"shuffle-preserves-len", `say(len(shuffle([1,2,3,4,5])))`, "5\n"},
		{"arr-slice-inclusive", `say([10, 20, 30, 40, 50][1..3])`, "[20, 30, 40]\n"},
		{"arr-slice-neg", `say([10, 20, 30][-2..-1])`, "[20, 30]\n"},
		{"arr-slice-clamp", `say([1, 2, 3][1..99])`, "[2, 3]\n"},
		{"arr-slice-empty", `say([1, 2, 3][2..0])`, "[]\n"},
		{"str-index", `say("hello"[1])`, "e\n"},
		{"str-index-neg", `say("hello"[-1])`, "o\n"},
		{"str-slice", `say("hello"[1..3])`, "ell\n"},
		{"str-slice-unicode", `say("héllo"[0..1])`, "hé\n"},
		{"default-param-used", `fn .f($a, $b = 8080) { $a + $b }; say(.f(1))`, "8081\n"},
		{"default-param-override", `fn .f($a, $b = 8080) { $a + $b }; say(.f(1, 9090))`, "9091\n"},
		{"default-ref-earlier", `fn .g($a, $b = $a + 1) { $b }; say(.g(5))`, "6\n"},
		{"default-lambda", `$f := |$x, $y = 10| $x + $y; say($f(5))`, "15\n"},
		{"default-two", `fn .f($a, $b = 2, $c = 3) { $a + $b + $c }; say(.f(1, 20))`, "24\n"},
		{"default-vm-path", `fn .add($a, $b = 10) { $a + $b }; fn .use() { .add(5) }; say(.use())`, "15\n"},
		{"default-lazy", `fn .boom() { fail("ran") }; fn .f($x = .boom()) { 1 }; say(.f(9))`, "1\n"},
		{"default-lambda-captures-enclosing", `fn .make($b) { |$x, $y = $b| $x + $y } $f := .make(9); say($f(1))`, "10\n"},
		{"default-survives-spawn", `fn .work($a, $b = 5) { $a + $b } $t := spawn(.work, 3); say(await($t))`, "8\n"},
		{"default-trailing-comma", `fn .f($a, $b = 1,) { $a + $b } say(.f(5))`, "6\n"},
		{"uuid-format", `say(matches(uuid(), qr/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/))`, "true\n"},
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
		{"recv_ok-closed", `$c := chan(); close($c); say(recv_ok($c))`, "[nil, false]\n"},
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
		{"const-interior-frozen", `$c ::= [1, 2, 3]; $c[0] = 9`, "frozen"},
		{"const-map-frozen", `$m ::= {"a": 1}; $m["b"] = 2`, "frozen"},
		{"const-push-frozen", `$c ::= [1]; push($c, 2)?`, "frozen"},
		{"fmt-bad-type", `format("{:q}", 1)?`, "unknown format type"},
		{"fmt-type-mismatch", `format("{:d}", "x")?`, "needs an int"},
		{"fmt-bad-spec", `format("{:.}", 1)?`, "precision"},
		{"fmt-huge-prec", `format("{:.99999999f}", 3.0)?`, "precision too large"},
		{"fmt-huge-width", `format("{:9999999}", 5)?`, "width too large"},
		{"parse-time-bad", `parse_time("nope", "%Y-%m-%d")?`, "parse_time"},
		{"parse-time-bad-code", `parse_time("x", "%Q")?`, "unsupported format code"},
		{"from-base64-bad", `from_base64("!!!")?`, "from_base64"},
		{"from-hex-bad", `from_hex("zz")?`, "from_hex"},
		{"str-index-oob", `"hi"[10]?`, "out of range"},
		{"arr-bad-index-type", `[1, 2, 3]["x"]?`, "int or range"},
		{"default-too-few", `fn .h($a, $b) { 1 }; .h(1)`, "expects 2"},
		{"default-too-many", `fn .f($a, $b = 1) { 1 }; .f(1, 2, 3)`, "1 to 2"},
		{"rand-int-nonpositive", `rand_int(0)?`, "must be positive"},
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
