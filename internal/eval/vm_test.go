package eval

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/anafalanx/drang/internal/ast"
	"github.com/anafalanx/drang/internal/parser"
)

// vmSubset holds programs that the v1 compiler is expected to compile fully (no
// walker fallback). They double as the parity corpus: the VM and the tree-walker
// must produce byte-identical output and agree on error-vs-success for each.
var vmSubset = []string{
	`say(1 + 2 * 3 - 4)`,
	`say((1 + 2) * 3)`,
	`say(17 % 5, 17 / 5, 18 / 6)`,
	`say("a" ~ "b" ~ "c")`,
	`say(1 ~ "-" ~ 2)`,
	`say(2 < 3, 3 <= 3, 4 > 5, 5 >= 5, 5 == 5, 5 != 6)`,
	`say("ab" < "ac", "z" > "a")`,
	`say(-7, - -3, !true, !false, !0)`,
	`say(true and 1, false and 1, 0 or 9, 7 or 9)`,
	`$x := 5
say($x + 1)
$x = $x * 2
say($x)`,
	`$k ::= 3
say($k * $k)`,
	`$n := 10
$sum := 0
$i := 1
while $i <= $n {
  $sum = $sum + $i
  $i = $i + 1
}
say("sum", $sum)`,
	`$x := 7
if $x > 5 { say("big") } else { say("small") }`,
	`$x := 2
if $x > 5 { say("big") } else { if $x > 1 { say("mid") } else { say("small") } }`,
	`$g := 0
$i := 0
while $i < 3 {
  if $i == 1 { $g = $g + 100 }
  $i = $i + 1
}
say($g)`,
	`say(len("hello"))`,
	`say(len("hi") + len("there"))`,
	// block scoping: an inner declaration must not leak to the outer scope
	`$x := 1
if true {
  $x := 99
  say($x)
}
say($x)`,
	// functions (Ident callee) — these compile fully and run on the VM
	`fn .sq($x) { $x * $x }
say(.sq(6), .sq(7))`,
	`fn .add($a, $b) { $a + $b }
say(.add(3, 4), .add(.add(1, 2), 3))`,
	`fn .fib($n) {
  if $n < 2 { $n } else { .fib($n - 1) + .fib($n - 2) }
}
say(.fib(10), .fib(15))`,
	`fn .classify($n) {
  if $n < 0 { return "neg" }
  if $n == 0 { return "zero" }
  "pos"
}
say(.classify(-5), .classify(0), .classify(7))`,
	// register-mode stress: block shadowing must not leak; loop locals reused
	// across iterations; a free variable (global) read alongside register locals.
	`fn .shadow($x) {
  $y := $x + 1
  if $x > 0 {
    $y := $x * 10
    say("inner", $y)
  }
  say("outer", $y)
}
.shadow(5)`,
	`fn .loopsum($n) {
  $acc := 0
  $i := 1
  while $i <= $n {
    $acc = $acc + $i
    $i = $i + 1
  }
  $acc
}
say(.loopsum(100), .loopsum(0))`,
	`fn .mixed($x) {
  $local := $x * $g
  $local + $x
}
$g := 3
say(.mixed(4), .mixed(10))`,
	`fn .reassign($x) {
  $x = $x + 1
  $x = $x * 2
  $x
}
say(.reassign(5))`,
	// error model: // recovers nil/error, ? passes a real value through
	`say(int("x") // -1, int("42") // -1)`,
	`say(7 // 99, int("42")?)`,
	// error inspection: read an error's details as values
	`say(is_err(int("x")), is_err("ok"), err_code("ok"), err_msg("ok") == "")`,
	// string interpolation: $name, ${expr}, escaped \$, and a Display'd value
	`$who := "drang"
$n := 6
say("hi $who, ${$n * 7} and a literal \$n = $n")`,
	`$xs := [1, 2, 3]
say("list ${$xs} len ${len($xs)}")`,
	// ordering: <=>, sort (natural + comparator), sort_by, min_by/max_by
	`$xs := [5, 3, 8, 1, 3]
say(sort($xs))
say(sort($xs, |$a, $b| $b <=> $a))
say(1 <=> 2, 2 <=> 2, 3 <=> 2)`,
	`$m := [{k: "b", n: 2}, {k: "a", n: 3}, {k: "c", n: 1}]
say(sort_by($m, |$x| $x.n) |> map(|$x| $x.k))
say(min_by($m, |$x| $x.n).k, max_by($m, |$x| $x.n).k)`,
	// loop control: break/next in for + while, nested if scope unwinding (Env mode)
	`$sum := 0
for $i in 1..10 {
  if $i == 7 { break }
  if $i % 2 == 0 { next }
  $sum += $i
}
say($sum)`,
	`$n := 0
$sum := 0
while $n < 100 {
  $n += 1
  if $n > 5 { break }
  if $n == 3 { next }
  $sum += $n
}
say($n, $sum)`,
	// nested loops: break/next bind to the innermost
	`$s := ""
for $i in 1..3 {
  for $j in 1..3 {
    if $j == $i { next }
    $s = $s ~ "$i$j "
  }
}
say($s)`,
	// break/next inside a function body (register mode)
	`fn .classify($xs) {
  $r := ""
  for $x in $xs {
    if $x < 0 { next }
    if $x > 100 { break }
    $r = $r ~ $x ~ ","
  }
  $r
}
say(.classify([5, -3, 20, -1, 200, 7]))`,
	// postfix modifiers on break/next (regression: must route through applyPostfix)
	`$out := ""
for $i in 1..6 {
  next unless $i % 2 == 0
  break if $i > 4
  $out = $out ~ $i ~ ","
}
say($out)`,
	// a bare break/next followed by more statements (regression: newline terminates)
	`$i := 0
while $i < 10 {
  $i = $i + 1
  next
  $i = $i + 100
}
say($i)`,
	// quote operators: qw word list, q raw string, qq interpolated string
	`say(qw(alpha beta gamma))
$w := "W"
say(q(raw $w stays))
say(qq{interp $w and ${2 * 21}})`,
	// heredoc: interpolated body, then normal code resumes after the terminator
	`$who := "Sam"
$msg := <<END
hi $who
the answer is ${6 * 7}
END
say($msg)
say("after")`,
	// regex: qr// literal + re() value, exercised by the builtins on both backends
	`$d := qr/\d+/
say(matches("a1b2", $d), matches("xyz", $d))
say(find_all("a1 b2 c3", $d))
say(match("k=9", qr{(\w+)=(\d+)})[2])
say(gsub("a1b2", re(q(\d)), "#"))
say(qr/x/i == qr/x/i, qr/x/ == qr/x/i)`,
	// ? inside a function: a propagated error becomes the function's result, then
	// // recovers it at the call site
	`fn .doubler($s) {
  $n := int($s)?
  $n * 2
}
say(.doubler("21"), .doubler("x") // "bad")`,
	// collections: literals, index/field reads, negative index, OOB, nesting
	`say([1, 2, 3])`,
	`$a := [10, 20, 30]
say($a[0], $a[2], $a[-1])`,
	`$m := {a: 1, b: 2}
say($m.a, $m.b, $m["a"])`,
	`$m := {x: 10}
say($m.missing // "none", $m["nope"] // "none")`,
	`say([1, 2, 3][9] // "oob")`,
	`say(1..5, {one: 1, two: 2})`,
	`$nested := [[1, 2], [3, 4]]
say($nested[1][0])`,
	`$data := {nums: [10, 20, 30]}
say($data.nums[1])`,
	// functions that index their params (register mode + collections)
	`fn .sum3($xs) { $xs[0] + $xs[1] + $xs[2] }
say(.sum3([10, 20, 30]))`,
	`fn .lookup($m, $k) { $m[$k] // "miss" }
say(.lookup({a: 1}, "a"), .lookup({a: 1}, "z"))`,
	// collection assignment + autovivification (single-level)
	`$a := [1, 2, 3]
$a[0] = 10
$a[-1] = 30
$a[3] = 4
say($a)`,
	`$m := {a: 1}
$m.b = 2
$m["c"] = 3
say($m.a, $m.b, $m.c)`,
	`$counts := {}
$counts["x"] += 1
$counts["x"] += 1
$counts["y"] += 1
say($counts.x, $counts.y)`,
	`$m := {}
$m.deep = "x"
say($m.deep)`,
	`$k := [1, 2]
$k[0] = 9
say($k)`,
	// variable compound assignment
	`$x := 5
$x += 3
$x *= 2
$x -= 1
say($x)`,
	// register-local containers + compound, inside functions
	`fn .build($n) {
  $acc := []
  $i := 0
  while $i < $n {
    $acc[$i] = $i * $i
    $i += 1
  }
  $acc
}
say(.build(4))`,
	`fn .factorial($n) {
  $result := 1
  $i := 2
  while $i <= $n {
    $result *= $i
    $i += 1
  }
  $result
}
say(.factorial(5), .factorial(0))`,
	`fn .tally($m, $k) {
  $m[$k] += 1
  $m
}
$counts := {}
say(.tally(.tally(.tally($counts, "a"), "a"), "b").a)`,
	// for-in over every iterable, one- and two-var
	`$s := 0
for $x in [10, 20, 30] { $s += $x }
say($s)`,
	`for $i, $x in [10, 20, 30] { say($i, $x) }`,
	`$s := 0
for $v in 1..100 { $s += $v }
say($s)`,
	`$m := {a: 1, b: 2, c: 3}
$sum := 0
for $v in $m { $sum += $v }
say($sum)`,
	`$m := {a: 1, b: 2}
for $k, $v in $m { say($k, $v) }`,
	`$out := ""
for $ch in "abc" { $out = $ch ~ $out }
say($out)`,
	// snapshot semantics: mutating the collection mid-loop doesn't change the count
	`$a := [1, 2, 3]
$n := 0
for $x in $a {
  $n += 1
  push($a, 99)
}
say($n, len($a))`,
	// for-in inside functions (register mode), one- and two-var
	`fn .total($xs) {
  $t := 0
  for $x in $xs { $t += $x }
  $t
}
say(.total([1, 2, 3, 4, 5]))`,
	`fn .wordcount($words) {
  $c := {}
  for $w in $words { $c[$w] += 1 }
  $c
}
$wc := .wordcount(["a", "b", "a", "c", "a"])
say($wc.a, $wc.b, $wc.c)`,
	`fn .dotproduct($xs, $ys) {
  $sum := 0
  for $i, $x in $xs { $sum += $x * $ys[$i] }
  $sum
}
say(.dotproduct([1, 2, 3], [4, 5, 6]))`,
	// non-identifier callees: stored, returned, and indexed function values
	`$double := |$x| $x * 2
say($double(21))`,
	`fn .apply($f, $x) { $f($x) }
say(.apply(|$n| $n + 100, 5))`,
	`fn .adder($n) { |$x| $x + $n }
$add5 := .adder(5)
say($add5(10), $add5(20))`,
	`$fns := [|$x| $x + 1, |$x| $x * 2, |$x| $x - 3]
say($fns[0](10), $fns[1](10), $fns[2](10))`,
	// bare identifier as a value (point-free)
	`fn .inc($x) { $x + 1 }
$f := .inc
say($f(41))`,
	// nested slot assignment with autovivification, at depth
	`$m := {}
$m.a = {}
$m.a.b = 42
say($m.a.b)`,
	`$grid := {}
$grid.row = [0, 0, 0]
$grid.row[1] = 9
say($grid.row[1], $grid.row[0])`,
	`$matrix := []
$matrix[0] = []
$matrix[0][0] = 1
$matrix[0][1] = 2
say($matrix[0][0], $matrix[0][1])`,
	`$deep := {}
$deep.a.b.c = "found"
say($deep.a.b.c)`,
	`fn .nest($m, $k1, $k2) {
  $m[$k1][$k2] += 1
  $m
}
$d := {}
say(.nest(.nest($d, "a", "x"), "a", "x").a.x)`,
}

