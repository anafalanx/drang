package eval

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/anafalanx/drang/internal/lexer"
	"github.com/anafalanx/drang/internal/parser"
)

func TestDuplicateTopFns(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{"none", "fn .a() { 1 }\nfn .b() { 2 }", nil},
		{"one-dup", "fn .a() { 1 }\nfn .a() { 2 }", []string{".a"}},
		{"triple-reported-once", "fn .a() {1}\nfn .a() {2}\nfn .a() {3}", []string{".a"}},
		{"two-distinct-dups-in-order", "fn .a(){1}\nfn .b(){1}\nfn .a(){2}\nfn .b(){2}", []string{".a", ".b"}},
		// Only the top level counts: a fn defined in each branch is a deliberate
		// conditional definition, not a duplicate.
		{"conditional-branches-not-counted", "if true { fn .h(){1} } else { fn .h(){2} }", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DuplicateTopFns(mustParse(t, c.src))
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("DuplicateTopFns(%q) = %v, want %v", c.src, got, c.want)
			}
		})
	}
}

func TestWarnDuplicateTopFns(t *testing.T) {
	// A duplicate prints exactly one warning line, naming both the origin and the function.
	var buf bytes.Buffer
	WarnDuplicateTopFns(mustParse(t, "fn .a(){1}\nfn .a(){2}"), "prog.dr", &buf)
	got := buf.String()
	for _, want := range []string{"prog.dr", ".a", "defined more than once", "last definition wins"} {
		if !strings.Contains(got, want) {
			t.Errorf("warning missing %q; got %q", want, got)
		}
	}
	if n := strings.Count(got, "\n"); n != 1 {
		t.Errorf("want exactly one warning line, got %d in %q", n, got)
	}
	// No duplicate → no output at all.
	buf.Reset()
	WarnDuplicateTopFns(mustParse(t, "fn .a(){1}\nfn .b(){2}"), "prog.dr", &buf)
	if buf.Len() != 0 {
		t.Errorf("expected no warning, got %q", buf.String())
	}
}

func migrationWarnings(t *testing.T, src string) []LintWarning {
	t.Helper()
	p := parser.New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse: %v\nsrc:\n%s", errs, src)
	}
	return MigrationWarnings(prog, src, p.Comments())
}

func warningsWithCode(warnings []LintWarning, code string) []LintWarning {
	var out []LintWarning
	for _, warning := range warnings {
		if warning.Code == code {
			out = append(out, warning)
		}
	}
	return out
}

func TestMigrationLintDiscardedFallibleCalls(t *testing.T) {
	src := `read_file("x")
$x := read_file("x")
read_file("x")?
say(read_file("x") // "fallback")
fn .implicit_ok() { read_file("x") }
fn .discard_bad() { read_file("x"); 1 }`
	warnings := warningsWithCode(migrationWarnings(t, src), lintErrDiscard)
	if len(warnings) != 2 {
		t.Fatalf("discard warnings = %+v, want lines 1 and 6 only", warnings)
	}
	if warnings[0].Line != 1 || warnings[1].Line != 6 {
		t.Fatalf("discard warning lines = %d, %d; want 1, 6", warnings[0].Line, warnings[1].Line)
	}
}

func TestMigrationLintTerminalDispatchIsNotDiscarded(t *testing.T) {
	src := `fn .x() { 1 }
dispatch({x: .x})`
	warnings := warningsWithCode(migrationWarnings(t, src), lintErrDiscard)
	if len(warnings) != 0 {
		t.Fatalf("terminal dispatch produced discard warnings: %+v", warnings)
	}
}

func TestMigrationLintRespectsBuiltinShadowing(t *testing.T) {
	src := `$read_file := |$path| "synthetic"
read_file("x")`
	if warnings := migrationWarnings(t, src); len(warnings) != 0 {
		t.Fatalf("shadowed builtin warnings = %+v, want none", warnings)
	}
}

