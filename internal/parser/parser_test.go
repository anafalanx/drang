package parser

import (
	"testing"

	"github.com/anafalanx/drang/internal/ast"
	"github.com/anafalanx/drang/internal/token"
)

func soleStmt(t *testing.T, src string) ast.Stmt {
	t.Helper()
	p := New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse %q: %v", src, errs)
	}
	if len(prog.Stmts) != 1 {
		t.Fatalf("%q: want 1 stmt, got %d", src, len(prog.Stmts))
	}
	return prog.Stmts[0]
}

// TestPostfixProvenance verifies step-2d: postfix modifiers record the keyword so the
// formatter can reprint  stmt if c  rather than the desugared block form, while block
// forms stay Postfix==0.
func TestPostfixProvenance(t *testing.T) {
	if s, ok := soleStmt(t, `say(1) if x`).(*ast.IfStmt); !ok || s.Postfix != token.IF {
		t.Errorf("postfix if: %#v", soleStmt(t, `say(1) if x`))
	}
	if s, ok := soleStmt(t, `say(1) unless x`).(*ast.IfStmt); !ok || s.Postfix != token.UNLESS {
		t.Errorf("postfix unless: %#v", soleStmt(t, `say(1) unless x`))
	}
	if s, ok := soleStmt(t, `if x { say(1) }`).(*ast.IfStmt); !ok || s.Postfix != 0 {
		t.Errorf("block if must not be postfix: %#v", soleStmt(t, `if x { say(1) }`))
	}
	if s, ok := soleStmt(t, `say(1) while x`).(*ast.WhileStmt); !ok || s.Postfix != token.WHILE {
		t.Errorf("postfix while: %#v", soleStmt(t, `say(1) while x`))
	}
	if s, ok := soleStmt(t, `say(1) until x`).(*ast.WhileStmt); !ok || s.Postfix != token.UNTIL {
		t.Errorf("postfix until: %#v", soleStmt(t, `say(1) until x`))
	}
	if s, ok := soleStmt(t, `say($x) for xs`).(*ast.ForStmt); !ok || s.Postfix != token.FOR {
		t.Errorf("postfix for: %#v", soleStmt(t, `say($x) for xs`))
	}
}

// TestQwProvenance verifies a qw{...} list keeps the Qw marker + verbatim Raw while
// still evaluating as an ordinary array of strings.
func TestQwProvenance(t *testing.T) {
	e := soleExpr(t, `qw{a b c}`)
	a, ok := e.(*ast.ArrayLit)
	if !ok || !a.Qw || a.Raw != "qw{a b c}" || len(a.Elems) != 3 {
		t.Errorf("qw: %#v", e)
	}
}

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

// TestPipeRHSMustBeCallable (pre-0.4 hardening, Bug 5): the loose pipeline precedence
// lets  5 |> f() == 5  capture the operator into the RHS; that must be a clear PARSE
// error, not a nonsensical arg-less call that only fails at runtime. Bare callables, a
// trailing ?, and an explicitly parenthesized pipe still parse.
func TestPipeRHSMustBeCallable(t *testing.T) {
	bad := []string{
		`5 |> f() == 5`,
		`5 |> f() + 1`,
		`5 |> 3`,
		`5 |> "x"`,
		`5 |> a ~ b`,
	}
	for _, src := range bad {
		p := New(src)
		p.ParseProgram()
		if len(p.Errors()) == 0 {
			t.Errorf("%q: expected a parse error for a non-callable |> RHS", src)
		}
	}
	good := []string{
		`5 |> f()`,
		`5 |> f`,
		`5 |> obj.m()`,
		`5 |> $g`,
		`5 |> fns[0]`,
		`5 |> f()?`,
		`(5 |> f()) == 5`,
	}
	for _, src := range good {
		p := New(src)
		p.ParseProgram()
		if errs := p.Errors(); len(errs) > 0 {
			t.Errorf("%q: unexpected parse error: %v", src, errs)
		}
	}
}

// TestNulByteRejected (pre-0.4 hardening, Bug 7): an embedded 0x00 byte must be reported
// (the EOF sentinel is also 0), not silently truncate the program.
func TestNulByteRejected(t *testing.T) {
	p := New("say(1)\x00say(2)")
	p.ParseProgram()
	if len(p.Errors()) == 0 {
		t.Error("an embedded NUL byte must be a parse error, not silent truncation")
	}
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