// vmParityExtra exercises functions/closures/lambdas where the top-level may
// partially fall back (e.g. a call through a variable is a non-Ident callee), but
// the called function/lambda itself still runs on the VM. Parity only.
var vmParityExtra = []string{
	`fn .counter() {
  $n := 0
  |$inc| {
    $n = $n + $inc
    $n
  }
}
$c := .counter()
say($c(1), $c(2), $c(3))`,
	`$double := |$x| $x * 2
say($double(21))`,
	`fn .apply($f, $x) { $f($x) }
say(.apply(|$n| $n + 100, 5))`,
	// nested slot assignment ($grid.row[1]) is not compiled yet -> the whole
	// program falls back to the walker; parity must still hold
	`$grid := {}
$grid.row = [0, 0, 0]
$grid.row[1] = 9
say($grid.row[1], $grid.row[0])`,
	// per-iteration loop scope: each closure made in an Env-mode for-in must
	// capture its own iteration's $i (11,12,13 — not 13,13,13)
	`$fns := []
for $i in 1..3 { push($fns, |$x| $x + $i) }
say(map($fns, |$f| $f(10)))`,
}

// vmParityErr exercises error parity: each program errors identically on both
// backends (same error-vs-success outcome).
var vmParityErr = []string{
	`say($undefined)`,
	`$k ::= 1
$k = 2`,
	`$x = 5`, // assign before declare
	`say(1 / 0)`,
	`say(1 % 0)`,
	`say("a" + 1)`,
	`say(int("x")?)`, // top-level ? propagation -> program aborts (both backends)
}

