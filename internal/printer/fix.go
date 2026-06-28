package printer

import "github.com/anafalanx/drang/internal/ast"

// This file is drang's edition / migration mechanism. drang carries no version pragma
// and no multiple semantics; instead, a language revision that renames or reshapes a
// construct ships a mechanical source rewrite here, run by `drang fmt --fix`.
//
// The mechanism is shipped with an empty rule set: migration rules are appended to
// fixRules as the language evolves. Each rule must be IDEMPOTENT and a NO-OP when its
// pattern is absent (so running all rules is always safe), and a rule that deletes a node
// must re-home or drop that node's comments deliberately (the drop-guard otherwise
// rejects the result).

// fixRules is the ordered list of migration rewrites applied by Fix. Empty until a
// revision needs one. Example of the intended shape (a builtin rename):
//
//	func(n ast.Node) {
//		if c, ok := n.(*ast.Call); ok {
//			if id, ok := c.Callee.(*ast.Ident); ok && id.Name == "old" {
//				id.Name = "new"
//			}
//		}
//	}
var fixRules []func(ast.Node)

// Fix applies every registered migration rule to prog in place (pre-order). Called only
// by FormatFix (`drang fmt --fix`); plain Format never rewrites.
func Fix(prog *ast.Program) {
	for _, rule := range fixRules {
		Walk(prog, rule)
	}
}

// Walk visits every node of the tree in pre-order, calling visit on each. Migration
// rules use it to inspect and mutate nodes in place.
func Walk(n ast.Node, visit func(ast.Node)) {
	if n == nil {
		return
	}
	visit(n)
	switch x := n.(type) {
	case *ast.Program:
		for _, s := range x.Stmts {
			Walk(s, visit)
		}
	case *ast.Block:
		for _, s := range x.Stmts {
			Walk(s, visit)
		}
	case *ast.SpecialBlock:
		Walk(x.Body, visit)
	case *ast.UseStmt:
		Walk(x.Path, visit)
	case *ast.ExampleStmt:
		Walk(x.Subject, visit)
		if x.Want != nil {
			Walk(x.Want, visit)
		}
	case *ast.ExprStmt:
		Walk(x.X, visit)
	case *ast.DeclStmt:
		Walk(x.Value, visit)
	case *ast.AssignStmt:
		Walk(x.Target, visit)
		Walk(x.Value, visit)
	case *ast.IfStmt:
		Walk(x.Cond, visit)
		Walk(x.Then, visit)
		if x.Else != nil {
			Walk(x.Else, visit)
		}
	case *ast.WhileStmt:
		Walk(x.Cond, visit)
		Walk(x.Body, visit)
	case *ast.ForStmt:
		Walk(x.Iter, visit)
		Walk(x.Body, visit)
	case *ast.FnDecl:
		walkDefaults(x.Defaults, visit)
		Walk(x.Body, visit)
	case *ast.ReturnStmt:
		if x.Value != nil {
			Walk(x.Value, visit)
		}
	case *ast.Interp:
		for _, p := range x.Parts {
			Walk(p, visit)
		}
	case *ast.Unary:
		Walk(x.X, visit)
	case *ast.Binary:
		Walk(x.L, visit)
		Walk(x.R, visit)
	case *ast.Logical:
		Walk(x.L, visit)
		Walk(x.R, visit)
	case *ast.DefOr:
		Walk(x.X, visit)
		Walk(x.Fallback, visit)
	case *ast.Call:
		Walk(x.Callee, visit)
		for _, a := range x.Args {
			Walk(a, visit)
		}
	case *ast.Pipe:
		Walk(x.Lhs, visit)
		Walk(x.Call, visit)
	case *ast.Index:
		Walk(x.X, visit)
		Walk(x.Idx, visit)
	case *ast.Field:
		Walk(x.X, visit)
	case *ast.Propagate:
		Walk(x.X, visit)
	case *ast.ArrayLit:
		for _, e := range x.Elems {
			Walk(e, visit)
		}
	case *ast.MapLit:
		for i := range x.Keys {
			Walk(x.Keys[i], visit)
			Walk(x.Vals[i], visit)
		}
	case *ast.RangeLit:
		Walk(x.Lo, visit)
		Walk(x.Hi, visit)
	case *ast.Lambda:
		walkDefaults(x.Defaults, visit)
		Walk(x.Body, visit)
	}
	// Leaf nodes (literals, Var, Ident, RegexLit, BreakStmt, NextStmt) have no children.
}

func walkDefaults(defaults []ast.Expr, visit func(ast.Node)) {
	for _, d := range defaults {
		if d != nil {
			Walk(d, visit)
		}
	}
}
