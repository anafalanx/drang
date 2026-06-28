package parser

import (
	"testing"

	"github.com/anafalanx/drang/internal/ast"
)

func soleExpr(t *testing.T, src string) ast.Expr {
	t.Helper()
	p := New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse %q: %v", src, errs)
	}
	if len(prog.Stmts) != 1 {
		t.Fatalf("%q: want 1 stmt, got %d", src, len(prog.Stmts))
	}
	es, ok := prog.Stmts[0].(*ast.ExprStmt)
	if !ok {
		t.Fatalf("%q: stmt is %T, want *ast.ExprStmt", src, prog.Stmts[0])
	}
	return es.X
}

// TestLiteralProvenance verifies step-2b: leaf literals carry the verbatim source
// (Raw) for the formatter, while the decoded eval fields (Value/Pattern) are unchanged.
func TestLiteralProvenance(t *testing.T) {
	if n, ok := soleExpr(t, "007").(*ast.IntLit); !ok || n.Raw != "007" || n.Value != 7 {
		t.Errorf("int 007: %#v", soleExpr(t, "007"))
	}
	if n, ok := soleExpr(t, "1.50").(*ast.FloatLit); !ok || n.Raw != "1.50" || n.Value != 1.5 {
		t.Errorf("float 1.50: %#v", soleExpr(t, "1.50"))
	}

	strs := []struct{ src, raw, val string }{
		{`"hi"`, `"hi"`, "hi"},
		{`"a\tb"`, `"a\tb"`, "a\tb"}, // Raw keeps the backslash; Value decodes it
		{`q{hi}`, `q{hi}`, "hi"},
		{`qq{hi}`, `qq{hi}`, "hi"},
		{`qq[a b]`, `qq[a b]`, "a b"},
	}
	for _, c := range strs {
		got := soleExpr(t, c.src)
		n, ok := got.(*ast.StringLit)
		if !ok {
			t.Errorf("%s: got %T, want *ast.StringLit", c.src, got)
			continue
		}
		if n.Raw != c.raw || n.Value != c.val {
			t.Errorf("%s: Raw=%q Value=%q, want Raw=%q Value=%q", c.src, n.Raw, n.Value, c.raw, c.val)
		}
	}

	// regex: Raw is the verbatim qr/foo/i; Pattern keeps the baked eval form.
	if n, ok := soleExpr(t, `qr/foo/i`).(*ast.RegexLit); !ok || n.Raw != "qr/foo/i" || n.Pattern != "(?i)foo" {
		t.Errorf("regex qr/foo/i: %#v", soleExpr(t, `qr/foo/i`))
	}
}