func mustParseProg(tb testing.TB, src string) *ast.Program {
	tb.Helper()
	p := parser.New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		tb.Fatalf("parse %q: %v", src, errs)
	}
	return prog
}

func runBackend(t *testing.T, src string, vm bool) (string, error) {
	t.Helper()
	prog := mustParseProg(t, src)
	var buf bytes.Buffer
	old := stdout
	oldVM := vmEnabled
	stdout = &buf
	defer func() { stdout = old; vmEnabled = oldVM }()
	env := NewEnv()
	var err error
	if vm {
		vmEnabled = true
		err = RunProgramVM(prog, env)
	} else {
		vmEnabled = false // pure tree-walking oracle: functions walk too
		err = RunProgram(prog, env)
	}
	return buf.String(), err
}

func TestVMCompilesSubset(t *testing.T) {
	for _, src := range vmSubset {
		if _, ok := compileProgram(mustParseProg(t, src)); !ok {
			t.Errorf("expected the VM to compile this program, but it fell back to the walker:\n%s", src)
		}
	}
}

func TestVMParity(t *testing.T) {
	all := append([]string{}, vmSubset...)
	all = append(all, vmParityErr...)
	all = append(all, vmParityExtra...)
	for _, src := range all {
		wOut, wErr := runBackend(t, src, false)
		vOut, vErr := runBackend(t, src, true)
		if wOut != vOut {
			t.Errorf("output mismatch for:\n%s\n  walker=%q\n  vm    =%q", src, wOut, vOut)
		}
		if (wErr == nil) != (vErr == nil) {
			t.Errorf("error-outcome mismatch for:\n%s\n  walker=%v\n  vm    =%v", src, wErr, vErr)
		}
	}
}

