// Package parser builds an AST from drang source using a Pratt expression parser.
//
// Statements: declarations ($x := e), assignments ($x = e), expression
// statements, block-form if/else (with else-if) and while, and the postfix
// modifiers if/unless/while/until. Statements are separated by inserted NEWLINE
// terminators or ';'. Calls require parentheses. Collections: array/map/range
// literals, indexing, and block- and postfix-form for-in.
package parser

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/anafalanx/drang/internal/ast"
	"github.com/anafalanx/drang/internal/lexer"
	"github.com/anafalanx/drang/internal/token"
)

// Binding-power levels, lowest to highest.
const (
	lowest = iota
	boolOr
	boolAnd
	orElse
	pipeline
	equals
	lessGreater
	rangelvl
	sum
	product
	prefix
	call
)

func precedence(k token.Kind) int {
	switch k {
	case token.OR:
		return boolOr
	case token.AND:
		return boolAnd
	case token.DEFOR:
		return orElse
	case token.PIPE:
		return pipeline
	case token.EQ, token.NE, token.SPACESHIP:
		return equals
	case token.LT, token.LE, token.GT, token.GE:
		return lessGreater
	case token.DOTDOT:
		return rangelvl
	case token.PLUS, token.MINUS, token.TILDE:
		return sum
	case token.STAR, token.SLASH, token.PERCENT:
		return product
	case token.LPAREN, token.LBRACKET, token.DOT, token.QUESTION:
		return call
	}
	return lowest
}

// Parser turns source text into an *ast.Program. It keeps one token of lookahead.
type Parser struct {
	lex           *lexer.Lexer
	tok           token.Token
	peek          token.Token
	lastBlockForm bool // whether the most recent statement was block-form (ended in }), so a same-line follow-on needs no separator
	stdlibMode    bool // allow bare 'fn name' (the embedded prelude defines bare stdlib functions); user code must use 'fn .name'
	errs          []string
	loopDepth     int // enclosing loop nesting (reset at fn/lambda boundaries) — gates break/next
	blockDepth    int // enclosing { } nesting — `example` is only valid at the top level
}

// New returns a Parser ready to parse src.
func New(src string) *Parser {
	p := &Parser{lex: lexer.New(src)}
	p.next()
	p.next()
	return p
}

// NewStdlib returns a parser for the embedded standard library, where a bare
// 'fn name' declares a (bare) stdlib function. User code must use 'fn .name'.
func NewStdlib(src string) *Parser {
	p := New(src)
	p.stdlibMode = true
	return p
}

func (p *Parser) next() {
	p.tok = p.peek
	p.peek = p.lex.Next()
}

func (p *Parser) errorf(format string, args ...any) {
	p.errs = append(p.errs, fmt.Sprintf("line %d: %s", p.tok.Line, fmt.Sprintf(format, args...)))
}

// Errors returns the parse errors collected during ParseProgram.
func (p *Parser) Errors() []string { return p.errs }

func isTerm(k token.Kind) bool { return k == token.NEWLINE || k == token.SEMI }

func (p *Parser) skipTerms() {
	for isTerm(p.tok.Kind) {
		p.next()
	}
}

// ParseProgram parses the entire input into a Program.
func (p *Parser) ParseProgram() *ast.Program {
	prog := &ast.Program{}
	prog.Stmts = p.parseStmtsUntil(token.EOF)
	return prog
}

// Comments returns the line comments the lexer captured as trivia (complete once
// ParseProgram has consumed the whole input). Used by the formatter to reattach them;
// the token stream and AST are unaffected.
func (p *Parser) Comments() []lexer.Comment { return p.lex.Comments() }

// parseStmtsUntil parses statements until the given closing kind (or EOF).
func (p *Parser) parseStmtsUntil(end token.Kind) []ast.Stmt {
	var stmts []ast.Stmt
	p.skipTerms()
	for p.tok.Kind != end && p.tok.Kind != token.EOF {
		if s := p.parseStmt(); s != nil {
			stmts = append(stmts, s)
		}
		// A block-form statement (if/while/for/fn/BEGIN/END) ends at its closing },
		// which also terminates the statement (perl-style) — so `if c { ... } stmt`
		// and `BEGIN{ ... } stmt` work on one line without an explicit ; or newline.
		// The leniency is scoped to block-form statements: a map literal or lambda
		// body that merely ends in } still requires a separator.
		if p.tok.Kind != end && p.tok.Kind != token.EOF && !isTerm(p.tok.Kind) && !p.lastBlockForm {
			p.errorf("expected end of statement, got %s %q", p.tok.Kind, p.tok.Lit)
			for p.tok.Kind != end && p.tok.Kind != token.EOF && !isTerm(p.tok.Kind) {
				p.next()
			}
		}
		p.skipTerms()
	}
	return stmts
}

