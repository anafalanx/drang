package eval

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anafalanx/drang/internal/ast"
	"github.com/anafalanx/drang/internal/parser"
)

func mustParse(t *testing.T, src string) *ast.Program {
	t.Helper()
	p := parser.New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse %q: %v", src, errs)
	}
	return prog
}

func TestGoldenDetachedSpawnSafe(t *testing.T) {
	// A detached (un-awaited) spawn that prints after the top level returns must not
	// race the capture swap or crash. Run under `go test -race` to exercise the fix;
	// the top-level output is captured (the detached line may or may not be — that's
	// inherently nondeterministic, so we don't assert on it).
	dir := t.TempDir()
	golden := filepath.Join(dir, "d.golden")
	src := `fn .bg() { say("bg") }
$t := spawn(.bg)
say("top")`
	var buf bytes.Buffer
	if _, _, err := RunExamples(mustParse(t, src), dir, "d.dr", golden, true, &buf); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got, _ := os.ReadFile(golden); !strings.Contains(string(got), "top") {
		t.Errorf("top-level output should be captured: %q", string(got))
	}
}

func TestGoldenOutput(t *testing.T) {
	dir := t.TempDir()
	golden := filepath.Join(dir, "t.golden")
	script := `say("hello")
say("world")`

	// --update writes the golden from captured stdout.
	var buf bytes.Buffer
	if _, _, err := RunExamples(mustParse(t, script), dir, "t.dr", golden, true, &buf); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got, _ := os.ReadFile(golden); string(got) != "hello\nworld\n" {
		t.Fatalf("golden not written correctly: %q", string(got))
	}

	// Matching output passes.
	buf.Reset()
	pass, fail, err := RunExamples(mustParse(t, script), dir, "t.dr", golden, false, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if pass != 1 || fail != 0 {
		t.Errorf("matching golden should pass; got %d/%d\n%s", pass, fail, buf.String())
	}

	// Changed output fails (and the diff names the changed line).
	buf.Reset()
	pass, fail, err = RunExamples(mustParse(t, `say("hello")
say("CHANGED")`), dir, "t.dr", golden, false, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if fail != 1 {
		t.Errorf("changed output should fail golden; got %d/%d", pass, fail)
	}
	if !strings.Contains(buf.String(), "CHANGED") {
		t.Errorf("diff should show the changed line:\n%s", buf.String())
	}
}

func TestGoldenDiffLimitPreservesLineSemantics(t *testing.T) {
	prefix := strings.Repeat("same\n", 32)
	expected := prefix + "old1\nold2\nold3\ntail\n"
	actual := prefix + "new1\nnew2\nnew3\nnew4\ntail\n"
	want := `        @@ first difference at line 33 @@
        - old1
        - old2
        … (1 more expected lines)
        + new1
        + new2
        … (2 more actual lines)
`
	if got := goldenDiffLimit(expected, actual, 2); got != want {
		t.Fatalf("bounded golden diff:\n%s\nwant:\n%s", got, want)
	}

	// strings.Split treats a trailing newline as an additional empty line; retain
	// that observable edge case without materializing a line slice.
	want = "        @@ first difference at line 2 @@\n        + \n"
	if got := goldenDiffLimit("a", "a\n", 2); got != want {
		t.Fatalf("trailing-empty-line diff = %q, want %q", got, want)
	}
}

func TestGoldenDiffLineScannerMatchesSplitReference(t *testing.T) {
	texts := []string{"", "a", "\n", "a\n", "\na", "a\nb", "a\n\nb", "a\r\nb\r\n"}
	for _, expected := range texts {
		for _, actual := range texts {
			for _, limit := range []int{0, 1, 2, 20} {
				got := goldenDiffLimit(expected, actual, limit)
				want := splitGoldenDiffLimit(expected, actual, limit)
				if got != want {
					t.Fatalf("goldenDiffLimit(%q, %q, %d) = %q, want %q", expected, actual, limit, got, want)
				}
			}
		}
	}
}

func splitGoldenDiffLimit(expected, actual string, maxPerSide int) string {
	if expected == actual {
		return ""
	}
	exp := strings.Split(expected, "\n")
	act := strings.Split(actual, "\n")
	prefix := 0
	for prefix < len(exp) && prefix < len(act) && exp[prefix] == act[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(exp)-prefix && suffix < len(act)-prefix && exp[len(exp)-1-suffix] == act[len(act)-1-suffix] {
		suffix++
	}
	var b strings.Builder
	fmt.Fprintf(&b, "        @@ first difference at line %d @@\n", prefix+1)
	expLines, actLines := exp[prefix:len(exp)-suffix], act[prefix:len(act)-suffix]
	for i, line := range expLines {
		if i >= maxPerSide {
			fmt.Fprintf(&b, "        … (%d more expected lines)\n", len(expLines)-i)
			break
		}
		fmt.Fprintf(&b, "        - %s\n", line)
	}
	for i, line := range actLines {
		if i >= maxPerSide {
			fmt.Fprintf(&b, "        … (%d more actual lines)\n", len(actLines)-i)
			break
		}
		fmt.Fprintf(&b, "        + %s\n", line)
	}
	return b.String()
}

func TestGoldenCaptureAndExpectedOutputAreBounded(t *testing.T) {
	oldLimit := maxGoldenOutputBytes
	maxGoldenOutputBytes = 8
	defer func() { maxGoldenOutputBytes = oldLimit }()

	dir := t.TempDir()
	golden := filepath.Join(dir, "bounded.golden")
	var report bytes.Buffer
	_, _, err := RunExamples(mustParse(t, `say("123456789")`), dir, "bounded.dr", golden, true, &report)
	if err == nil || !strings.Contains(err.Error(), "8-byte limit") {
		t.Fatalf("oversized captured output error = %v, want limit failure", err)
	}

	if err := os.WriteFile(golden, []byte("123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = RunExamples(mustParse(t, `$x := 1`), dir, "bounded.dr", golden, false, &report)
	if err == nil || !strings.Contains(err.Error(), "golden output exceeds") {
		t.Fatalf("oversized golden error = %v, want bounded-read failure", err)
	}
}

func TestRunExamplesPropagatesReportWriterFailure(t *testing.T) {
	_, _, err := RunExamples(mustParse(t, `example 1 == 2`), "", "bad.dr", "", false, failingWriter{})
	if err == nil || !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("report error = %v, want writer failure", err)
	}
}

func TestRunExamplesPassFail(t *testing.T) {
	src := `fn .add($a, $b) { $a + $b }
fn .boom() { fail("x") }
example .add(2, 3) == 5
example .add(2, 3) == 6
example len([1, 2, 3]) == 3
example "truthy"
example .boom() fails
example 1 fails`
	p := parser.New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	var buf bytes.Buffer
	pass, fail, lerr := RunExamples(prog, "", "test.dr", "", false, &buf)
	if lerr != nil {
		t.Fatalf("load: %v", lerr)
	}
	if pass != 4 || fail != 2 {
		t.Errorf("got %d passed, %d failed; want 4, 2\noutput:\n%s", pass, fail, buf.String())
	}
	if !strings.Contains(buf.String(), "expected 6, got 5") {
		t.Errorf("missing == failure detail in:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "expected an error, but got 1") {
		t.Errorf("missing fails-form detail in:\n%s", buf.String())
	}
}

func TestExampleTopLevelExitNotMasked(t *testing.T) {
	// A top-level exit() must not skip the examples (it once silently reported green).
	src := "example 1 == 2\nexit(0)"
	p := parser.New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	var buf bytes.Buffer
	pass, fail, lerr := RunExamples(prog, "", "x.dr", "", false, &buf)
	if code, ok := ExitRequested(lerr); !ok || code != 0 {
		t.Fatalf("retained exit = (%d, %v), want explicit exit(0)", code, ok)
	}
	if pass != 0 || fail != 1 {
		t.Errorf("got %d passed, %d failed; want 0, 1 (exit must not mask)\n%s", pass, fail, buf.String())
	}
}

func TestRunExamplesRetainsNonzeroTopLevelExit(t *testing.T) {
	var buf bytes.Buffer
	pass, fail, lerr := RunExamples(mustParse(t, "example true\nexit(7)"), "", "x.dr", "", false, &buf)
	if code, ok := ExitRequested(lerr); !ok || code != 7 {
		t.Fatalf("retained exit = (%d, %v), want explicit exit(7)", code, ok)
	}
	if pass != 1 || fail != 0 {
		t.Fatalf("examples after setup exit = %d passed, %d failed; want 1, 0", pass, fail)
	}
}

func TestRunExamplesProvidesDefaultStorePath(t *testing.T) {
	dir := t.TempDir()
	origin := filepath.Join(dir, "stateful.dr")
	src := `$s := store()?
store_set($s, "k", 7)?
store_close($s)?
example true`
	var buf bytes.Buffer
	pass, fail, lerr := RunExamples(mustParse(t, src), dir, origin, "", false, &buf)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if pass != 1 || fail != 0 {
		t.Fatalf("store-backed examples = %d passed, %d failed; want 1, 0", pass, fail)
	}
	if _, err := os.Stat(filepath.Join(dir, ".drang", "stateful.store")); err != nil {
		t.Fatalf("default test store was not created beside the script: %v", err)
	}
}

func TestExampleSubjectExitIsFailure(t *testing.T) {
	// exit()/die() inside an example must not be swallowed as a passing `fails`.
	src := "example exit(3) fails"
	p := parser.New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	var buf bytes.Buffer
	pass, fail, lerr := RunExamples(prog, "", "x.dr", "", false, &buf)
	if lerr != nil {
		t.Fatalf("load: %v", lerr)
	}
	if pass != 0 || fail != 1 {
		t.Errorf("exit in an example should fail, not pass; got %d passed, %d failed\n%s", pass, fail, buf.String())
	}
}

func TestRequiredAfterDefaultIsParseError(t *testing.T) {
	p := parser.New("fn .bad($a = 1, $b) { 1 }")
	p.ParseProgram()
	found := false
	for _, e := range p.Errors() {
		if strings.Contains(e, "cannot follow a defaulted one") {
			found = true
		}
	}
	if !found {
		t.Errorf("a required param after a defaulted one should be a parse error, got %v", p.Errors())
	}
}

func TestNestedExampleIsParseError(t *testing.T) {
	p := parser.New("fn .f() {\n  example 1 == 1\n}")
	p.ParseProgram()
	found := false
	for _, e := range p.Errors() {
		if strings.Contains(e, "top level") {
			found = true
		}
	}
	if !found {
		t.Errorf("a nested example should be a parse error, got %v", p.Errors())
	}
}

func TestExampleIsNoopInRun(t *testing.T) {
	// An example must neither run nor error during a normal program run.
	out := run(t, `fn .boom() { fail("x") }
example .boom() fails
example 1 == 2
say("done")`)
	if out != "done\n" {
		t.Errorf("examples leaked into a normal run: %q", out)
	}
}
