// Package printer renders a drang AST back to canonical source (the backend of
// `drang fmt`). It is gofmt-spirit: opinionated, deterministic, and idempotent. It
// relies on the provenance the parser records — Raw on literals, faithful Pipe/Interp
// nodes, and the postfix/qw flags — to reprint the author's surface forms.
//
// Step 3 emits canonical statement layout (one per line, tab-indented, braced blocks
// always multiline) with precedence-driven minimal parentheses, but does NOT yet wrap
// long lines or weave in comments — those are later steps.
package printer

import (
	"strconv"
	"strings"

	"github.com/anafalanx/drang/internal/ast"
	"github.com/anafalanx/drang/internal/token"
)

// Program renders a whole program to formatted drang source (ending in a single
// newline, or "" for an empty program).
func Program(prog *ast.Program) string {
	p := &printer{}
	p.stmts(prog.Stmts)
	return p.b.String()
}

type printer struct {
	b      strings.Builder
	indent int
}

func (p *printer) write(s string) { p.b.WriteString(s) }
func (p *printer) pad()           { p.b.WriteString(strings.Repeat("\t", p.indent)) }

// stmts writes each statement on its own indented line. At the top level a blank line
// separates each function / BEGIN-END block from its neighbors (on both sides).
func (p *printer) stmts(list []ast.Stmt) {
	for i, s := range list {
		if i > 0 && p.indent == 0 && (standsAlone(s) || standsAlone(list[i-1])) {
			p.write("\n")
		}
		p.pad()
		p.stmt(s)
		p.write("\n")
	}
}

// standsAlone reports whether a top-level statement should be set off by blank lines.
func standsAlone(s ast.Stmt) bool {
	switch s.(type) {
	case *ast.FnDecl, *ast.SpecialBlock:
		return true
	}
	return false
}

// block writes a brace-delimited body: "{" on the current line, statements indented one
// level, and "}" padded to the current indent. An empty body collapses to "{}".
func (p *printer) block(b *ast.Block) {
	if len(b.Stmts) == 0 {
		p.write("{}")
		return
	}
	p.write("{\n")
	p.indent++
	p.stmts(b.Stmts)
	p.indent--
	p.pad()
	p.write("}")
}

// stmt writes a single statement's content (no leading indent, no trailing newline).
func (p *printer) stmt(s ast.Stmt) {
	switch n := s.(type) {
	case *ast.ExprStmt:
		p.expr(n.X)
	case *ast.DeclStmt:
		op := ":="
		if n.Const {
			op = "::="
		}
		p.write("$" + n.Name + " " + op + " ")
		p.expr(n.Value)
	case *ast.AssignStmt:
		p.expr(n.Target)
		if n.Op != token.ILLEGAL {
			p.write(" " + oper(n.Op) + "= ")
		} else {
			p.write(" = ")
		}
		p.expr(n.Value)
	case *ast.IfStmt:
		p.ifStmt(n)
	case *ast.WhileStmt:
		p.whileStmt(n)
	case *ast.ForStmt:
		p.forStmt(n)
	case *ast.FnDecl:
		p.write("fn " + n.Name + "(")
		p.params(n.Params, n.Defaults)
		p.write(") ")
		p.block(n.Body)
	case *ast.ReturnStmt:
		if n.Value == nil {
			p.write("return")
		} else {
			p.write("return ")
			p.expr(n.Value)
		}
	case *ast.BreakStmt:
		p.write("break")
	case *ast.NextStmt:
		p.write("next")
	case *ast.UseStmt:
		p.write("use ")
		p.expr(n.Path)
	case *ast.ExampleStmt:
		p.write("example ")
		p.expr(n.Subject)
		switch {
		case n.Fails:
			p.write(" fails")
		case n.Want != nil:
			p.write(" == ")
			p.expr(n.Want)
		}
	case *ast.SpecialBlock:
		p.write(n.Name + " ")
		p.block(n.Body)
	case *ast.Block:
		p.block(n)
	default:
		// Unknown statement: fall back to the debug form rather than dropping it.
		p.write(s.String())
	}
}

func (p *printer) ifStmt(n *ast.IfStmt) {
	if n.Postfix != 0 {
		p.stmt(n.Then.Stmts[0])
		if n.Postfix == token.UNLESS {
			p.write(" unless ")
			p.expr(stripNot(n.Cond))
		} else {
			p.write(" if ")
			p.expr(n.Cond)
		}
		return
	}
	p.write("if ")
	p.expr(n.Cond)
	p.write(" ")
	p.block(n.Then)
	switch e := n.Else.(type) {
	case *ast.IfStmt:
		p.write(" else ")
		p.ifStmt(e)
	case *ast.Block:
		p.write(" else ")
		p.block(e)
	}
}