func (p *Parser) parseStmt() ast.Stmt {
	pos := p.here() // the statement's first token
	s := p.parseStmtDispatch()
	if s != nil {
		setStmtPos(s, pos)
	}
	return s
}

func (p *Parser) parseStmtDispatch() ast.Stmt {
	// Contextual keywords: a statement-leading BEGIN/END immediately followed by '{'
	// is a one-liner stream block. BEGIN and END stay ordinary identifiers elsewhere.
	// Each branch sets lastBlockForm as its final act (overriding any value a nested
	// block left behind), recording whether THIS statement ended at a block's }.
	if p.tok.Kind == token.IDENT && (p.tok.Lit == "BEGIN" || p.tok.Lit == "END") && p.peek.Kind == token.LBRACE {
		s := p.parseSpecialBlock()
		p.lastBlockForm = true
		return s
	}
	// Contextual: `use "path"` (a string right after `use`) is a flat-merge import
	// directive. `use(...)` (parens) falls through to a normal call — the captured form.
	if p.tok.Kind == token.IDENT && p.tok.Lit == "use" && (p.peek.Kind == token.STRING || p.peek.Kind == token.RAWSTR) {
		return p.parseUse()
	}
	// Contextual: a statement-leading `example` followed by an expression is a
	// `drang test` assertion (a no-op in a normal run). `example` stays an ordinary
	// word when not at statement-lead with an expression after it.
	if p.tok.Kind == token.IDENT && p.tok.Lit == "example" &&
		p.peek.Kind != token.NEWLINE && p.peek.Kind != token.SEMI &&
		p.peek.Kind != token.EOF && p.peek.Kind != token.RBRACE {
		if p.blockDepth > 0 {
			p.errorf("`example` is only valid at the top level, not inside a block or function")
		}
		s := p.parseExample()
		p.lastBlockForm = false
		return s
	}
	switch p.tok.Kind {
	case token.FN:
		s := p.parseFn()
		p.lastBlockForm = true
		return s
	case token.IF:
		s := p.parseIf()
		p.lastBlockForm = true
		return s
	case token.WHILE:
		s := p.parseWhile()
		p.lastBlockForm = true
		return s
	case token.FOR:
		s := p.parseFor()
		p.lastBlockForm = true
		return s
	case token.BREAK:
		s := p.applyPostfix(p.parseLoopControl(token.BREAK))
		p.lastBlockForm = false
		return s
	case token.NEXT:
		s := p.applyPostfix(p.parseLoopControl(token.NEXT))
		p.lastBlockForm = false
		return s
	}
	s := p.parseSimpleStmt()
	if s == nil {
		p.lastBlockForm = false
		return nil
	}
	s = p.applyPostfix(s)
	p.lastBlockForm = false // a map literal / lambda body ending in } is NOT block-form
	return s
}

// parseSpecialBlock parses a BEGIN { ... } / END { ... } one-liner block. The caller
// has verified the current token is the BEGIN/END identifier and the next is '{'.
func (p *Parser) parseSpecialBlock() ast.Stmt {
	name := p.tok.Lit
	p.next() // consume BEGIN/END
	body := p.parseBlock()
	if body == nil {
		return nil
	}
	return &ast.SpecialBlock{Name: name, Body: body}
}

// parseUse parses a flat-merge import directive: `use "path"`. The caller has
// verified the current token is `use` and the next is a string.
func (p *Parser) parseUse() ast.Stmt {
	pos := p.here()
	p.next() // consume 'use'
	path := p.parseExpr(lowest)
	if path == nil {
		return nil
	}
	return &ast.UseStmt{Pos: pos, Path: path}
}

// parseExample parses a `drang test` assertion: `example EXPR`, `example EXPR == EXPR`,
// or `example EXPR fails`. The caller has verified the current token is `example`.
func (p *Parser) parseExample() ast.Stmt {
	pos := p.here()
	p.next() // consume 'example'
	expr := p.parseExpr(lowest)
	if expr == nil {
		return nil
	}
	ex := &ast.ExampleStmt{Pos: pos}
	if p.tok.Kind == token.IDENT && p.tok.Lit == "fails" {
		p.next()
		ex.Subject = expr
		ex.Fails = true
		return ex
	}
	// A top-level `==` splits into subject/want for a richer failure message.
	if bin, ok := expr.(*ast.Binary); ok && bin.Op == token.EQ {
		ex.Subject, ex.Want = bin.L, bin.R
		return ex
	}
	ex.Subject = expr
	return ex
}