// walkerFallbacks counts, recursively, the compiled functions in a proto tree
// whose body did NOT compile (Proto == nil) and therefore tree-walk.
func walkerFallbacks(p *Proto) int {
	n := 0
	for _, t := range p.Protos {
		if t.Proto == nil {
			n++
		} else {
			n += walkerFallbacks(t.Proto)
		}
	}
	return n
}

// TestVMCompilesExamples is the coverage probe: the real example scripts must
// compile top-to-bottom with zero functions falling back to the tree-walker.
func TestErrorPositions(t *testing.T) {
	cases := []struct {
		name string
		src  string
		line int
	}{
		{"undefined-var", "$x := 1\n$y := 2\nsay($nope)", 3},
		{"unknown-fn", "say(1)\nnope(2)", 2},
		{"type-error", "$a := 5\nsay($a * \"x\")", 2},
		{"in-function", "fn .f($n) {\n  $n + \"x\"\n}\nsay(.f(1))", 2},
	}
	for _, c := range cases {
		_, err := runBackend(t, c.src, true) // VM (the production path)
		if err == nil {
			t.Errorf("%s: expected a runtime error", c.name)
			continue
		}
		line, _, ok := ErrorPos(err)
		if !ok {
			t.Errorf("%s: error carries no position: %v", c.name, err)
			continue
		}
		if line != c.line {
			t.Errorf("%s: error at line %d, want %d (%v)", c.name, line, c.line, err)
		}
	}
}

