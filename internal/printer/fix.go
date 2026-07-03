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

// fixRules is the ordered list of migration rewrites applied by Fix. Each entry fires
// once on the *ast.Program root and runs its own traversal — that gives a rule parent
// context (e.g. to skip map-literal KEYS, which are bare Idents that name user data,
// not builtins) and per-program state without leaking across files.
var fixRules = []func(ast.Node){
	fixNamespace08,
}

// fixNamespace08 is the pre-1.0 namespace-coherence migration (2026-07):
//
//   - builtin renames: each_line→stream_lines, index_of→find_index, abspath→abs_path,
//     slash→to_slash, strftime→format_time, url_encode→to_url, url_decode→from_url,
//     sys_gc→drang_gc, replace→replace_all (bare replace WAS replace-all; the new
//     bare-first form is replace_first)
//   - gsub(s, pat, r) CALL SITES → replace_all(s, pat, r) when pat is a qr// literal,
//     else replace_all(s, re(pat), r): old gsub compiled a STRING pattern as a regex,
//     while replace_all treats a string needle as a literal — wrapping in re()
//     preserves the regex semantics exactly
//   - tally($xs) CALL SITES → count_by($xs, |$e| $e): tally was count_by with the
//     identity key
//
// Bare Idents rename in every expression position (callee, piped callee, first-class
// reference) but NEVER in map-literal key position, where {replace: 1} is user data.
//
// Deliberately NOT migrated (loud runtime failure beats silent corruption):
//   - a FIRST-CLASS `gsub` reference ($f := gsub; map($fs, gsub)) — a bare rename would
//     silently flip string-pattern semantics from regex to literal (no re() wrap is
//     possible at a reference site); the name is left as-is and fails loudly as
//     "unknown function gsub". Migrate by hand: |$s, $p, $r| replace_all($s, re($p), $r).
//   - a FIRST-CLASS `tally` reference — count_by has a different arity; left as-is
//     (loud). Migrate by hand: |$xs| count_by($xs, |$e| $e).
//   - a gsub/tally call whose arity matches NEITHER the plain nor the piped form — it
//     was already an error in 0.7; the rewrite must not invent behavior for it.
//
// An interpolating string ($"...${...}", $qq{...}) whose ${...} parts are rewritten has
// its Raw cleared, so the printer re-renders it from Parts instead of reprinting the
// stale verbatim source (which would silently discard the migration).
func fixNamespace08(n ast.Node) {
	prog, ok := n.(*ast.Program)
	if !ok {
		return // fires once, on the root
	}
	renames := map[string]string{
		"each_line":  "stream_lines",
		"index_of":   "find_index",
		"abspath":    "abs_path",
		"slash":      "to_slash",
		"strftime":   "format_time",
		"url_encode": "to_url",
		"url_decode": "from_url",
		"sys_gc":     "drang_gc",
		"replace":    "replace_all",
		// gsub and tally are deliberately absent: their bare-reference rename would not
		// preserve semantics (see the function comment); only their CALL SITES migrate,
		// in the structural branch below.
	}

	// Pass 1: collect map-literal KEY Idents — user data, never renamed.
	mapKeys := map[*ast.Ident]bool{}
	Walk(prog, func(n ast.Node) {
		if m, ok := n.(*ast.MapLit); ok {
			for _, k := range m.Keys {
				if id, ok := k.(*ast.Ident); ok {
					mapKeys[id] = true
				}
			}
		}
	})

	// Pass 2: structural rewrites at call sites, then the name swaps, recording every
	// node the pass mutates (pass 3 needs them). Calls are visited before their callee
	// Idents (pre-order), so a structural rule renames its own callee and the later
	// Ident visit sees the new name. Arity distinguishes a plain call from a piped one
	// (x |> f(...) carries the subject in Lhs, not Args).
	mutated := map[ast.Node]bool{}
	Walk(prog, func(n ast.Node) {
		switch x := n.(type) {
		case *ast.Call:
			id, ok := x.Callee.(*ast.Ident)
			if !ok {
				return
			}
			switch id.Name {
			case "gsub":
				// pattern arg: index 1 when plain gsub(s, pat, r), 0 when piped s |> gsub(pat, r)
				pi := -1
				if len(x.Args) == 3 {
					pi = 1
				} else if len(x.Args) == 2 {
					pi = 0
				}
				if pi >= 0 {
					if _, isRe := x.Args[pi].(*ast.RegexLit); !isRe {
						pat := x.Args[pi]
						var pos ast.Pos
						if lc, ok := pat.(interface{ Loc() (int, int) }); ok {
							l, c := lc.Loc()
							pos = ast.Pos{Line: l, Col: c}
						}
						x.Args[pi] = &ast.Call{
							Pos:    pos,
							Callee: &ast.Ident{Pos: pos, Name: "re"},
							Args:   []ast.Expr{pat},
							Rparen: pos.Line,
						}
					}
					id.Name = "replace_all"
					mutated[x], mutated[id] = true, true
				}
			case "tally":
				// plain tally($xs) has 1 arg; piped $xs |> tally() has 0
				if len(x.Args) <= 1 {
					pos := ast.Pos{Line: x.Rparen}
					x.Args = append(x.Args, &ast.Lambda{
						Pos:    pos,
						Params: []string{"e"},
						Body: &ast.Block{
							Pos:   pos,
							Stmts: []ast.Stmt{&ast.ExprStmt{Pos: pos, X: &ast.Var{Pos: pos, Name: "e"}}},
							// Rbrace 0 = synthesized (expr-lambda body), per the Block contract
						},
					})
					id.Name = "count_by"
					mutated[x], mutated[id] = true, true
				}
			}
		case *ast.Ident:
			if to, ok := renames[x.Name]; ok && !mapKeys[x] {
				x.Name = to
				mutated[x] = true
			}
		}
	})
	if len(mutated) == 0 {
		return
	}

	// Pass 3: any interpolating string whose ${...} subtree was mutated must drop its
	// verbatim Raw, or the printer would reprint the OLD source text and silently
	// discard the migration. Each Interp checks its own subtree, so nested
	// interpolations clear independently (an outer one containing a mutated inner one
	// re-renders both).
	Walk(prog, func(n ast.Node) {
		in, ok := n.(*ast.Interp)
		if !ok || in.Raw == "" {
			return
		}
		for _, part := range in.Parts {
			hit := false
			Walk(part, func(m ast.Node) {
				if mutated[m] {
					hit = true
				}
			})
			if hit {
				in.Raw = ""
				return
			}
		}
	})
}

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