// setStmtPos stamps a statement's source position (its first token) if unset.
func setStmtPos(s ast.Stmt, pos ast.Pos) {
	switch n := s.(type) {
	case *ast.DeclStmt:
		setIfUnset(&n.Pos, pos)
	case *ast.AssignStmt:
		setIfUnset(&n.Pos, pos)
	case *ast.ExprStmt:
		setIfUnset(&n.Pos, pos)
	case *ast.IfStmt:
		setIfUnset(&n.Pos, pos)
	case *ast.WhileStmt:
		setIfUnset(&n.Pos, pos)
	case *ast.ForStmt:
		setIfUnset(&n.Pos, pos)
	case *ast.FnDecl:
		setIfUnset(&n.Pos, pos)
	case *ast.ReturnStmt:
		setIfUnset(&n.Pos, pos)
	case *ast.BreakStmt:
		setIfUnset(&n.Pos, pos)
	case *ast.NextStmt:
		setIfUnset(&n.Pos, pos)
	case *ast.Block:
		setIfUnset(&n.Pos, pos)
	case *ast.SpecialBlock:
		setIfUnset(&n.Pos, pos)
	case *ast.UseStmt:
		setIfUnset(&n.Pos, pos)
	case *ast.ExampleStmt:
		setIfUnset(&n.Pos, pos)
	}
}

func setIfUnset(dst *ast.Pos, pos ast.Pos) {
	if dst.Line == 0 {
		*dst = pos
	}
}

// applyPostfix wraps a simple statement in a trailing if/unless/while/until.
func (p *Parser) applyPostfix(s ast.Stmt) ast.Stmt {
	switch p.tok.Kind {
	case token.IF:
		p.next()
		cond := p.parseExpr(lowest)
		if cond == nil {
			return nil
		}
		return &ast.IfStmt{Cond: cond, Then: blockOf(s)}
	case token.UNLESS:
		p.next()
		cond := p.parseExpr(lowest)
		if cond == nil {
			return nil
		}
		return &ast.IfStmt{Cond: notExpr(cond), Then: blockOf(s)}
	case token.WHILE:
		p.next()
		cond := p.parseExpr(lowest)
		if cond == nil {
			return nil
		}
		return &ast.WhileStmt{Cond: cond, Body: blockOf(s)}
	case token.UNTIL:
		p.next()
		cond := p.parseExpr(lowest)
		if cond == nil {
			return nil
		}
		return &ast.WhileStmt{Cond: notExpr(cond), Body: blockOf(s)}
	case token.FOR:
		p.next()
		iter := p.parseExpr(lowest)
		if iter == nil {
			return nil
		}
		return &ast.ForStmt{Vars: []string{"_"}, Iter: iter, Body: blockOf(s)}
	}
	return s
}

func (p *Parser) parseSimpleStmt() ast.Stmt {
	if p.tok.Kind == token.RETURN {
		return p.parseReturn()
	}
	if p.tok.Kind == token.VAR && p.peek.Kind == token.COLONEQ {
		return p.parseDecl(false)
	}
	if p.tok.Kind == token.VAR && p.peek.Kind == token.CONSTDECL {
		return p.parseDecl(true)
	}
	lhs := p.parseExpr(lowest)
	if lhs == nil {
		return nil
	}
	if op, isAssign := assignOp(p.tok.Kind); isAssign {
		if !isAssignable(lhs) {
			p.errorf("cannot assign to %s", lhs)
			return nil
		}
		p.next() // consume '=' / '+=' / etc.
		rhs := p.parseExpr(lowest)
		if rhs == nil {
			return nil
		}
		return &ast.AssignStmt{Target: lhs, Op: op, Value: rhs}
	}
	return &ast.ExprStmt{X: lhs}
}

func (p *Parser) parseDecl(isConst bool) ast.Stmt {
	name := p.tok.Lit
	p.next() // $name
	p.next() // := or ::=
	val := p.parseExpr(lowest)
	if val == nil {
		return nil
	}
	return &ast.DeclStmt{Name: name, Value: val, Const: isConst}
}