func TestLoopControlParseGating(t *testing.T) {
	// break/next are valid only lexically inside a loop in the current function;
	// the parser must reject strays (reset at fn/lambda boundaries) and accept the
	// in-loop forms, including postfix modifiers.
	bad := []string{
		`break`,                                // top level
		`next`,                                 // top level
		`if true { break }`,                    // if is not a loop
		`for $i in 1..3 { fn .f() { break } }`, // can't escape a function
		`for $i in 1..3 { $g := || { next } }`, // can't escape a lambda
		`for $i in 1..3 { map([1], |$x| break) }`, // can't escape a lambda arg
	}
	for _, src := range bad {
		p := parser.New(src)
		p.ParseProgram()
		if len(p.Errors()) == 0 {
			t.Errorf("expected a parse error for %q, got none", src)
		}
	}
	good := []string{
		`for $i in 1..3 { break }`,
		`while true { next }`,
		`for $i in 1..9 { break if $i == 3 }`,   // postfix if
		`for $i in 1..9 { next unless $i > 2 }`, // postfix unless
		`for $i in 1..3 { if $i == 2 { break } }`,
		`for $i in 1..3 { for $j in 1..3 { break } }`, // nested
	}
	for _, src := range good {
		p := parser.New(src)
		p.ParseProgram()
		if errs := p.Errors(); len(errs) > 0 {
			t.Errorf("unexpected parse error for %q: %v", src, errs)
		}
	}
}