func (p *printer) whileStmt(n *ast.WhileStmt) {
	if n.Postfix != 0 {
		p.stmt(n.Body.Stmts[0])
		if n.Postfix == token.UNTIL {
			p.write(" until ")
			p.expr(stripNot(n.Cond))
		} else {
			p.write(" while ")
			p.expr(n.Cond)
		}
		return
	}
	p.write("while ")
	p.expr(n.Cond)
	p.write(" ")
	p.block(n.Body)
}

func (p *printer) forStmt(n *ast.ForStmt) {
	if n.Postfix != 0 {
		p.stmt(n.Body.Stmts[0])
		p.write(" for ")
		p.expr(n.Iter)
		return
	}
	p.write("for ")
	for i, v := range n.Vars {
		if i > 0 {
			p.write(", ")
		}
		p.write("$" + v)
	}
	p.write(" in ")
	p.expr(n.Iter)
	p.write(" ")
	p.block(n.Body)
}

// stripNot returns the un-negated condition of a postfix unless/until (the parser stores
// notExpr(cond) = a single Unary{BANG}); it returns e unchanged if not that shape.
func stripNot(e ast.Expr) ast.Expr {
	if u, ok := e.(*ast.Unary); ok && u.Op == token.BANG {
		return u.X
	}
	return e
}

func (p *printer) params(names []string, defaults []ast.Expr) {
	for i, name := range names {
		if i > 0 {
			p.write(", ")
		}
		p.write("$" + name)
		if i < len(defaults) && defaults[i] != nil {
			p.write(" = ")
			p.expr(defaults[i])
		}
	}
}

func (p *printer) args(list []ast.Expr) {
	for i, a := range list {
		if i > 0 {
			p.write(", ")
		}
		p.expr(a)
	}
}

// operand writes e, wrapping it in parentheses when needParen.
func (p *printer) operand(e ast.Expr, needParen bool) {
	if needParen {
		p.write("(")
		p.expr(e)
		p.write(")")
	} else {
		p.expr(e)
	}
}

// expr writes an expression (single line; a block-bodied lambda is the one form that
// spans lines, using the current indent).
func (p *printer) expr(e ast.Expr) {
	switch n := e.(type) {
	case *ast.IntLit:
		p.write(orRaw(n.Raw, func() string { return strconv.FormatInt(n.Value, 10) }))
	case *ast.FloatLit:
		p.write(orRaw(n.Raw, func() string { return strconv.FormatFloat(n.Value, 'g', -1, 64) }))
	case *ast.StringLit:
		p.write(orRaw(n.Raw, func() string { return strconv.Quote(n.Value) }))
	case *ast.Interp:
		if n.Raw != "" {
			p.write(n.Raw)
		} else {
			p.write(interpFallback(n))
		}
	case *ast.RegexLit:
		p.write(orRaw(n.Raw, func() string { return regexFallback(n.Pattern) }))
	case *ast.BoolLit:
		if n.Value {
			p.write("true")
		} else {
			p.write("false")
		}
	case *ast.Var:
		p.write("$" + n.Name)
	case *ast.Ident:
		p.write(n.Name)
	case *ast.Unary:
		p.write(oper(n.Op))
		needParen := prec(n.X) < pPrefix
		if u, ok := n.X.(*ast.Unary); ok && n.Op == token.MINUS && u.Op == token.MINUS {
			needParen = true // avoid "--"
		}
		p.operand(n.X, needParen)
	case *ast.Binary:
		lvl := precOf(n.Op)
		p.operand(n.L, prec(n.L) < lvl)
		p.write(" " + oper(n.Op) + " ")
		p.operand(n.R, prec(n.R) <= lvl)
	case *ast.Logical:
		lvl := precOf(n.Op)
		p.operand(n.L, prec(n.L) < lvl)
		p.write(" " + oper(n.Op) + " ")
		p.operand(n.R, prec(n.R) <= lvl)
	case *ast.DefOr:
		p.operand(n.X, prec(n.X) < pOrElse)
		p.write(" // ")
		p.operand(n.Fallback, prec(n.Fallback) <= pOrElse)
	case *ast.RangeLit:
		p.operand(n.Lo, prec(n.Lo) < pRange)
		p.write("..")
		p.operand(n.Hi, prec(n.Hi) <= pRange)
	case *ast.Pipe:
		p.operand(n.Lhs, prec(n.Lhs) < pPipeline)
		p.write(" |> ")
		p.operand(n.Call.Callee, prec(n.Call.Callee) < pCall)
		if len(n.Call.Args) > 0 {
			p.write("(")
			p.args(n.Call.Args)
			p.write(")")
		}
	case *ast.Call:
		p.operand(n.Callee, prec(n.Callee) < pCall)
		p.write("(")
		p.args(n.Args)
		p.write(")")
	case *ast.Index:
		p.operand(n.X, prec(n.X) < pCall)
		p.write("[")
		p.expr(n.Idx)
		p.write("]")
	case *ast.Field:
		p.operand(n.X, prec(n.X) < pCall)
		p.write("." + n.Name)
	case *ast.Propagate:
		p.operand(n.X, prec(n.X) < pCall)
		p.write("?")
	case *ast.ArrayLit:
		if n.Qw && n.Raw != "" {
			p.write(n.Raw)
		} else {
			p.write("[")
			p.args(n.Elems)
			p.write("]")
		}
	case *ast.MapLit:
		p.write("{")
		for i := range n.Keys {
			if i > 0 {
				p.write(", ")
			}
			p.expr(n.Keys[i])
			p.write(": ")
			p.expr(n.Vals[i])
		}
		p.write("}")
	case *ast.Lambda:
		p.write("|")
		p.params(n.Params, n.Defaults)
		p.write("|")
		if es, ok := soleExprStmt(n.Body); ok {
			p.write(" ")
			p.expr(es.X)
		} else {
			p.write(" ")
			p.block(n.Body)
		}
	default:
		// Unknown expression: debug form rather than dropping it.
		p.write(e.String())
	}
}

