package eval

import (
	"strings"
	"testing"

	"github.com/anafalanx/drang/internal/ast"
	"github.com/anafalanx/drang/internal/parser"
)

func TestLoopKeywordHint(t *testing.T) {
	if h := loopKeywordHint("continue"); !strings.Contains(h, "next") {
		t.Errorf("continue hint should point to next, got %q", h)
	}
	if h := loopKeywordHint("last"); !strings.Contains(h, "break") {
		t.Errorf("last hint should point to break, got %q", h)
	}
	if h := loopKeywordHint("foo"); h != "" {
		t.Errorf("a normal undefined name should get no hint, got %q", h)
	}
}

// A bare continue/last must produce a teaching error (not a cryptic "undefined") on BOTH
// backends, since drang uses Ruby's next/break.
func TestLoopKeywordErrorBothBackends(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"continue", `for $i in 1..3 { continue }`, "next"},
		{"last", `for $i in 1..3 { last }`, "break"},
	}
	backends := []struct {
		label string
		run   func(*ast.Program, *Env) error
	}{
		{"walker", RunProgram},
		{"vm", RunProgramVM},
	}
	for _, c := range cases {
		for _, b := range backends {
			p := parser.New(c.src)
			prog := p.ParseProgram()
			if errs := p.Errors(); len(errs) > 0 {
				t.Fatalf("%s/%s parse errors: %v", c.name, b.label, errs)
			}
			err := b.run(prog, NewEnv())
			if err == nil {
				t.Errorf("%s/%s: expected a runtime error", c.name, b.label)
				continue
			}
			msg := err.Error()
			if !strings.Contains(msg, "undefined: "+c.name) || !strings.Contains(msg, c.want) {
				t.Errorf("%s/%s: error %q should say 'undefined: %s' and point to %q", c.name, b.label, msg, c.name, c.want)
			}
		}
	}
}
