// Package ast defines the drang abstract syntax tree.
//
// Each node's String method renders an S-expression form used by the --ast
// dump, which makes operator precedence, |> desugaring, and control flow easy
// to eyeball. Every node also carries a source Pos (set by the parser) so the
// compiler can record per-instruction positions and runtime errors can point at
// the offending line and column.
package ast

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/anafalanx/drang/internal/token"
)

// Pos is a 1-based source position, embedded in every node. A zero Line means
// "unset" (the position is then inherited from an enclosing node).
type Pos struct {
	Line, Col int
}

// Loc exposes the position generically (every node embeds Pos, so every node
// satisfies interface{ Loc() (int, int) }).
func (p Pos) Loc() (int, int) { return p.Line, p.Col }

// Node is any AST node.
type Node interface{ String() string }

// Expr is an expression node.
type Expr interface {
	Node
	exprNode()
}

// Stmt is a statement node.
type Stmt interface {
	Node
	stmtNode()
}

// Program is the top-level list of statements.
type Program struct{ Stmts []Stmt }

func (p *Program) String() string { return joinStmts(p.Stmts, "\n") }

// Block is a brace-delimited sequence of statements (introduces a scope).
type Block struct {
	Pos
	Stmts []Stmt
}

func (b *Block) String() string { return "(block " + joinStmts(b.Stmts, " ") + ")" }
func (*Block) stmtNode()        {}

// SpecialBlock is a BEGIN { ... } or END { ... } block. BEGIN/END are contextual
// keywords (recognized only as a statement-leading `BEGIN {` / `END {`), and the
// blocks are meaningful only in one-liner stream mode (-n/-p), where the driver
// hoists them out of the per-line loop. Reaching one during normal evaluation is an
// error.
type SpecialBlock struct {
	Pos
	Name string // "BEGIN" or "END"
	Body *Block
}

func (s *SpecialBlock) String() string {
	return "(" + strings.ToLower(s.Name) + " " + joinStmts(s.Body.Stmts, " ") + ")"
}
func (*SpecialBlock) stmtNode() {}

// UseStmt is a bare `use "path"` import directive: it flat-merges a module's
// exported .functions and $CONSTs into the current scope. (The captured form,
// `$u := use("path")`, is an ordinary call to the `use` special form, not this node.)
type UseStmt struct {
	Pos
	Path Expr
}

func (s *UseStmt) String() string { return "(use " + s.Path.String() + ")" }
func (*UseStmt) stmtNode()        {}

// ExampleStmt is a `drang test` assertion — a no-op in a normal run:
//
//	example EXPR == EXPR   (Want set: equality)
//	example EXPR           (Want nil: truthy)
//	example EXPR fails     (Fails set: expects an error)
type ExampleStmt struct {
	Pos
	Subject Expr // the expression under test (the left side of the == form)
	Want    Expr // expected value for the == form; nil for the truthy/fails forms
	Fails   bool // the `fails` form
}

func (s *ExampleStmt) String() string {
	switch {
	case s.Fails:
		return "(example " + s.Subject.String() + " fails)"
	case s.Want != nil:
		return "(example " + s.Subject.String() + " == " + s.Want.String() + ")"
	default:
		return "(example " + s.Subject.String() + ")"
	}
}
func (*ExampleStmt) stmtNode() {}

// ExprStmt is an expression used as a statement.
type ExprStmt struct {
	Pos
	X Expr
}

func (s *ExprStmt) String() string { return s.X.String() }
func (*ExprStmt) stmtNode()        {}

// DeclStmt declares and initializes a lexical binding: $name := value
// (mutable) or $name ::= value (constant, when Const is true).
type DeclStmt struct {
	Pos
	Name  string
	Value Expr
	Const bool
}

func (s *DeclStmt) String() string {
	op := ":="
	if s.Const {
		op = "::="
	}
	return fmt.Sprintf("(%s $%s %s)", op, s.Name, s.Value)
}
func (*DeclStmt) stmtNode() {}

// AssignStmt assigns Value to an lvalue Target (Var, Index, or Field). A
// non-zero Op marks a compound assignment ($x += v): read-modify-write the same
// place with Op. Op == token.ILLEGAL (0) is a plain assignment.
type AssignStmt struct {
	Pos
	Target Expr
	Op     token.Kind
	Value  Expr
}