// soleExprStmt reports whether a block is a single expression statement (an
// expression-bodied lambda), so it can be reprinted as |..| expr rather than |..| {..}.
func soleExprStmt(b *ast.Block) (*ast.ExprStmt, bool) {
	if len(b.Stmts) == 1 {
		if es, ok := b.Stmts[0].(*ast.ExprStmt); ok {
			return es, true
		}
	}
	return nil, false
}

func orRaw(raw string, fallback func() string) string {
	if raw != "" {
		return raw
	}
	return fallback()
}

// interpFallback reconstructs a double-quoted interpolated string from Parts, for the
// rare case of a synthesized Interp with no Raw.
func interpFallback(n *ast.Interp) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, part := range n.Parts {
		switch e := part.(type) {
		case *ast.StringLit:
			s := strconv.Quote(e.Value)
			b.WriteString(s[1 : len(s)-1]) // drop the surrounding quotes, keep escapes
		case *ast.Var:
			b.WriteString("${" + e.Name + "}")
		default:
			b.WriteString("${")
			b.WriteString(e.String())
			b.WriteString("}")
		}
	}
	b.WriteByte('"')
	return b.String()
}

// regexFallback emits a canonical qr/.../ for a synthesized RegexLit with no Raw,
// choosing a delimiter the pattern does not contain.
func regexFallback(pattern string) string {
	for _, d := range []string{"/", "|", "#", "!", "~"} {
		if !strings.Contains(pattern, d) {
			return "qr" + d + pattern + d
		}
	}
	return "qr{" + pattern + "}"
}

// precedence levels, mirroring the parser (lowest to highest). pAtom is above pCall so
// literals/vars/calls are never parenthesized as operands.
const (
	pLowest = iota
	pBoolOr
	pBoolAnd
	pOrElse
	pPipeline
	pEquals
	pLessGreater
	pRange
	pSum
	pProduct
	pPrefix
	pCall
	pAtom
)

func precOf(k token.Kind) int {
	switch k {
	case token.OR:
		return pBoolOr
	case token.AND:
		return pBoolAnd
	case token.EQ, token.NE, token.SPACESHIP:
		return pEquals
	case token.LT, token.LE, token.GT, token.GE:
		return pLessGreater
	case token.PLUS, token.MINUS, token.TILDE:
		return pSum
	case token.STAR, token.SLASH, token.PERCENT:
		return pProduct
	}
	return pLowest
}

// prec returns the binding precedence of an expression's top operator.
func prec(e ast.Expr) int {
	switch n := e.(type) {
	case *ast.Logical:
		return precOf(n.Op)
	case *ast.Binary:
		return precOf(n.Op)
	case *ast.DefOr:
		return pOrElse
	case *ast.Pipe:
		return pPipeline
	case *ast.RangeLit:
		return pRange
	case *ast.Unary:
		return pPrefix
	case *ast.Call, *ast.Index, *ast.Field, *ast.Propagate:
		return pCall
	case *ast.Lambda:
		return pLowest // a lambda as an operator operand always needs parentheses
	default:
		return pAtom
	}
}

// oper returns the source spelling of an operator token.
func oper(k token.Kind) string {
	switch k {
	case token.PLUS:
		return "+"
	case token.MINUS:
		return "-"
	case token.STAR:
		return "*"
	case token.SLASH:
		return "/"
	case token.PERCENT:
		return "%"
	case token.TILDE:
		return "~"
	case token.EQ:
		return "=="
	case token.NE:
		return "!="
	case token.LT:
		return "<"
	case token.LE:
		return "<="
	case token.GT:
		return ">"
	case token.GE:
		return ">="
	case token.SPACESHIP:
		return "<=>"
	case token.BANG:
		return "!"
	case token.AND:
		return "and"
	case token.OR:
		return "or"
	case token.DEFOR:
		return "//"
	case token.DOTDOT:
		return ".."
	}
	return k.String()
}