func TestMigrationLintShadowingIsLexicallyScoped(t *testing.T) {
	src := `$f := |$read_file, $say| 1
read_file("outside")
say(read_file("also-outside"))
fn .local($read_file) { read_file("synthetic") }
read_file("after-function")`
	warnings := migrationWarnings(t, src)
	discard := warningsWithCode(warnings, lintErrDiscard)
	output := warningsWithCode(warnings, lintErrOutput)
	if len(discard) != 2 || discard[0].Line != 2 || discard[1].Line != 5 {
		t.Fatalf("lexical discard warnings = %+v, want lines 2 and 5", discard)
	}
	if len(output) != 1 || output[0].Line != 3 {
		t.Fatalf("lexical output warnings = %+v, want line 3", output)
	}
}

func TestMigrationLintShadowingBeginsAfterDeclaration(t *testing.T) {
	src := `$read_file := read_file("initializer")
read_file("user-value")`
	warnings := warningsWithCode(migrationWarnings(t, src), lintErrDiscard)
	if len(warnings) != 0 {
		t.Fatalf("post-declaration user call warnings = %+v, want none", warnings)
	}
	all := migrationWarnings(t, `say(read_file("initializer"))
$read_file := |$path| "synthetic"
read_file("user-value")`)
	output := warningsWithCode(all, lintErrOutput)
	if len(output) != 1 || output[0].Line != 1 {
		t.Fatalf("declaration-order output warnings = %+v, want line 1", output)
	}
}

func TestMigrationLintRespectsControlAndOutputBuiltinShadowing(t *testing.T) {
	src := `$say := |$x| $x
$warn := |$x| $x
$die := |$x| $x
$bool := |$x| true
say(read_file("x"))
warn(read_file("x"))
die(read_file("x"))
if bool(read_file("x")) { 1 }
read_file("x") |> say
read_file("x") |> bool`
	if warnings := migrationWarnings(t, src); len(warnings) != 0 {
		t.Fatalf("shadowed control/output builtin warnings = %+v, want none", warnings)
	}
}

func TestKnownFallibleLintNamesResolve(t *testing.T) {
	for name := range knownFallibleNames {
		if !isNonUserName(name) {
			t.Errorf("known-fallible lint name %q is not a registered builtin or special form", name)
		}
	}
	for _, name := range []string{"cwd", "home", "exe"} {
		if !knownFallibleNames[name] {
			t.Errorf("runtime-fallible builtin %q is missing from the lint set", name)
		}
	}
	for _, name := range []string{"parse_args", "str"} {
		if !knownFallibleNames[name] {
			t.Errorf("resource-fallible builtin %q is missing from the lint set", name)
		}
	}
	if knownFallibleNames["rand"] {
		t.Error("total builtin rand must not be in the fallible lint set")
	}
	if knownFallibleNames["capture_all"] {
		t.Error("capture_all always returns its status record and must not be classified as an Err result")
	}
}

func TestMigrationLintMatchesDefaultParameterBindingOrder(t *testing.T) {
	src := `fn .f($read_file = str(read_file("a")), $later = str(read_file("shadowed"))) { say(read_file("body")) }
$g := |$x = str(read_file("b")), $read_file = 1| say(read_file("lambda-body"))`
	warnings := warningsWithCode(migrationWarnings(t, src), lintErrOutput)
	if len(warnings) != 2 || warnings[0].Line != 1 || warnings[1].Line != 2 {
		t.Fatalf("default-parameter warnings = %+v, want one pre-binding warning per line", warnings)
	}
}

func TestMigrationLintMatchesLogicalSelectedLiteralAndJoinSemantics(t *testing.T) {
	src := `say(join(fail("join-arg")))
say([fail("unselected"), 1][1])
say(fail("control") and "ok")
say(str($ARGV))
parse_args($ARGV)`
	warnings := migrationWarnings(t, src)
	output := warningsWithCode(warnings, lintErrOutput)
	if len(output) != 2 || output[0].Line != 1 || output[1].Line != 4 ||
		!strings.Contains(output[0].Message, "join") || !strings.Contains(output[1].Message, "str") {
		t.Fatalf("output warnings = %+v, want join on line 1 and str on line 4", output)
	}
	boolWarnings := warningsWithCode(warnings, lintErrBool)
	if len(boolWarnings) != 1 || boolWarnings[0].Line != 3 {
		t.Fatalf("boolean warnings = %+v, want fail control source on line 3", boolWarnings)
	}
	discard := warningsWithCode(warnings, lintErrDiscard)
	if len(discard) != 1 || discard[0].Line != 5 || !strings.Contains(discard[0].Message, "parse_args") {
		t.Fatalf("discard warnings = %+v, want parse_args on line 5", discard)
	}
}