func assignOp(k token.Kind) (token.Kind, bool) {
	switch k {
	case token.ASSIGN:
		return token.ILLEGAL, true
	case token.PLUSEQ:
		return token.PLUS, true
	case token.MINUSEQ:
		return token.MINUS, true
	case token.STAREQ:
		return token.STAR, true
	case token.SLASHEQ:
		return token.SLASH, true
	}
	return token.ILLEGAL, false
}

func isAssignable(e ast.Expr) bool {
	switch e.(type) {
	case *ast.Var, *ast.Index, *ast.Field:
		return true
	}
	return false
}

func (p *Parser) skipNewlines() {
	for p.tok.Kind == token.NEWLINE {
		p.next()
	}
}

func (p *Parser) parseArrayLit() ast.Expr {
	pos := p.here()
	p.next() // '['
	p.skipNewlines()
	var elems []ast.Expr
	for p.tok.Kind != token.RBRACKET && p.tok.Kind != token.EOF {
		el := p.parseExpr(lowest)
		if el == nil {
			return nil
		}
		elems = append(elems, el)
		p.skipNewlines()
		if p.tok.Kind != token.COMMA {
			break
		}
		p.next()
		p.skipNewlines()
	}
	if p.tok.Kind != token.RBRACKET {
		p.errorf("expected ']' to close array, got %s %q", p.tok.Kind, p.tok.Lit)
		return nil
	}
	p.next()
	return &ast.ArrayLit{Pos: pos, Elems: elems}
}

func (p *Parser) parseMapLit() ast.Expr {
	pos := p.here()
	p.next() // '{'
	p.skipNewlines()
	var keys, vals []ast.Expr
	for p.tok.Kind != token.RBRACE && p.tok.Kind != token.EOF {
		k := p.parseExpr(lowest)
		if k == nil {
			return nil
		}
		if p.tok.Kind != token.COLON {
			p.errorf("expected ':' in map literal, got %s %q", p.tok.Kind, p.tok.Lit)
			return nil
		}
		p.next() // ':'
		v := p.parseExpr(lowest)
		if v == nil {
			return nil
		}
		keys = append(keys, k)
		vals = append(vals, v)
		p.skipNewlines()
		if p.tok.Kind != token.COMMA {
			break
		}
		p.next()
		p.skipNewlines()
	}
	if p.tok.Kind != token.RBRACE {
		p.errorf("expected '}' to close map, got %s %q", p.tok.Kind, p.tok.Lit)
		return nil
	}
	p.next()
	return &ast.MapLit{Pos: pos, Keys: keys, Vals: vals}
}

// parseLambda parses an anonymous function: |$a, $b| expr  or  |$a| { ... }.
// A '{' immediately after the parameters is a block body; to return a map, wrap
// it in parens: |$x| ({a: 1}).
// parseParams parses a comma-separated parameter list — each $name with an optional
// `= default` — up to the given closing token. A required parameter may not follow a
// defaulted one. The returned defaults slice is parallel to params (nil = required).
func (p *Parser) parseParams(end token.Kind) (params []string, defaults []ast.Expr, ok bool) {
	if p.tok.Kind == end {
		return nil, nil, true
	}
	seenDefault := false
	for {
		if p.tok.Kind != token.VAR {
			p.errorf("expected $param, got %s %q", p.tok.Kind, p.tok.Lit)
			return nil, nil, false
		}
		params = append(params, p.tok.Lit)
		p.next()
		if p.tok.Kind == token.ASSIGN { // $name = default
			p.next()
			d := p.parseExpr(lowest)
			if d == nil {
				return nil, nil, false
			}
			defaults = append(defaults, d)
			seenDefault = true
		} else {
			if seenDefault {
				p.errorf("a required parameter ($%s) cannot follow a defaulted one", params[len(params)-1])
			}
			defaults = append(defaults, nil)
		}
		if p.tok.Kind != token.COMMA {
			break
		}
		p.next()
		if p.tok.Kind == end { // tolerate a trailing comma
			break
		}
	}
	return params, defaults, true
}

func (p *Parser) parseLambda() ast.Expr {
	pos := p.here()
	p.next() // opening '|'
	params, defaults, ok := p.parseParams(token.BAR)
	if !ok {
		return nil
	}
	if p.tok.Kind != token.BAR {
		p.errorf("expected '|' to close lambda parameters, got %s %q", p.tok.Kind, p.tok.Lit)
		return nil
	}
	p.next() // closing '|'
	outerLoop := p.loopDepth
	p.loopDepth = 0 // a lambda is a new function scope: break/next can't escape into an outer loop
	var body *ast.Block
	if p.tok.Kind == token.LBRACE {
		body = p.parseBlock()
	} else {
		expr := p.parseExpr(lowest)
		if expr == nil {
			p.loopDepth = outerLoop
			return nil
		}
		body = &ast.Block{Pos: pos, Stmts: []ast.Stmt{&ast.ExprStmt{Pos: exprPos(expr), X: expr}}}
	}
	p.loopDepth = outerLoop
	if body == nil {
		return nil
	}
	return &ast.Lambda{Pos: pos, Params: params, Defaults: defaults, Body: body}
}

