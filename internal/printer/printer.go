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
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/anafalanx/drang/internal/ast"
	"github.com/anafalanx/drang/internal/lexer"
	"github.com/anafalanx/drang/internal/parser"
	"github.com/anafalanx/drang/internal/token"
)

// Format parses drang source, reprints it canonically with comments preserved, and
// verifies (the drop-guard) that the output carries exactly the same comments as the
// input. It errors on a parse failure, on output that fails to re-parse, or if a comment
// would be dropped or altered — so callers never write corrupted output.
func Format(src string) (string, error) { return format(src, false) }

// FormatFix is Format plus the migration rewrites (Fix) applied before printing — the
// `drang fmt --fix` path (drang's edition mechanism).
func FormatFix(src string) (string, error) { return format(src, true) }

func format(src string, fix bool) (string, error) {
	p := parser.New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		return "", fmt.Errorf("parse error: %s", strings.Join(errs, "; "))
	}
	if fix {
		Fix(prog)
	}
	in := p.Comments()
	out := Program(prog, in)

	check := parser.New(out)
	check.ParseProgram()
	if errs := check.Errors(); len(errs) > 0 {
		return "", fmt.Errorf("formatter produced invalid drang: %s", strings.Join(errs, "; "))
	}
	if err := sameComments(in, check.Comments()); err != nil {
		return "", err
	}
	return out, nil
}

// Program renders a program to formatted drang source (ending in a single newline, or ""
// for an empty program), weaving the given comments back in by source position.
func Program(prog *ast.Program, comments []lexer.Comment) string {
	cs := append([]lexer.Comment(nil), comments...)
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].Line != cs[j].Line {
			return cs[i].Line < cs[j].Line
		}
		return cs[i].Col < cs[j].Col
	})
	p := &printer{comments: cs}
	p.stmts(prog.Stmts, 1<<30)
	return p.b.String()
}

// sameComments reports whether two comment lists carry the same multiset of (trimmed)
// texts — the formatter's drop-guard against losing or inventing comments.
func sameComments(in, out []lexer.Comment) error {
	count := map[string]int{}
	for _, c := range in {
		count[strings.TrimRight(c.Text, " \t")]++
	}
	for _, c := range out {
		count[strings.TrimRight(c.Text, " \t")]--
	}
	for text, n := range count {
		if n > 0 {
			return fmt.Errorf("formatter dropped a comment: %q", text)
		}
		if n < 0 {
			return fmt.Errorf("formatter invented a comment: %q", text)
		}
	}
	return nil
}

// maxWidth is the soft target line width; constructs wider than this wrap.
const maxWidth = 100

type printer struct {
	b        strings.Builder
	indent   int
	col      int  // current output column, for line-width decisions (tabs count as 1)
	oneLine  bool // when set, wrappable constructs render on a single line (no wrapping)
	comments []lexer.Comment
	ci       int // cursor: index of the next not-yet-emitted comment
}

func (p *printer) write(s string) {
	p.b.WriteString(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		p.col = len(s) - i - 1
	} else {
		p.col += len(s)
	}
}

func (p *printer) pad() { p.write(strings.Repeat("\t", p.indent)) }

// fitsOneLine reports whether render's single-line output fits from the current column.
// A render that is inherently multi-line (it contains a block body, hence a newline) is
// treated as fitting: the block already provides the break, so this level must not wrap.
func (p *printer) fitsOneLine(render func(*printer)) bool {
	if p.oneLine {
		return true
	}
	sub := &printer{oneLine: true}
	render(sub)
	s := sub.b.String()
	if strings.Contains(s, "\n") {
		return true
	}
	return p.col+len(s) <= maxWidth
}

func (p *printer) inOneLine(f func()) {
	saved := p.oneLine
	p.oneLine = true
	f()
	p.oneLine = saved
}

// flushBefore emits, at the current indent, each pending comment that starts before
// source line `line` (leading / floating comments), one per line.
func (p *printer) flushBefore(line int) {
	for p.ci < len(p.comments) && p.comments[p.ci].Line < line {
		p.pad()
		p.write(strings.TrimRight(p.comments[p.ci].Text, " \t"))
		p.write("\n")
		p.ci++
	}
}

// trailingOn consumes and returns a pending comment on source line `line` (a same-line
// trailing comment), if any.
func (p *printer) trailingOn(line int) (string, bool) {
	if line > 0 && p.ci < len(p.comments) && p.comments[p.ci].Line == line {
		t := strings.TrimRight(p.comments[p.ci].Text, " \t")
		p.ci++
		return t, true
	}
	return "", false
}

