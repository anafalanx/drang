package eval

import "github.com/anafalanx/lang3/internal/ast"

// registerEligible reports whether a function body can use the fast register-
// resident-locals mode: none of its params/locals are captured by a nested
// closure, it declares no constants, and it declares no nested named functions.
//
// The analysis is deliberately CONSERVATIVE — when unsure it returns false, and
// the function falls back to the (correct, slower) Env-backed mode. Capture is
// over-approximated: any name a nested closure mentions anywhere is treated as
// captured, even if the closure actually shadows it. That only ever costs an
// optimization, never correctness. The reason register reuse is sound here: a
// capture-free function's locals are never reachable by a closure, so reusing a
// slot across loop iterations is unobservable.
func registerEligible(params []string, body *ast.Block) bool {
	a := &funcAnalysis{
		declared:       map[string]bool{},
		usedInClosures: map[string]bool{},
	}
	for _, p := range params {
		a.declared[p] = true
	}
	a.scanBlock(body)
	if a.hasConst || a.hasFnDecl {
		return false
	}
	for name := range a.declared {
		if a.usedInClosures[name] {
			return false // a param/local is captured by a nested closure
		}
	}
	return true
}

type funcAnalysis struct {
	declared       map[string]bool // this function's params + locals (not nested closures')
	usedInClosures map[string]bool // every Var name appearing inside a nested closure
	hasConst       bool
	hasFnDecl      bool
}

func (a *funcAnalysis) scanBlock(b *ast.Block) {
	for _, s := range b.Stmts {
		a.scanStmt(s)
	}
}

func (a *funcAnalysis) scanStmt(s ast.Stmt) {
	switch n := s.(type) {
	case *ast.DeclStmt:
		a.declared[n.Name] = true
		if n.Const {
			a.hasConst = true
		}
		a.scanExpr(n.Value)
	case *ast.AssignStmt:
		a.scanExpr(n.Target)
		a.scanExpr(n.Value)
	case *ast.ExprStmt:
		a.scanExpr(n.X)
	case *ast.IfStmt:
		a.scanExpr(n.Cond)
		a.scanBlock(n.Then)
		switch e := n.Else.(type) {
		case *ast.Block:
			a.scanBlock(e)
		case *ast.IfStmt:
			a.scanStmt(e)
		}
	case *ast.WhileStmt:
		a.scanExpr(n.Cond)
		a.scanBlock(n.Body)
	case *ast.ForStmt:
		for _, v := range n.Vars {
			a.declared[v] = true
		}
		a.scanExpr(n.Iter)
		a.scanBlock(n.Body)
	case *ast.FnDecl:
		a.hasFnDecl = true
		a.declared[n.Name] = true
		collectVars(n.Body, a.usedInClosures)
	case *ast.ReturnStmt:
		if n.Value != nil {
			a.scanExpr(n.Value)
		}
	}
}

// scanExpr walks this function's own expressions, descending into nested closures
// only to harvest the names they use (via collectVars), never treating a closure's
// body as this function's code.
func (a *funcAnalysis) scanExpr(e ast.Expr) {
	switch n := e.(type) {
	case *ast.Lambda:
		collectVars(n.Body, a.usedInClosures)
	case *ast.Unary:
		a.scanExpr(n.X)
	case *ast.Binary:
		a.scanExpr(n.L)
		a.scanExpr(n.R)
	case *ast.Logical:
		a.scanExpr(n.L)
		a.scanExpr(n.R)
	case *ast.Call:
		a.scanExpr(n.Callee)
		for _, arg := range n.Args {
			a.scanExpr(arg)
		}
	case *ast.Index:
		a.scanExpr(n.X)
		a.scanExpr(n.Idx)
	case *ast.Field:
		a.scanExpr(n.X)
	case *ast.Propagate:
		a.scanExpr(n.X)
	case *ast.DefOr:
		a.scanExpr(n.X)
		a.scanExpr(n.Fallback)
	case *ast.ArrayLit:
		for _, el := range n.Elems {
			a.scanExpr(el)
		}
	case *ast.MapLit:
		for _, k := range n.Keys {
			a.scanExpr(k)
		}
		for _, v := range n.Vals {
			a.scanExpr(v)
		}
	case *ast.RangeLit:
		a.scanExpr(n.Lo)
		a.scanExpr(n.Hi)
	}
}

// collectBoundNames gathers every name the program binds anywhere — var/const
// decls, function names and params, lambda params, for-loop vars. A builtin/HOF/
// special-form name ABSENT from this set can never be shadowed by a user binding,
// so a call to it dispatches directly with no env lookup. Whole-program and
// conservative: a name bound in any scope disables direct dispatch for it
// everywhere (never an unsound direct dispatch, occasionally a missed one).
func collectBoundNames(prog *ast.Program) map[string]bool {
	set := map[string]bool{}
	for _, s := range prog.Stmts {
		boundInStmt(s, set)
	}
	return set
}