func (s *AssignStmt) String() string {
	if s.Op != token.ILLEGAL {
		return fmt.Sprintf("(%s= %s %s)", opStr(s.Op), s.Target, s.Value)
	}
	return fmt.Sprintf("(= %s %s)", s.Target, s.Value)
}
func (*AssignStmt) stmtNode() {}

// IfStmt is a conditional. Else is nil, a *Block, or another *IfStmt (else-if).
type IfStmt struct {
	Pos
	Cond Expr
	Then *Block
	Else Stmt
}

func (s *IfStmt) String() string {
	if s.Else != nil {
		return fmt.Sprintf("(if %s %s %s)", s.Cond, s.Then, s.Else)
	}
	return fmt.Sprintf("(if %s %s)", s.Cond, s.Then)
}
func (*IfStmt) stmtNode() {}

// WhileStmt is a conditional loop.
type WhileStmt struct {
	Pos
	Cond Expr
	Body *Block
}

func (s *WhileStmt) String() string { return fmt.Sprintf("(while %s %s)", s.Cond, s.Body) }
func (*WhileStmt) stmtNode()        {}

// ForStmt iterates a collection: for $x in expr { body } (or for $i, $x in expr).
type ForStmt struct {
	Pos
	Vars []string // one name (element) or two (index/key, value)
	Iter Expr
	Body *Block
}

func (s *ForStmt) String() string {
	vs := make([]string, len(s.Vars))
	for i, v := range s.Vars {
		vs[i] = "$" + v
	}
	return fmt.Sprintf("(for (%s) %s %s)", strings.Join(vs, " "), s.Iter, s.Body)
}
func (*ForStmt) stmtNode() {}

// FnDecl declares a named function: fn name($a, $b) { body }.
type FnDecl struct {
	Pos
	Name   string
	Params []string
	Body   *Block
}

func (s *FnDecl) String() string {
	ps := make([]string, len(s.Params))
	for i, p := range s.Params {
		ps[i] = "$" + p
	}
	return fmt.Sprintf("(fn %s (%s) %s)", s.Name, strings.Join(ps, " "), s.Body)
}
func (*FnDecl) stmtNode() {}

// ReturnStmt returns from the enclosing function; Value is nil for a bare return.
type ReturnStmt struct {
	Pos
	Value Expr
}

func (s *ReturnStmt) String() string {
	if s.Value == nil {
		return "(return)"
	}
	return fmt.Sprintf("(return %s)", s.Value)
}
func (*ReturnStmt) stmtNode() {}

// BreakStmt exits the innermost enclosing loop.
type BreakStmt struct{ Pos }

func (*BreakStmt) String() string { return "(break)" }
func (*BreakStmt) stmtNode()      {}

// NextStmt skips to the next iteration of the innermost enclosing loop.
type NextStmt struct{ Pos }

func (*NextStmt) String() string { return "(next)" }
func (*NextStmt) stmtNode()      {}

// IntLit is an integer literal.
type IntLit struct {
	Pos
	Value int64
}

func (e *IntLit) String() string { return strconv.FormatInt(e.Value, 10) }
func (*IntLit) exprNode()        {}

// FloatLit is a floating-point literal.
type FloatLit struct {
	Pos
	Value float64
}

func (e *FloatLit) String() string { return strconv.FormatFloat(e.Value, 'g', -1, 64) }
func (*FloatLit) exprNode()        {}

// StringLit is a string literal.
type StringLit struct {
	Pos
	Value string
}

func (e *StringLit) String() string { return strconv.Quote(e.Value) }
func (*StringLit) exprNode()        {}

// RegexLit is a compiled-regex literal: qr/pattern/flags. Pattern already has any
// trailing flags baked in as Go inline flags (e.g. qr/foo/i -> "(?i)foo").
type RegexLit struct {
	Pos
	Pattern string
}

func (e *RegexLit) String() string { return "(regex " + strconv.Quote(e.Pattern) + ")" }
func (*RegexLit) exprNode()        {}

// BoolLit is a boolean literal.
type BoolLit struct {
	Pos
	Value bool
}

func (e *BoolLit) String() string {
	if e.Value {
		return "true"
	}
	return "false"
}
func (*BoolLit) exprNode() {}

// Var is a use of a $-sigil variable.
type Var struct {
	Pos
	Name string
}

func (e *Var) String() string { return "$" + e.Name }
func (*Var) exprNode()        {}

// Ident is a bare identifier (a builtin or function name).
type Ident struct {
	Pos
	Name string
}