func TestRegexValue(t *testing.T) {
	cases := []struct{ src, want string }{
		// qr// and re() produce equal values for the same source; flags are part of identity
		{`say(qr/\d+/ == qr/\d+/)`, "true\n"},
		{`say(qr/\d+/ == re(q(\d+)))`, "true\n"},
		{`say(qr/x/i == qr/x/)`, "false\n"},
		// a regex value works wherever a string pattern does
		{`say(matches("a9", qr/\d/), matches("a9", q(\d)))`, "true true\n"},
		// flags: i (case-insensitive) baked in
		{`say(matches("ABC", qr/abc/i))`, "true\n"},
		// a bad pattern is a catchable Err value, not an abort (both qr and re)
		{`say(is_err(qr/[/), is_err(re("(")))`, "true true\n"},
		{`say(qr/[/ // "recovered")`, "recovered\n"},
		// re() passes an existing regex through unchanged
		{`$r := qr/\w+/
say(re($r) == $r)`, "true\n"},
		// Display picks a delimiter the pattern avoids, so it round-trips
		{`say(qr{a/b})`, "qr|a/b|\n"},
		{`say(qr|a/b| == qr{a/b})`, "true\n"},
	}
	for _, c := range cases {
		got, err := runBackend(t, c.src, true)
		if err != nil {
			t.Errorf("%q: unexpected error: %v", c.src, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q: got %q, want %q", c.src, got, c.want)
		}
	}
}

func TestQuoteHeredocEdges(t *testing.T) {
	cases := []struct{ src, want string }{
		// empty heredoc body is "" (Perl/Ruby line-based model); a blank line is "\n"
		{"$x := <<END\nEND\nsay(len($x))", "0\n"},
		{"$x := <<END\n\nEND\nsay(len($x))", "1\n"},
		{"$x := <<END\nhi\nEND\nsay(len($x))", "3\n"}, // "hi\n"
		// a non-brace delimiter handles a brace inside a ${...} interpolation
		{"say(qq[x${ \"}\" }y])", "x}y\n"},
	}
	for _, c := range cases {
		got, err := runBackend(t, c.src, true)
		if err != nil {
			t.Errorf("%q: unexpected error: %v", c.src, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q: got %q, want %q", c.src, got, c.want)
		}
	}
}

func TestVMCompilesExamples(t *testing.T) {
	files, _ := filepath.Glob("../../examples/*.dr")
	if len(files) == 0 {
		t.Skip("no example scripts found")
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		prog := mustParseProg(t, string(src))
		p, ok := compileProgram(prog)
		if !ok {
			t.Errorf("%s: top level fell back to the walker", filepath.Base(f))
			continue
		}
		if fb := walkerFallbacks(p); fb != 0 {
			t.Errorf("%s: %d function(s) fell back to the walker", filepath.Base(f), fb)
		} else {
			t.Logf("%s: fully compiled on the VM", filepath.Base(f))
		}
	}
}

func benchFib(b *testing.B, vm bool) {
	prog := mustParseProg(b, `fn .fib($n) {
  if $n < 2 { $n } else { .fib($n - 1) + .fib($n - 2) }
}
.fib(28)`)
	oldVM, oldOut := vmEnabled, stdout
	vmEnabled, stdout = vm, io.Discard
	defer func() { vmEnabled, stdout = oldVM, oldOut }()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := RunProgram(prog, NewEnv()); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFibVM vs BenchmarkFibWalker measures the register VM against the pure
// tree-walker on recursion-heavy code (~832k calls per op).
func BenchmarkFibVM(b *testing.B)     { benchFib(b, true) }
func BenchmarkFibWalker(b *testing.B) { benchFib(b, false) }

func benchGlue(b *testing.B, vm bool) {
	// Representative glue: a loop with map autovivification + compound assignment,
	// then a for-in fold — the shape of real counting/reporting scripts.
	prog := mustParseProg(b, `fn .process($n) {
  $counts := {}
  $i := 0
  while $i < $n {
    $counts[$i % 7] += 1
    $i += 1
  }
  $total := 0
  for $k, $v in $counts { $total += $v * $k }
  $total
}
.process(200000)`)
	oldVM, oldOut := vmEnabled, stdout
	vmEnabled, stdout = vm, io.Discard
	defer func() { vmEnabled, stdout = oldVM, oldOut }()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := RunProgram(prog, NewEnv()); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGlueVM vs BenchmarkGlueWalker measures a realistic collection/loop
// workload (map autoviv, compound assignment, for-in fold).
func BenchmarkGlueVM(b *testing.B)     { benchGlue(b, true) }
func BenchmarkGlueWalker(b *testing.B) { benchGlue(b, false) }

func benchBuiltin(b *testing.B, vm bool) {
	// A builtin called every iteration — the case direct dispatch (skip env.get)
	// targets.
	prog := mustParseProg(b, `fn .totallen($n) {
  $s := 0
  $i := 0
  while $i < $n {
    $s += len("hello")
    $i += 1
  }
  $s
}
.totallen(300000)`)
	oldVM, oldOut := vmEnabled, stdout
	vmEnabled, stdout = vm, io.Discard
	defer func() { vmEnabled, stdout = oldVM, oldOut }()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := RunProgram(prog, NewEnv()); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkBuiltinVM vs BenchmarkBuiltinWalker measures a builtin call in a hot
// loop (the direct-dispatch target).
func BenchmarkBuiltinVM(b *testing.B)     { benchBuiltin(b, true) }
func BenchmarkBuiltinWalker(b *testing.B) { benchBuiltin(b, false) }