func (p *Parser) parseIf() ast.Stmt {
	p.next() // 'if'
	cond := p.parseExpr(lowest)
	if cond == nil {
		return nil
	}
	then := p.parseBlock()
	if then == nil {
		return nil
	}
	var els ast.Stmt
	// allow 'else' on the same line as '}' or after a single newline
	if p.tok.Kind == token.NEWLINE && p.peek.Kind == token.ELSE {
		p.next()
	}
	if p.tok.Kind == token.ELSE {
		p.next()
		if p.tok.Kind == token.IF {
			els = p.parseIf()
		} else {
			b := p.parseBlock()
			if b == nil {
				return nil
			}
			els = b
		}
	}
	return &ast.IfStmt{Cond: cond, Then: then, Else: els}
}

func (p *Parser) parseWhile() ast.Stmt {
	p.next() // 'while'
	cond := p.parseExpr(lowest)
	if cond == nil {
		return nil
	}
	p.loopDepth++
	body := p.parseBlock()
	p.loopDepth--
	if body == nil {
		return nil
	}
	return &ast.WhileStmt{Cond: cond, Body: body}
}

// parseFor parses  for $x in iter { ... }  or  for $i, $x in iter { ... }.
func (p *Parser) parseFor() ast.Stmt {
	p.next() // 'for'
	if p.tok.Kind != token.VAR {
		p.errorf("expected $var after 'for', got %s %q", p.tok.Kind, p.tok.Lit)
		return nil
	}
	vars := []string{p.tok.Lit}
	p.next()
	if p.tok.Kind == token.COMMA {
		p.next()
		if p.tok.Kind != token.VAR {
			p.errorf("expected second $var after ',' in for, got %s %q", p.tok.Kind, p.tok.Lit)
			return nil
		}
		vars = append(vars, p.tok.Lit)
		p.next()
	}
	if p.tok.Kind != token.IN {
		p.errorf("expected 'in' after for variables, got %s %q", p.tok.Kind, p.tok.Lit)
		return nil
	}
	p.next() // 'in'
	iter := p.parseExpr(lowest)
	if iter == nil {
		return nil
	}
	p.loopDepth++
	body := p.parseBlock()
	p.loopDepth--
	if body == nil {
		return nil
	}
	return &ast.ForStmt{Vars: vars, Iter: iter, Body: body}
}

// parseLoopControl parses break/next. They are only valid lexically inside a loop
// in the current function (loopDepth resets at fn/lambda boundaries), so a stray
// one is a parse error — caught before execution, like an undefined label.
func (p *Parser) parseLoopControl(kind token.Kind) ast.Stmt {
	word := "break"
	if kind == token.NEXT {
		word = "next"
	}
	if p.loopDepth == 0 {
		p.errorf("'%s' outside a loop", word)
	}
	p.next() // consume break/next
	if kind == token.NEXT {
		return &ast.NextStmt{}
	}
	return &ast.BreakStmt{}
}

func (p *Parser) parseBlock() *ast.Block {
	if p.tok.Kind != token.LBRACE {
		p.errorf("expected '{', got %s %q", p.tok.Kind, p.tok.Lit)
		return nil
	}
	p.next()
	p.blockDepth++
	blk := &ast.Block{Stmts: p.parseStmtsUntil(token.RBRACE)}
	p.blockDepth--
	if p.tok.Kind != token.RBRACE {
		p.errorf("expected '}', got %s %q", p.tok.Kind, p.tok.Lit)
		return nil
	}
	p.next()
	return blk
}

// here is the source position of the current token, for stamping onto a node.
func (p *Parser) here() ast.Pos { return ast.Pos{Line: p.tok.Line, Col: p.tok.Col} }

// exprPos returns an already-parsed expression's position (every node embeds Pos).
func exprPos(e ast.Expr) ast.Pos {
	if l, ok := e.(interface{ Loc() (int, int) }); ok {
		line, col := l.Loc()
		return ast.Pos{Line: line, Col: col}
	}
	return ast.Pos{}
}