func TestMigrationLintModelsJoinRenderedOperands(t *testing.T) {
	src := `say(join([fail("element"), 1], fail("separator")))
say(join([fail("only")], fail("unused-separator")))
join(read_file("not-an-array"))`
	warnings := migrationWarnings(t, src)
	output := warningsWithCode(warnings, lintErrOutput)
	if len(output) != 3 || output[0].Line != 1 || output[1].Line != 1 || output[2].Line != 2 {
		t.Fatalf("join output warnings = %+v, want two line 1 sources and one line 2 source", output)
	}
	discard := warningsWithCode(warnings, lintErrDiscard)
	if len(discard) != 1 || discard[0].Line != 3 || !strings.Contains(discard[0].Message, "join") {
		t.Fatalf("join discard warnings = %+v, want join's replacement Err on line 3", discard)
	}
}

func TestMigrationLintInterpolationUsesOuterCoordinates(t *testing.T) {
	src := `say(read_file("line1"))
$"a ${read_file(q{x})} b ${http_get(q{y})}"
<<~$TXT
    ${read_file(q{z})}
    ${http_get(q{w})}
TXT`
	output := warningsWithCode(migrationWarnings(t, src), lintErrOutput)
	want := [][2]int{{1, 5}, {2, 7}, {2, 28}, {4, 7}, {5, 7}}
	if len(output) != len(want) {
		t.Fatalf("interpolation warnings = %+v, want %v", output, want)
	}
	for i, pos := range want {
		if output[i].Line != pos[0] || output[i].Col != pos[1] {
			t.Errorf("warning %d = %d:%d, want %d:%d", i, output[i].Line, output[i].Col, pos[0], pos[1])
		}
	}
}

func TestMigrationLintRebasesLambdaBodyInsideInterpolation(t *testing.T) {
	src := "1\n" + `$"${|$x| { say(read_file(q{x})); $x }}"`
	output := warningsWithCode(migrationWarnings(t, src), lintErrOutput)
	wantCol := strings.Index(strings.Split(src, "\n")[1], "read_file") + 1
	found := false
	for _, warning := range output {
		if warning.Line == 2 && warning.Col == wantCol && strings.Contains(warning.Message, "read_file") {
			found = true
		}
	}
	if !found {
		t.Fatalf("lambda-body warnings = %+v, want read_file at 2:%d", output, wantCol)
	}
}

func TestMigrationLintErrInBooleanAndOutputContexts(t *testing.T) {
	src := `if read_file("x") { say("bad") }
while int("x") { break }
say(http_get("http://example.invalid"))
warn(is_err(http_get("http://example.invalid")))
!capture("cmd", "/c", "exit 1")`
	warnings := migrationWarnings(t, src)
	boolWarnings := warningsWithCode(warnings, lintErrBool)
	outputWarnings := warningsWithCode(warnings, lintErrOutput)
	if len(boolWarnings) != 3 {
		t.Fatalf("boolean warnings = %+v, want lines 1, 2, 5", boolWarnings)
	}
	if len(outputWarnings) != 1 || outputWarnings[0].Line != 3 {
		t.Fatalf("output warnings = %+v, want line 3 only", outputWarnings)
	}
	for _, warning := range warnings {
		if warning.Line < 1 || warning.Col < 1 {
			t.Fatalf("warning lacks a source position: %+v", warning)
		}
	}
}

func TestMigrationLintRecognizesDerivedDiscardedErr(t *testing.T) {
	src := `-read_file("x")
read_file("x") + 1
read_file("x").field
read_file("x")[0]
read_file("x") ~ "suffix"
read_file("x") == fail("x")`
	all := migrationWarnings(t, src)
	warnings := warningsWithCode(all, lintErrDiscard)
	if len(warnings) != 4 {
		t.Fatalf("derived discard warnings = %+v, want lines 1-4 only", warnings)
	}
	for i, warning := range warnings {
		if warning.Line != i+1 {
			t.Fatalf("derived discard warning %d = %+v, want line %d", i, warning, i+1)
		}
	}
	output := warningsWithCode(all, lintErrOutput)
	if len(output) != 1 || output[0].Line != 5 {
		t.Fatalf("derived stringification warnings = %+v, want output warning on line 5", output)
	}
}

