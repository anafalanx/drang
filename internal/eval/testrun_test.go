package eval

import (
	"bytes"
	"strings"
	"testing"

	"github.com/anafalanx/drang/internal/parser"
)

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
	pass, fail, lerr := RunExamples(prog, "", "test.dr", &buf)
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
	pass, fail, lerr := RunExamples(prog, "", "x.dr", &buf)
	if lerr != nil {
		t.Fatalf("load: %v", lerr)
	}
	if pass != 0 || fail != 1 {
		t.Errorf("got %d passed, %d failed; want 0, 1 (exit must not mask)\n%s", pass, fail, buf.String())
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
	pass, fail, lerr := RunExamples(prog, "", "x.dr", &buf)
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