func (p *Parser) parseExpr(prec int) ast.Expr {
	left := p.parsePrefix()
	if left == nil {
		return nil
	}
	for !isTerm(p.tok.Kind) && prec < precedence(p.tok.Kind) {
		left = p.parseInfix(left)
		if left == nil {
			return nil
		}
	}
	return left
}

func (p *Parser) parsePrefix() ast.Expr {
	pos := p.here()
	switch p.tok.Kind {
	case token.INT:
		v, err := strconv.ParseInt(p.tok.Lit, 10, 64)
		if err != nil {
			p.errorf("invalid integer %q", p.tok.Lit)
		}
		p.next()
		return &ast.IntLit{Pos: pos, Value: v}
	case token.FLOAT:
		v, err := strconv.ParseFloat(p.tok.Lit, 64)
		if err != nil {
			p.errorf("invalid float %q", p.tok.Lit)
		}
		p.next()
		return &ast.FloatLit{Pos: pos, Value: v}
	case token.STRING:
		raw := p.tok.Lit
		p.next()
		return p.interpolate(raw, pos)
	case token.RAWSTR:
		s := p.tok.Lit
		p.next()
		return &ast.StringLit{Pos: pos, Value: s} // q{...}: literal, no interpolation
	case token.QW:
		words := strings.Fields(p.tok.Lit)
		p.next()
		elems := make([]ast.Expr, len(words))
		for i, w := range words {
			elems[i] = &ast.StringLit{Pos: pos, Value: w}
		}
		return &ast.ArrayLit{Pos: pos, Elems: elems}
	case token.QR:
		pat := p.tok.Lit
		p.next()
		return &ast.RegexLit{Pos: pos, Pattern: pat}
	case token.TRUE:
		p.next()
		return &ast.BoolLit{Pos: pos, Value: true}
	case token.FALSE:
		p.next()
		return &ast.BoolLit{Pos: pos, Value: false}
	case token.VAR:
		name := p.tok.Lit
		p.next()
		return &ast.Var{Pos: pos, Name: name}
	case token.IDENT:
		name := p.tok.Lit
		p.next()
		return &ast.Ident{Pos: pos, Name: name}
	case token.DOT:
		// Leading-dot user-function reference: .name lives in the user namespace. The
		// name keeps its '.', so it resolves through the ordinary identifier path and
		// is naturally disjoint from bare builtins (different keys). This is a prefix
		// (nud) role; infix '.' (field access) is handled separately in parseInfix.
		p.next() // '.'
		if p.tok.Kind != token.IDENT {
			p.errorf("expected a name after '.', got %s %q", p.tok.Kind, p.tok.Lit)
			return nil
		}
		dotName := p.tok.Lit
		p.next()
		return &ast.Ident{Pos: pos, Name: "." + dotName}
	case token.LPAREN:
		p.next()
		e := p.parseExpr(lowest)
		if e == nil {
			return nil
		}
		if p.tok.Kind != token.RPAREN {
			p.errorf("expected ')', got %s %q", p.tok.Kind, p.tok.Lit)
			return nil
		}
		p.next()
		return e
	case token.LBRACKET:
		return p.parseArrayLit()
	case token.LBRACE:
		return p.parseMapLit()
	case token.BAR:
		return p.parseLambda()
	case token.MINUS, token.BANG, token.NOT:
		op := p.tok.Kind
		if op == token.NOT {
			op = token.BANG
		}
		p.next()
		x := p.parseExpr(prefix)
		if x == nil {
			return nil
		}
		return &ast.Unary{Pos: pos, Op: op, X: x}
	}
	p.errorf("unexpected %s %q", p.tok.Kind, p.tok.Lit)
	p.next()
	return nil
}