func TestMigrationLintRecognizesNestedStringifyingSinks(t *testing.T) {
	src := `say([read_file("a")])
warn({result: read_file("b")})
str(read_file("c"))
format("{}", read_file("d"))
join([read_file("e")], ",")
write_file("out", read_file("f"))?
send_stdin($proc, read_file("g"))?
say("prefix" ~ read_file("h"))
$qq{value ${read_file("i")}}`
	warnings := warningsWithCode(migrationWarnings(t, src), lintErrOutput)
	if len(warnings) != 9 {
		t.Fatalf("nested output warnings = %+v, want one per line", warnings)
	}
	for i, warning := range warnings[:8] {
		if warning.Line != i+1 {
			t.Fatalf("nested output warning %d = %+v, want line %d", i, warning, i+1)
		}
	}
	// Interpolated-part positions currently inherit the part parser's local line;
	// the warning is still present and points at the fallible source column.
	if warnings[8].Col < 1 {
		t.Fatalf("interpolation warning lacks a source column: %+v", warnings[8])
	}
}

func TestMigrationLintClassifiesStandaloneStringificationAsOutput(t *testing.T) {
	src := `str(read_file("a"))
format("{}", read_file("b"))
join([read_file("c")], ",")
read_file("d") ~ "suffix"
$qq{value ${read_file("e")}}`
	warnings := migrationWarnings(t, src)
	output := warningsWithCode(warnings, lintErrOutput)
	discard := warningsWithCode(warnings, lintErrDiscard)
	if len(output) != 5 {
		t.Fatalf("standalone stringification output warnings = %+v, want five", output)
	}
	if len(discard) != 0 {
		t.Fatalf("standalone stringification discard warnings = %+v, want none", discard)
	}
}

func TestMigrationLintFindsIndependentAndNestedOutputSources(t *testing.T) {
	src := `$x := read_file("assigned") ~ ""
if read_file("condition") ~ "" { 1 }
say([read_file("a"), http_get("b")])
format("{} {}", read_file("c"), http_get("d"))`
	output := warningsWithCode(migrationWarnings(t, src), lintErrOutput)
	if len(output) != 6 {
		t.Fatalf("independent output warnings = %+v, want six source sites", output)
	}
	wantLines := []int{1, 2, 3, 3, 4, 4}
	for i, warning := range output {
		if warning.Line != wantLines[i] {
			t.Fatalf("independent output warning %d = %+v, want line %d", i, warning, wantLines[i])
		}
	}
	if boolWarnings := warningsWithCode(migrationWarnings(t, src), lintErrBool); len(boolWarnings) != 0 {
		t.Fatalf("stringified conditions produced inaccurate Err-truthiness warnings: %+v", boolWarnings)
	}
}

func TestMigrationLintRecognizesSelectedLiteralErr(t *testing.T) {
	src := `[read_file("a"), 1][0]
{result: read_file("b")}.result
[read_file("not-selected"), 1][1]
{result: read_file("not-selected")}.other`
	warnings := warningsWithCode(migrationWarnings(t, src), lintErrDiscard)
	if len(warnings) != 2 || warnings[0].Line != 1 || warnings[1].Line != 2 {
		t.Fatalf("selected literal discard warnings = %+v, want lines 1 and 2 only", warnings)
	}
}

func TestMigrationLintModelsLastWriteWinsLiteralMapSelection(t *testing.T) {
	src := `say({a: fail("overwritten"), a: q{ok}}.a)
say({a: q{ok}, a: fail("selected")}.a)
say({q{a}: fail("overwritten"), q{a}: q{ok}}[q{a}])
say({q{a}: q{ok}, q{a}: fail("selected")}[q{a}])
say({1: fail("numeric-overwritten"), 1.0: q{ok}}[1])`
	output := warningsWithCode(migrationWarnings(t, src), lintErrOutput)
	if len(output) != 2 || output[0].Line != 2 || output[1].Line != 4 {
		t.Fatalf("last-write-wins output warnings = %+v, want selected values on lines 2 and 4", output)
	}
}