func (p *printer) pendingBefore(line int) bool {
	return p.ci < len(p.comments) && p.comments[p.ci].Line < line
}

// stmts writes each statement on its own indented line, weaving in comments by source
// position: leading/floating comments before a statement, then the statement, then a
// same-line trailing comment. `limit` is the source line that bounds this scope (the
// closing-brace line, or a sentinel at top level) so trailing/dangling comments before a
// `}` are flushed at this indent. At the top level a blank line separates each function /
// BEGIN-END block from its neighbors.
func (p *printer) stmts(list []ast.Stmt, limit int) {
	for i, s := range list {
		if i > 0 && p.indent == 0 && (standsAlone(s) || standsAlone(list[i-1])) {
			p.write("\n")
		}
		p.flushBefore(nodeLine(s))
		p.pad()
		p.stmt(s)
		if t, ok := p.trailingOn(endLine(s)); ok {
			p.write("  " + t)
		}
		p.write("\n")
	}
	p.flushBefore(limit)
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
// level, and "}" padded to the current indent. An empty body collapses to "{}" unless it
// holds dangling comments. Comments inside the braces are flushed up to the closing-brace
// line (b.Rbrace).
func (p *printer) block(b *ast.Block) {
	if len(b.Stmts) == 0 {
		if b.Rbrace > 0 && p.pendingBefore(b.Rbrace) {
			p.write("{\n")
			p.indent++
			p.flushBefore(b.Rbrace)
			p.indent--
			p.pad()
			p.write("}")
			return
		}
		p.write("{}")
		return
	}
	p.write("{\n")
	p.indent++
	p.stmts(b.Stmts, blockLimit(b))
	p.indent--
	p.pad()
	p.write("}")
}

func blockLimit(b *ast.Block) int {
	if b.Rbrace > 0 {
		return b.Rbrace
	}
	return 0 // synthesized block (not reached via block()); flush nothing extra
}

// nodeLine is a statement's source start line (0 if unset).
func nodeLine(s ast.Stmt) int {
	if l, ok := s.(interface{ Loc() (int, int) }); ok {
		line, _ := l.Loc()
		return line
	}
	return 0
}

// endLine is the last source line a statement occupies — its closing-brace line for
// block-bearing forms, else its start line — used to attach same-line trailing comments.
func endLine(s ast.Stmt) int {
	switch n := s.(type) {
	case *ast.FnDecl:
		return blockEnd(n.Body, nodeLine(s))
	case *ast.SpecialBlock:
		return blockEnd(n.Body, nodeLine(s))
	case *ast.IfStmt:
		if n.Postfix != 0 {
			return nodeLine(s)
		}
		switch e := n.Else.(type) {
		case *ast.Block:
			return blockEnd(e, nodeLine(s))
		case *ast.IfStmt:
			return endLine(e)
		}
		return blockEnd(n.Then, nodeLine(s))
	case *ast.WhileStmt:
		if n.Postfix != 0 {
			return nodeLine(s)
		}
		return blockEnd(n.Body, nodeLine(s))
	case *ast.ForStmt:
		if n.Postfix != 0 {
			return nodeLine(s)
		}
		return blockEnd(n.Body, nodeLine(s))
	}
	return nodeLine(s)
}

func blockEnd(b *ast.Block, fallback int) int {
	if b != nil && b.Rbrace > 0 {
		return b.Rbrace
	}
	return fallback
}

// stmt writes a single statement's content (no leading indent, no trailing newline).
func (p *printer) stmt(s ast.Stmt) {
	switch n := s.(type) {
	case *ast.ExprStmt:
		// A statement that begins with `{` would be read as a block, so a leading map
		// literal must be parenthesized.
		p.operand(n.X, startsWithBrace(n.X))
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

func (p *printer) callArgs(args []ast.Expr) { p.list("(", ")", args) }

// list emits a delimited expression list (call args or an array literal): single-line
// when it fits, otherwise one element per line (no trailing comma — drang calls reject
// it), with the closing delimiter dedented to this indent. Newlines inside ( and [ are
// insignificant, so the wrapped form re-parses.
func (p *printer) list(open, close string, items []ast.Expr) {
	if len(items) == 0 {
		p.write(open + close)
		return
	}
	render := func(q *printer) {
		q.write(open)
		for i, it := range items {
			if i > 0 {
				q.write(", ")
			}
			q.expr(it)
		}
		q.write(close)
	}
	if p.fitsOneLine(render) {
		p.inOneLine(func() { render(p) })
		return
	}
	p.write(open + "\n")
	p.indent++
	for i, it := range items {
		p.pad()
		p.expr(it)
		if i < len(items)-1 {
			p.write(",")
		}
		p.write("\n")
	}
	p.indent--
	p.pad()
	p.write(close)
}

// mapLit emits a map literal, wrapping one entry per line when it doesn't fit.
func (p *printer) mapLit(n *ast.MapLit) {
	if len(n.Keys) == 0 {
		p.write("{}")
		return
	}
	render := func(q *printer) {
		q.write("{")
		for i := range n.Keys {
			if i > 0 {
				q.write(", ")
			}
			q.expr(n.Keys[i])
			q.write(": ")
			q.expr(n.Vals[i])
		}
		q.write("}")
	}
	if p.fitsOneLine(render) {
		p.inOneLine(func() { render(p) })
		return
	}
	p.write("{\n")
	p.indent++
	for i := range n.Keys {
		p.pad()
		p.expr(n.Keys[i])
		p.write(": ")
		p.expr(n.Vals[i])
		if i < len(n.Keys)-1 {
			p.write(",")
		}
		p.write("\n")
	}
	p.indent--
	p.pad()
	p.write("}")
}

// pipe emits a |> pipeline: one line when it fits, otherwise broken at every stage with
// a trailing |> (leading |> would terminate the statement; a trailing operator continues
// the line), each stage indented one level.
func (p *printer) pipe(n *ast.Pipe) {
	if p.fitsOneLine(func(q *printer) { q.pipeOneLine(n) }) {
		p.inOneLine(func() { p.pipeOneLine(n) })
		return
	}
	head, stages := flattenPipe(n)
	p.operand(head, prec(head) < pPipeline)
	p.write(" |>")
	p.indent++
	for i, c := range stages {
		p.write("\n")
		p.pad()
		p.operand(c.Callee, prec(c.Callee) < pCall)
		if len(c.Args) > 0 {
			p.callArgs(c.Args)
		}
		if i < len(stages)-1 {
			p.write(" |>")
		}
	}
	p.indent--
}

func (p *printer) pipeOneLine(n *ast.Pipe) {
	p.operand(n.Lhs, prec(n.Lhs) < pPipeline)
	p.write(" |> ")
	p.operand(n.Call.Callee, prec(n.Call.Callee) < pCall)
	if len(n.Call.Args) > 0 {
		p.callArgs(n.Call.Args)
	}
}

// flattenPipe unwinds a left-nested pipe chain into its head expression and ordered call
// stages.
func flattenPipe(n *ast.Pipe) (ast.Expr, []*ast.Call) {
	var calls []*ast.Call
	cur := ast.Expr(n)
	for {
		pp, ok := cur.(*ast.Pipe)
		if !ok {
			break
		}
		calls = append(calls, pp.Call)
		cur = pp.Lhs
	}
	for i, j := 0, len(calls)-1; i < j; i, j = i+1, j-1 {
		calls[i], calls[j] = calls[j], calls[i]
	}
	return cur, calls
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
		p.pipe(n)
	case *ast.Call:
		p.operand(n.Callee, prec(n.Callee) < pCall)
		p.callArgs(n.Args)
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
			p.list("[", "]", n.Elems)
		}
	case *ast.MapLit:
		p.mapLit(n)
	case *ast.Lambda:
		p.write("|")
		p.params(n.Params, n.Defaults)
		p.write("|")
		if es, ok := soleExprStmt(n.Body); ok {
			// |x| {..} would parse as a block body, so a leading map literal needs parens.
			p.write(" ")
			p.operand(es.X, startsWithBrace(es.X))
		} else {
			p.write(" ")
			p.block(n.Body)
		}
	default:
		// Unknown expression: debug form rather than dropping it.
		p.write(e.String())
	}
}

// startsWithBrace reports whether an expression's printed form begins with `{` (a map
// literal as its leftmost leaf). Such an expression must be parenthesized at statement
// start or as a lambda expression-body, where a leading `{` would otherwise be read as a
// block.
func startsWithBrace(e ast.Expr) bool {
	switch n := e.(type) {
	case *ast.MapLit:
		return true
	case *ast.Binary:
		return startsWithBrace(n.L)
	case *ast.Logical:
		return startsWithBrace(n.L)
	case *ast.DefOr:
		return startsWithBrace(n.X)
	case *ast.RangeLit:
		return startsWithBrace(n.Lo)
	case *ast.Pipe:
		return startsWithBrace(n.Lhs)
	case *ast.Call:
		return startsWithBrace(n.Callee)
	case *ast.Index:
		return startsWithBrace(n.X)
	case *ast.Field:
		return startsWithBrace(n.X)
	case *ast.Propagate:
		return startsWithBrace(n.X)
	}
	return false
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
