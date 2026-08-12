package parser

import (
	"strings"
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

// TestExportMarker: `export` before a top-level fn / `::=` constant sets Exported;
// bad placements are parse errors so the marker can never silently mean nothing.
func TestExportMarker(t *testing.T) {
	good := map[string]string{
		`export fn .f() { 1 }`: "(export fn .f () (block 1))",
		`export $C ::= 5`:      "(export ::= $C 5)",
		`fn .f() { 1 }`:        "(fn .f () (block 1))",
		`$C ::= 5`:             "(::= $C 5)",
		`export fn .f() { 1 }
export $C ::= 5`: "(export fn .f () (block 1))\n(export ::= $C 5)",
	}
	for src, want := range good {
		p := New(src)
		prog := p.ParseProgram()
		if errs := p.Errors(); len(errs) > 0 {
			t.Errorf("%q: unexpected parse error: %v", src, errs)
			continue
		}
		if got := prog.String(); got != want {
			t.Errorf("%q: got %q, want %q", src, got, want)
		}
	}
	bad := map[string]string{
		`export $x := 5`:                "mutable",             // cannot mark a mutable variable
		`fn .f() { export fn .g() {} }`: "top level",           // nested in a function
		`if true { export $C ::= 1 }`:   "top level",           // nested in a block
		`export $x`:                     "must be followed by", // no ::= after the var
		`export $x + 1`:                 "must be followed by", // an expression, not a decl
	}
	for src, wantSub := range bad {
		p := New(src)
		p.ParseProgram()
		errs := p.Errors()
		if len(errs) == 0 {
			t.Errorf("%q: expected a parse error", src)
			continue
		}
		if !strings.Contains(strings.Join(errs, "; "), wantSub) {
			t.Errorf("%q: error %v should mention %q", src, errs, wantSub)
		}
	}
	// `export` not followed by fn/$ stays an ordinary identifier (a builtin-name
	// expression), so existing code using none of this parses unchanged.
	p := New(`say(1)`)
	p.ParseProgram()
	if len(p.Errors()) != 0 {
		t.Errorf("plain program: unexpected errors %v", p.Errors())
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

func TestParserComplexityLimits(t *testing.T) {
	tests := []string{
		strings.Repeat("!", maxParseDepth+32) + "true",
		strings.Repeat("[", maxParseDepth+32) + "0" + strings.Repeat("]", maxParseDepth+32),
		strings.Repeat("if true {", maxParseDepth+32) + "1" + strings.Repeat("}", maxParseDepth+32),
	}
	for i, src := range tests {
		p := New(src)
		p.ParseProgram()
		if got := strings.Join(p.Errors(), "; "); !strings.Contains(got, "source is too complex") {
			t.Errorf("case %d: expected a bounded-complexity diagnostic, got %q", i, got)
		}
	}

	// Exercise the flat-node budget without constructing a million-node fixture.
	p := New("1")
	p.budget.nodes = maxParseNodes
	p.ParseProgram()
	if got := strings.Join(p.Errors(), "; "); !strings.Contains(got, "syntax nodes") {
		t.Fatalf("node-budget diagnostic = %q", got)
	}
}

func TestParserStopsScanningAfterComplexityFailure(t *testing.T) {
	// This source contains far more opening delimiters than the parser accepts.
	// Once the depth diagnostic fires, parsing must stop instead of tokenizing the
	// remaining million delimiters into the lexer's bracket stack.
	p := New(strings.Repeat("[", 1<<20) + "0")
	p.ParseProgram()
	if got := strings.Join(p.Errors(), "; "); !strings.Contains(got, "source is too complex") {
		t.Fatalf("complexity diagnostic = %q", got)
	}
	if p.peek.Col > maxParseDepth+16 {
		t.Fatalf("lexer kept scanning after parser budget failure: lookahead reached column %d", p.peek.Col)
	}
}

func TestElseIfNestingCeiling(t *testing.T) {
	chain := func(levels int, tail string) string {
		var b strings.Builder
		for range levels - 1 {
			b.WriteString("if true {} else ")
		}
		b.WriteString("if true {}")
		b.WriteString(tail)
		return b.String()
	}

	// The documented recursive-parser ceiling itself remains legal.
	p := New(chain(maxParseDepth, ""))
	p.ParseProgram()
	if errs := p.Errors(); len(errs) != 0 {
		t.Fatalf("%d-level else-if chain: unexpected errors %v", maxParseDepth, errs)
	}

	// The next branch is rejected before parseIf makes another recursive Go call.
	p = New(chain(maxParseDepth+1, ""))
	p.ParseProgram()
	if got := strings.Join(p.Errors(), "; "); !strings.Contains(got, "else-if nesting exceeds the limit") {
		t.Fatalf("overflow diagnostic = %q", got)
	}
}

func TestElseIfComplexityFailureStopsScanning(t *testing.T) {
	var b strings.Builder
	for range maxParseDepth {
		b.WriteString("if true {} else ")
	}
	b.WriteString("if ")
	suffixStart := b.Len()
	b.WriteString(strings.Repeat("[", 4096))
	b.WriteByte('0')

	p := New(b.String())
	p.ParseProgram()
	if got := strings.Join(p.Errors(), "; "); !strings.Contains(got, "else-if nesting exceeds the limit") {
		t.Fatalf("overflow diagnostic = %q", got)
	}
	if !p.budget.failed {
		t.Fatal("else-if complexity failure was not terminal")
	}
	if p.peek.Col > suffixStart+2 {
		t.Fatalf("lexer kept scanning the adversarial suffix: lookahead reached column %d, suffix starts at %d", p.peek.Col, suffixStart)
	}
}

func TestAggregateSyntaxUsesSharedBudget(t *testing.T) {
	tests := []string{
		`$"${1}${2}"`,
		`qw{one two}`,
		`|$a, $b| 1`,
	}
	for _, src := range tests {
		p := New(src)
		p.budget.nodes = maxParseNodes - 2
		p.ParseProgram()
		if got := strings.Join(p.Errors(), "; "); !strings.Contains(got, "syntax nodes") {
			t.Errorf("%q: expected shared node-budget diagnostic, got %q", src, got)
		}
	}
}

func TestInterpolationSubparserInheritsExpressionDepth(t *testing.T) {
	p := New(`$"${1}"`)
	p.exprDepth = maxParseDepth - 1
	p.ParseProgram()
	if got := strings.Join(p.Errors(), "; "); !strings.Contains(got, "expression nesting") {
		t.Fatalf("nested interpolation depth diagnostic = %q", got)
	}
}

func TestInterpolationPositionsUseOuterSource(t *testing.T) {
	src := `1
$"first ${read_file(q{a})} second ${|$x| { read_file(q{b}) }}"
<<~$TXT
    third ${read_file("c")}
    fourth ${read_file("d")}
TXT`
	p := New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatal(errs)
	}
	var got [][2]int
	var visitExpr func(ast.Expr)
	visitExpr = func(e ast.Expr) {
		switch n := e.(type) {
		case *ast.Call:
			if id, ok := n.Callee.(*ast.Ident); ok && id.Name == "read_file" {
				line, col := n.Loc()
				got = append(got, [2]int{line, col})
			}
			for _, arg := range n.Args {
				visitExpr(arg)
			}
		case *ast.Interp:
			for _, part := range n.Parts {
				visitExpr(part)
			}
		case *ast.Lambda:
			for _, stmt := range n.Body.Stmts {
				if es, ok := stmt.(*ast.ExprStmt); ok {
					visitExpr(es.X)
				}
			}
		}
	}
	for _, stmt := range prog.Stmts {
		if es, ok := stmt.(*ast.ExprStmt); ok {
			visitExpr(es.X)
		}
	}
	want := [][2]int{{2, 11}, {2, 44}, {4, 13}, {5, 14}}
	if len(got) != len(want) {
		t.Fatalf("positions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d = %v, want %v", i, got[i], want[i])
		}
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