func (e *Ident) String() string { return e.Name }
func (*Ident) exprNode()        {}

// Unary is a prefix operation (- or !).
type Unary struct {
	Pos
	Op token.Kind
	X  Expr
}

func (e *Unary) String() string { return fmt.Sprintf("(%s %s)", opStr(e.Op), e.X) }
func (*Unary) exprNode()        {}

// Binary is an infix operation.
type Binary struct {
	Pos
	Op   token.Kind
	L, R Expr
}

func (e *Binary) String() string { return fmt.Sprintf("(%s %s %s)", opStr(e.Op), e.L, e.R) }
func (*Binary) exprNode()        {}

// Call is a function call f(args...).
type Call struct {
	Pos
	Callee Expr
	Args   []Expr
}

func (e *Call) String() string {
	var b strings.Builder
	b.WriteString("(call ")
	b.WriteString(e.Callee.String())
	for _, a := range e.Args {
		b.WriteByte(' ')
		b.WriteString(a.String())
	}
	b.WriteByte(')')
	return b.String()
}
func (*Call) exprNode() {}

// Index is an indexing expression x[i].
type Index struct {
	Pos
	X   Expr
	Idx Expr
}

func (e *Index) String() string { return fmt.Sprintf("(index %s %s)", e.X, e.Idx) }
func (*Index) exprNode()        {}

// Field is member access x.name.
type Field struct {
	Pos
	X    Expr
	Name string
}

func (e *Field) String() string { return fmt.Sprintf("(. %s %s)", e.X, e.Name) }
func (*Field) exprNode()        {}

// Propagate is the postfix ? error-propagation operator.
type Propagate struct {
	Pos
	X Expr
}

func (e *Propagate) String() string { return fmt.Sprintf("(? %s)", e.X) }
func (*Propagate) exprNode()        {}

// Logical is a short-circuiting boolean operation: L and R, or L or R.
type Logical struct {
	Pos
	Op   token.Kind // AND or OR
	L, R Expr
}

func (e *Logical) String() string { return fmt.Sprintf("(%s %s %s)", opStr(e.Op), e.L, e.R) }
func (*Logical) exprNode()        {}

// DefOr provides a fallback when X is nil or an error: X // Fallback.
type DefOr struct {
	Pos
	X        Expr
	Fallback Expr
}

func (e *DefOr) String() string { return fmt.Sprintf("(// %s %s)", e.X, e.Fallback) }
func (*DefOr) exprNode()        {}

// ArrayLit is an array literal: [e1, e2, ...].
type ArrayLit struct {
	Pos
	Elems []Expr
}

func (e *ArrayLit) String() string {
	parts := make([]string, len(e.Elems))
	for i, el := range e.Elems {
		parts[i] = el.String()
	}
	return "(array " + strings.Join(parts, " ") + ")"
}
func (*ArrayLit) exprNode() {}

// MapLit is a map literal: {k1: v1, k2: v2, ...} (parallel Keys/Vals).
type MapLit struct {
	Pos
	Keys []Expr
	Vals []Expr
}

func (e *MapLit) String() string {
	parts := make([]string, len(e.Keys))
	for i := range e.Keys {
		parts[i] = "(" + e.Keys[i].String() + " " + e.Vals[i].String() + ")"
	}
	return "(map " + strings.Join(parts, " ") + ")"
}
func (*MapLit) exprNode() {}

// RangeLit is a range literal: lo..hi.
type RangeLit struct {
	Pos
	Lo, Hi Expr
}

func (e *RangeLit) String() string { return fmt.Sprintf("(range %s %s)", e.Lo, e.Hi) }
func (*RangeLit) exprNode()        {}

// Lambda is an anonymous function value: |$a, $b| expr  or  |$a| { ... }.
type Lambda struct {
	Pos
	Params []string
	Body   *Block
}

func (e *Lambda) String() string {
	ps := make([]string, len(e.Params))
	for i, p := range e.Params {
		ps[i] = "$" + p
	}
	return fmt.Sprintf("(lambda (%s) %s)", strings.Join(ps, " "), e.Body)
}
func (*Lambda) exprNode() {}

func joinStmts(stmts []Stmt, sep string) string {
	parts := make([]string, len(stmts))
	for i, s := range stmts {
		parts[i] = s.String()
	}
	return strings.Join(parts, sep)
}

func opStr(k token.Kind) string {
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
	case token.OR:
		return "or"
	case token.AND:
		return "and"
	}
	return k.String()
}