func (p *Parser) parseInfix(left ast.Expr) ast.Expr {
	pos := p.here()
	switch p.tok.Kind {
	case token.PLUS, token.MINUS, token.STAR, token.SLASH, token.PERCENT, token.TILDE,
		token.EQ, token.NE, token.LT, token.LE, token.GT, token.GE, token.SPACESHIP:
		op := p.tok.Kind
		pr := precedence(op)
		p.next()
		right := p.parseExpr(pr)
		if right == nil {
			return nil
		}
		return &ast.Binary{Pos: pos, Op: op, L: left, R: right}
	case token.PIPE:
		p.next()
		right := p.parseExpr(pipeline)
		if right == nil {
			return nil
		}
		return makePipe(left, right)
	case token.OR:
		p.next()
		right := p.parseExpr(boolOr)
		if right == nil {
			return nil
		}
		return &ast.Logical{Pos: pos, Op: token.OR, L: left, R: right}
	case token.AND:
		p.next()
		right := p.parseExpr(boolAnd)
		if right == nil {
			return nil
		}
		return &ast.Logical{Pos: pos, Op: token.AND, L: left, R: right}
	case token.DEFOR:
		p.next()
		fallback := p.parseExpr(orElse)
		if fallback == nil {
			return nil
		}
		return &ast.DefOr{Pos: pos, X: left, Fallback: fallback}
	case token.DOTDOT:
		p.next()
		hi := p.parseExpr(rangelvl)
		if hi == nil {
			return nil
		}
		return &ast.RangeLit{Pos: pos, Lo: left, Hi: hi}
	case token.LPAREN:
		return p.parseCall(left)
	case token.LBRACKET:
		p.next()
		idx := p.parseExpr(lowest)
		if idx == nil {
			return nil
		}
		if p.tok.Kind != token.RBRACKET {
			p.errorf("expected ']', got %s %q", p.tok.Kind, p.tok.Lit)
			return nil
		}
		p.next()
		return &ast.Index{Pos: pos, X: left, Idx: idx}
	case token.DOT:
		p.next()
		if p.tok.Kind != token.IDENT {
			p.errorf("expected field name after '.', got %s %q", p.tok.Kind, p.tok.Lit)
			return nil
		}
		name := p.tok.Lit
		p.next()
		return &ast.Field{Pos: pos, X: left, Name: name}
	case token.QUESTION:
		p.next()
		return &ast.Propagate{Pos: pos, X: left}
	}
	p.errorf("no infix parse for %s", p.tok.Kind)
	return nil
}

func (p *Parser) parseCall(callee ast.Expr) ast.Expr {
	p.next() // '('
	var args []ast.Expr
	if p.tok.Kind != token.RPAREN {
		first := p.parseExpr(lowest)
		if first == nil {
			return nil
		}
		args = append(args, first)
		for p.tok.Kind == token.COMMA {
			p.next()
			a := p.parseExpr(lowest)
			if a == nil {
				return nil
			}
			args = append(args, a)
		}
	}
	if p.tok.Kind != token.RPAREN {
		p.errorf("expected ')' to close call, got %s %q", p.tok.Kind, p.tok.Lit)
		return nil
	}
	p.next()
	return &ast.Call{Pos: exprPos(callee), Callee: callee, Args: args}
}

// makePipe builds a faithful  left |> right  Pipe node. When right is a call f(b),
// Call holds it verbatim (args NOT including left); a bare  left |> f  wraps f in an
// arg-less Call so the node shape is uniform. eval prepends left as the first arg, so
// the runtime behavior matches the old desugaring to f(left, b).
func makePipe(left, right ast.Expr) ast.Expr {
	if c, ok := right.(*ast.Call); ok {
		return &ast.Pipe{Pos: exprPos(right), Lhs: left, Call: c}
	}
	return &ast.Pipe{Pos: exprPos(right), Lhs: left, Call: &ast.Call{Pos: exprPos(right), Callee: right}}
}

// interpolate decodes a raw string body, processing escapes and $-interpolation
// together (it has the raw form, so it can tell "\$" — a literal $ — from "$x").
// "$name" splices a variable; "${expr}" splices any expression. The result is a
// plain StringLit when there is no interpolation, otherwise a ~-concatenation;
// a leading "" is prepended when needed so the value is always a string.
func (p *Parser) interpolate(raw string, pos ast.Pos) ast.Expr {
	var ops []ast.Expr
	var seg strings.Builder
	flush := func() {
		if seg.Len() > 0 {
			ops = append(ops, &ast.StringLit{Pos: pos, Value: seg.String()})
			seg.Reset()
		}
	}
	i := 0
	for i < len(raw) {
		c := raw[i]
		if c == '\\' && i+1 < len(raw) {
			switch raw[i+1] {
			case 'n':
				seg.WriteByte('\n')
			case 't':
				seg.WriteByte('\t')
			case 'r':
				seg.WriteByte('\r')
			case '\\':
				seg.WriteByte('\\')
			case '"':
				seg.WriteByte('"')
			case '$':
				seg.WriteByte('$') // escaped interpolation -> literal $
			default:
				// lenient: keep the backslash (regex "\d", paths "C:\dir")
				seg.WriteByte('\\')
				seg.WriteByte(raw[i+1])
			}
			i += 2
			continue
		}
		if c == '$' && i+1 < len(raw) {
			if raw[i+1] == '{' { // ${expr}
				j, ok := matchBrace(raw, i+2)
				if !ok {
					p.errorf("unterminated ${...} in string")
					return &ast.StringLit{Pos: pos, Value: seg.String()}
				}
				flush()
				ops = append(ops, p.subExpr(raw[i+2:j], pos))
				i = j + 1
				continue
			}
			if isIdentStart(raw[i+1]) { // $name
				j := i + 1
				for j < len(raw) && isIdentChar(raw[j]) {
					j++
				}
				flush()
				ops = append(ops, &ast.Var{Pos: pos, Name: raw[i+1 : j]})
				i = j
				continue
			}
		}
		seg.WriteByte(c) // ordinary char, or a lone '$'
		i++
	}
	flush()
	if len(ops) == 0 {
		return &ast.StringLit{Pos: pos, Value: ""}
	}
	expr := ops[0]
	if _, isStr := expr.(*ast.StringLit); !isStr {
		// first piece is an interpolation: force a string result with "" ~ ...
		expr = &ast.Binary{Pos: pos, Op: token.TILDE, L: &ast.StringLit{Pos: pos}, R: expr}
	}
	for _, op := range ops[1:] {
		expr = &ast.Binary{Pos: pos, Op: token.TILDE, L: expr, R: op}
	}
	return expr
}

