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