func boundInStmt(s ast.Stmt, set map[string]bool) {
	switch n := s.(type) {
	case *ast.DeclStmt:
		set[n.Name] = true
		boundInExpr(n.Value, set)
	case *ast.AssignStmt:
		boundInExpr(n.Value, set) // an assignment target rebinds, never introduces a name
	case *ast.ExprStmt:
		boundInExpr(n.X, set)
	case *ast.IfStmt:
		boundInExpr(n.Cond, set)
		boundInStmt(n.Then, set)
		if n.Else != nil {
			boundInStmt(n.Else, set)
		}
	case *ast.WhileStmt:
		boundInExpr(n.Cond, set)
		boundInStmt(n.Body, set)
	case *ast.ForStmt:
		for _, v := range n.Vars {
			set[v] = true
		}
		boundInExpr(n.Iter, set)
		boundInStmt(n.Body, set)
	case *ast.FnDecl:
		set[n.Name] = true
		for _, p := range n.Params {
			set[p] = true
		}
		boundInStmt(n.Body, set)
	case *ast.ReturnStmt:
		if n.Value != nil {
			boundInExpr(n.Value, set)
		}
	case *ast.Block:
		for _, st := range n.Stmts {
			boundInStmt(st, set)
		}
	}
}

func boundInExpr(e ast.Expr, set map[string]bool) {
	switch n := e.(type) {
	case *ast.Unary:
		boundInExpr(n.X, set)
	case *ast.Binary:
		boundInExpr(n.L, set)
		boundInExpr(n.R, set)
	case *ast.Logical:
		boundInExpr(n.L, set)
		boundInExpr(n.R, set)
	case *ast.Call:
		boundInExpr(n.Callee, set)
		for _, a := range n.Args {
			boundInExpr(a, set)
		}
	case *ast.Index:
		boundInExpr(n.X, set)
		boundInExpr(n.Idx, set)
	case *ast.Field:
		boundInExpr(n.X, set)
	case *ast.Propagate:
		boundInExpr(n.X, set)
	case *ast.DefOr:
		boundInExpr(n.X, set)
		boundInExpr(n.Fallback, set)
	case *ast.ArrayLit:
		for _, el := range n.Elems {
			boundInExpr(el, set)
		}
	case *ast.MapLit:
		for _, k := range n.Keys {
			boundInExpr(k, set)
		}
		for _, v := range n.Vals {
			boundInExpr(v, set)
		}
	case *ast.RangeLit:
		boundInExpr(n.Lo, set)
		boundInExpr(n.Hi, set)
	case *ast.Lambda:
		for _, p := range n.Params {
			set[p] = true
		}
		boundInStmt(n.Body, set)
	}
}

// collectVars records every Var name in a subtree (descending into everything,
// including nested closures). Used to over-approximate what a closure captures.
func collectVars(n ast.Node, set map[string]bool) {
	switch e := n.(type) {
	case *ast.Var:
		set[e.Name] = true
	case *ast.Unary:
		collectVars(e.X, set)
	case *ast.Binary:
		collectVars(e.L, set)
		collectVars(e.R, set)
	case *ast.Logical:
		collectVars(e.L, set)
		collectVars(e.R, set)
	case *ast.Call:
		collectVars(e.Callee, set)
		for _, a := range e.Args {
			collectVars(a, set)
		}
	case *ast.Index:
		collectVars(e.X, set)
		collectVars(e.Idx, set)
	case *ast.Field:
		collectVars(e.X, set)
	case *ast.Propagate:
		collectVars(e.X, set)
	case *ast.DefOr:
		collectVars(e.X, set)
		collectVars(e.Fallback, set)
	case *ast.ArrayLit:
		for _, el := range e.Elems {
			collectVars(el, set)
		}
	case *ast.MapLit:
		for _, k := range e.Keys {
			collectVars(k, set)
		}
		for _, v := range e.Vals {
			collectVars(v, set)
		}
	case *ast.RangeLit:
		collectVars(e.Lo, set)
		collectVars(e.Hi, set)
	case *ast.Lambda:
		collectVars(e.Body, set)
	case *ast.Block:
		for _, s := range e.Stmts {
			collectVars(s, set)
		}
	case *ast.ExprStmt:
		collectVars(e.X, set)
	case *ast.DeclStmt:
		collectVars(e.Value, set)
	case *ast.AssignStmt:
		collectVars(e.Target, set)
		collectVars(e.Value, set)
	case *ast.IfStmt:
		collectVars(e.Cond, set)
		collectVars(e.Then, set)
		if e.Else != nil {
			collectVars(e.Else, set)
		}
	case *ast.WhileStmt:
		collectVars(e.Cond, set)
		collectVars(e.Body, set)
	case *ast.ForStmt:
		collectVars(e.Iter, set)
		collectVars(e.Body, set)
	case *ast.FnDecl:
		collectVars(e.Body, set)
	case *ast.ReturnStmt:
		if e.Value != nil {
			collectVars(e.Value, set)
		}
	}
}