// matchBrace returns the index of the '}' closing a '{' (raw[start] is just past
// the '{'), counting nesting and skipping string literals; ok is false if unmatched.
func matchBrace(raw string, start int) (int, bool) {
	depth := 1
	for j := start; j < len(raw); j++ {
		switch raw[j] {
		case '"':
			j++
			for j < len(raw) && raw[j] != '"' {
				if raw[j] == '\\' && j+1 < len(raw) {
					j++
				}
				j++
			}
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return j, true
			}
		}
	}
	return 0, false
}

// subExpr parses an interpolated ${...} body as a single expression.
func (p *Parser) subExpr(inner string, pos ast.Pos) ast.Expr {
	sub := New(inner)
	e := sub.parseExpr(lowest)
	for _, m := range sub.errs {
		p.errs = append(p.errs, "in interpolation: "+m)
	}
	if e == nil {
		return &ast.StringLit{Pos: pos, Value: ""}
	}
	if sub.tok.Kind != token.EOF && sub.tok.Kind != token.NEWLINE {
		p.errorf("unexpected %q in ${...}", sub.tok.Lit)
	}
	return e
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentChar(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

func blockOf(s ast.Stmt) *ast.Block { return &ast.Block{Stmts: []ast.Stmt{s}} }

func notExpr(e ast.Expr) ast.Expr { return &ast.Unary{Op: token.BANG, X: e} }

func (p *Parser) parseFn() ast.Stmt {
	p.next() // 'fn'
	dot := ""
	if p.tok.Kind == token.DOT { // fn .name — a user-namespace function
		dot = "."
		p.next()
	}
	if p.tok.Kind != token.IDENT {
		p.errorf("expected function name after 'fn', got %s %q", p.tok.Kind, p.tok.Lit)
		return nil
	}
	if dot == "" && !p.stdlibMode {
		p.errorf("user functions need a leading dot: write 'fn .%s' (bare names are reserved for builtins)", p.tok.Lit)
	}
	name := dot + p.tok.Lit
	p.next()
	if p.tok.Kind != token.LPAREN {
		p.errorf("expected '(' after function name, got %s %q", p.tok.Kind, p.tok.Lit)
		return nil
	}
	p.next()
	params, defaults, ok := p.parseParams(token.RPAREN)
	if !ok {
		return nil
	}
	if p.tok.Kind != token.RPAREN {
		p.errorf("expected ')' to close parameters, got %s %q", p.tok.Kind, p.tok.Lit)
		return nil
	}
	p.next()
	outerLoop := p.loopDepth
	p.loopDepth = 0 // a function body is its own scope: break/next can't escape it
	body := p.parseBlock()
	p.loopDepth = outerLoop
	if body == nil {
		return nil
	}
	return &ast.FnDecl{Name: name, Params: params, Defaults: defaults, Body: body}
}

func (p *Parser) parseReturn() ast.Stmt {
	p.next() // 'return'
	if isTerm(p.tok.Kind) || p.tok.Kind == token.RBRACE || p.tok.Kind == token.EOF {
		return &ast.ReturnStmt{Value: nil}
	}
	val := p.parseExpr(lowest)
	if val == nil {
		return nil
	}
	return &ast.ReturnStmt{Value: val}
}