func TestMigrationLintDoesNotWarnExplicitErrDataHandling(t *testing.T) {
	src := `$a := is_err(read_file("a"))
$b := err_msg(read_file("b"))
$c := err_code(read_file("c"))
$d := read_file("d") == fail("d")
$e := [read_file("e")]
$f := {result: read_file("f")}
say(read_file("g")?)
write_file("out", read_file("h")?)?`
	if warnings := migrationWarnings(t, src); len(warnings) != 0 {
		t.Fatalf("explicit Err-data handling warnings = %+v, want none", warnings)
	}
}

func TestMigrationLintAutoPrintAssignment(t *testing.T) {
	src := `$_ = read_file("x")
$saved = read_file("y")`
	p := parser.New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatal(errs)
	}
	plain := MigrationWarnings(prog, src, p.Comments())
	if warnings := warningsWithCode(plain, lintErrOutput); len(warnings) != 0 {
		t.Fatalf("plain assignment output warnings = %+v, want none", warnings)
	}
	stream := MigrationWarningsWithOptions(prog, src, p.Comments(), LintOptions{AutoPrint: true})
	warnings := warningsWithCode(stream, lintErrOutput)
	if len(warnings) != 1 || warnings[0].Line != 1 || !strings.Contains(warnings[0].Message, "-p automatic output") {
		t.Fatalf("auto-print output warnings = %+v, want line 1 only", warnings)
	}
}

func TestMigrationLintSuppressions(t *testing.T) {
	src := `# lint:ignore err-discard
read_file("x")
say(read_file("x")) # lint:ignore err-output

# lint:ignore
if read_file("x") { say("bad") }`
	if warnings := migrationWarnings(t, src); len(warnings) != 0 {
		t.Fatalf("suppressed warnings = %+v, want none", warnings)
	}
}

func TestLintSuppressionsStaySparseAcrossManyBlankLines(t *testing.T) {
	const blankLines = 200_000
	src := strings.Repeat("\n", blankLines) +
		"# lint:ignore err-discard\r\n\r\n# another comment\r\nread_file(\"x\")\n"
	commentLine := blankLines + 1
	targetLine := blankLines + 4
	got := buildLintSuppressions(src, []lexer.Comment{{
		Text: "# lint:ignore err-discard",
		Line: commentLine,
		Col:  1,
	}})
	if len(got) != 1 || !got[targetLine][lintErrDiscard] {
		t.Fatalf("sparse suppression targets = %+v, want only line %d", got, targetLine)
	}
}

func TestMigrationLintSuppressionCoversMultilineStatement(t *testing.T) {
	src := `# lint:ignore err-output
say(
  read_file("x"),
  "fallback"
)`
	if warnings := migrationWarnings(t, src); len(warnings) != 0 {
		t.Fatalf("multiline statement warnings = %+v, want none", warnings)
	}
}

func TestMigrationLintWindowsPathsPreferRawStrings(t *testing.T) {
	src := `$a := "C:\temp\new"
$b := qq{\\server\share\file}
$c := q{C:\temp\new}
$d := 'C:\temp\new'
$e := "C:/temp/new"
$f := "D:\temp\new" # lint:ignore windows-path`
	warnings := warningsWithCode(migrationWarnings(t, src), lintWindowsPath)
	if len(warnings) != 2 || warnings[0].Line != 1 || warnings[1].Line != 2 {
		t.Fatalf("windows-path warnings = %+v, want escaped forms on lines 1 and 2", warnings)
	}
}

func TestWarnProgramLintsIncludesPositionAndCode(t *testing.T) {
	src := "say(read_file(\"x\"))\n"
	p := parser.New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatal(errs)
	}
	var buf bytes.Buffer
	WarnProgramLints(prog, src, "prog.dr", p.Comments(), &buf)
	got := buf.String()
	for _, want := range []string{"prog.dr:1:", lintErrOutput, "read_file"} {
		if !strings.Contains(got, want) {
			t.Errorf("warning missing %q: %q", want, got)
		}
	}
}
