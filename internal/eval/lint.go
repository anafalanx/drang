package eval

import (
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/anafalanx/drang/internal/ast"
	"github.com/anafalanx/drang/internal/lexer"
	"github.com/anafalanx/drang/internal/token"
)

const (
	lintErrDiscard  = "err-discard"
	lintErrBool     = "err-bool"
	lintErrOutput   = "err-output"
	lintWindowsPath = "windows-path"
)

// LintWarning is a compatibility-preserving migration diagnostic. Codes are
// stable so a deliberate use can be suppressed with:
//
//	# lint:ignore err-discard
//
// on the warning's line or on a standalone comment immediately before the
// statement. A bare "# lint:ignore" suppresses every lint code at that site.
type LintWarning struct {
	Code    string
	Message string
	Line    int
	Col     int
}

// LintOptions describes an execution context that creates an implicit value
// consumer not visible in the AST. The zero value preserves normal file/module
// behavior.
type LintOptions struct {
	AutoPrint bool // -p renders $_ after every body iteration
}

// DuplicateTopFns returns the names of functions declared more than once at a program's
// top level, in first-duplication order (each duplicated name appears once).
func DuplicateTopFns(prog *ast.Program) []string {
	counts := map[string]int{}
	var dups []string
	for _, s := range prog.Stmts {
		fd, ok := s.(*ast.FnDecl)
		if !ok {
			continue
		}
		counts[fd.Name]++
		if counts[fd.Name] == 2 {
			dups = append(dups, fd.Name)
		}
	}
	return dups
}

// WarnDuplicateTopFns retains the original duplicate-definition diagnostic.
func WarnDuplicateTopFns(prog *ast.Program, origin string, w io.Writer) {
	dups := DuplicateTopFns(prog)
	if len(dups) == 0 {
		return
	}
	outMu.Lock()
	defer outMu.Unlock()
	for _, name := range dups {
		fmt.Fprintf(w, "drang: warning: %s: function %s is defined more than once at the top level; the last definition wins\n", origin, name)
	}
}

// WarnProgramLints emits duplicate-definition and migration diagnostics for a parsed file.
func WarnProgramLints(prog *ast.Program, src, origin string, comments []lexer.Comment, w io.Writer, opts ...LintOptions) {
	WarnDuplicateTopFns(prog, origin, w)
	warnings := MigrationWarningsWithOptions(prog, src, comments, firstLintOptions(opts))
	if len(warnings) == 0 {
		return
	}
	outMu.Lock()
	defer outMu.Unlock()
	for _, warning := range warnings {
		fmt.Fprintf(w, "drang: warning: %s:%d:%d: %s: %s\n",
			origin, warning.Line, warning.Col, warning.Code, warning.Message)
	}
}

func firstLintOptions(opts []LintOptions) LintOptions {
	if len(opts) > 0 {
		return opts[0]
	}
	return LintOptions{}
}

// knownFallibleNames is intentionally a positive list. Builtins that cannot return
// an Err value (say, warn, bool, type, now, rand, etc.) stay out. User functions
// are not guessed at, and a user binding conservatively disables builtin inference.
var knownFallibleNames = func() map[string]bool {
	const names = `
fail parse_args int str float
store store_update with_store store_get store_set store_has store_delete store_keys store_all store_clear store_path store_close
run capture pipe start kill pid status send_stdin close_stdin recv_stdout recv_stderr stream_lines
len push pop keys values pairs has delete chars contains
path_join dirname basename ext stem abs_path to_slash is_abs clean rel is_within
exists is_dir is_file is_symlink readlink walk glob read_dir mkdir mtime newer stale read_file write_file tempfile tempdir rename rm copy size
split replace_first replace_all trim upper lower starts_with ends_with format lines repeat find_index
from_json to_json from_csv to_csv
abs sum min max floor ceil round sqrt pow log div sin cos tan asin acos atan atan2 exp
re matches match match_all join take drop uniq
spawn await chan send recv recv_ok close drain
sleep format_time parse_time date_parts
sha256 sha1 md5 to_base64 from_base64 to_hex from_hex to_url from_url
rand_int shuffle sample uuid drang_gc cwd home exe is_terminal
http http_get http_post serve use validate
map filter reject each find any all count reduce flat_map pmap sort sort_by min_by max_by
`
	m := make(map[string]bool)
	for _, name := range strings.Fields(names) {
		m[name] = true
	}
	return m
}()

type lintCollector struct {
	warnings     []LintWarning
	seen         map[string]bool
	suppressions map[int]map[string]bool
	active       map[string]bool
	// scopes tracks bindings at their lexical site. The compiler's whole-program
	// collectBoundNames set is deliberately too conservative for diagnostics: a
	// lambda parameter called $read_file must not suppress a warning for the real
	// read_file builtin elsewhere in the file.
	scopes []map[string]bool
	opts   LintOptions
}

// MigrationWarnings finds statically recognizable silent-Err hazards without changing
// runtime behavior. It deliberately does not infer user-function return types.
func MigrationWarnings(prog *ast.Program, src string, comments []lexer.Comment) []LintWarning {
	return MigrationWarningsWithOptions(prog, src, comments, LintOptions{})
}

// MigrationWarningsWithOptions adds diagnostics for implicit consumers supplied
// by a particular execution mode (currently -p's automatic $_ rendering).
func MigrationWarningsWithOptions(prog *ast.Program, src string, comments []lexer.Comment, opts LintOptions) []LintWarning {
	c := &lintCollector{
		seen:         make(map[string]bool),
		suppressions: buildLintSuppressions(src, comments),
		scopes:       []map[string]bool{{}},
		opts:         opts,
	}
	for _, stmt := range prog.Stmts {
		c.stmt(stmt, false) // a program's statement values are discarded
	}
	return c.warnings
}

func (c *lintCollector) pushScope(names []string) {
	scope := make(map[string]bool, len(names))
	for _, name := range names {
		scope[name] = true
	}
	c.scopes = append(c.scopes, scope)
}

func (c *lintCollector) popScope() { c.scopes = c.scopes[:len(c.scopes)-1] }

func (c *lintCollector) bind(name string) {
	c.scopes[len(c.scopes)-1][name] = true
}

func (c *lintCollector) isShadowed(name string) bool {
	for i := len(c.scopes) - 1; i >= 0; i-- {
		if c.scopes[i][name] {
			return true
		}
	}
	return false
}

func (c *lintCollector) add(code, message string, node ast.Node) {
	loc, ok := node.(interface{ Loc() (int, int) })
	if !ok {
		return
	}
	line, col := loc.Loc()
	if line < 1 {
		return
	}
	if c.active["*"] || c.active[code] {
		return
	}
	if codes := c.suppressions[line]; codes["*"] || codes[code] {
		return
	}
	key := fmt.Sprintf("%d:%d:%s", line, col, code)
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	c.warnings = append(c.warnings, LintWarning{Code: code, Message: message, Line: line, Col: col})
}

func (c *lintCollector) block(block *ast.Block, resultUsed bool) {
	c.pushScope(nil)
	defer c.popScope()
	for i, stmt := range block.Stmts {
		c.stmt(stmt, resultUsed && i == len(block.Stmts)-1)
	}
}

// outputSinkArg reports whether argument i is rendered through Display by the
// named builtin. These are the places where an Err can silently become ordinary
// text and a surrounding ? then observes only the sink's success.
func outputSinkArg(name string, i int) bool {
	switch name {
	case "say", "warn", "die":
		return true
	case "str":
		return i == 0
	case "format":
		return i > 0
	case "join":
		return i == 1
	case "write_file", "send_stdin":
		return i == 1
	}
	return false
}

func (c *lintCollector) stmt(stmt ast.Stmt, resultUsed bool) {
	previous := c.active
	if loc, ok := stmt.(interface{ Loc() (int, int) }); ok {
		line, _ := loc.Loc()
		if codes := c.suppressions[line]; len(codes) > 0 {
			c.active = make(map[string]bool, len(previous)+len(codes))
			for code := range previous {
				c.active[code] = true
			}
			for code := range codes {
				c.active[code] = true
			}
		}
	}
	defer func() { c.active = previous }()

	switch n := stmt.(type) {
	case *ast.Block:
		c.block(n, resultUsed)
	case *ast.ExprStmt:
		if !resultUsed {
			if source, name, ok := c.stringifiedFallibleExpr(n.X); ok {
				c.add(lintErrOutput,
					fmt.Sprintf("%s may return Err and is implicitly stringified; handle it before converting it to output text", name), source)
			} else if source, name, ok := c.fallibleExpr(n.X); ok {
				c.add(lintErrDiscard,
					fmt.Sprintf("result derived from fallible call %s is discarded; handle it with ?, //, or is_err", name), source)
			}
		}
		c.expr(n.X)
	case *ast.DeclStmt:
		c.expr(n.Value)
		// The initializer runs before the new binding exists; only later sites in
		// this lexical scope see the user value instead of a same-named builtin.
		c.bind(n.Name)
	case *ast.AssignStmt:
		c.expr(n.Target)
		c.expr(n.Value)
		if v, ok := n.Target.(*ast.Var); ok && v.Name == "_" && c.opts.AutoPrint {
			c.outputOperand(n.Value, "-p automatic output")
		}
	case *ast.FnDecl:
		c.pushScope(nil)
		defer c.popScope()
		for i, d := range n.Defaults {
			if d != nil {
				c.expr(d)
			}
			// Defaults run left-to-right before the current parameter is bound.
			// They can see earlier parameters, but current/later parameter names
			// still resolve through the closure scope, including bare builtins.
			c.bind(n.Params[i])
		}
		c.block(n.Body, true) // final statement is the implicit return value
	case *ast.ReturnStmt:
		if n.Value != nil {
			c.expr(n.Value)
		}
	case *ast.IfStmt:
		c.booleanOperand(n.Cond, "a control-flow condition")
		c.expr(n.Cond)
		c.block(n.Then, resultUsed)
		if n.Else != nil {
			c.stmt(n.Else, resultUsed)
		}
	case *ast.WhileStmt:
		c.booleanOperand(n.Cond, "a loop condition")
		c.expr(n.Cond)
		c.block(n.Body, false)
	case *ast.ForStmt:
		c.expr(n.Iter)
		c.pushScope(n.Vars)
		c.block(n.Body, false)
		c.popScope()
	case *ast.SpecialBlock:
		c.block(n.Body, false)
	case *ast.UseStmt:
		c.expr(n.Path)
	case *ast.ExampleStmt:
		c.expr(n.Subject)
		if n.Want != nil {
			c.expr(n.Want)
		}
	}
}

// stringifiedFallibleExpr identifies an expression whose outer operation turns
// an inner Err into ordinary text. It is separate from fallibleExpr because the
// diagnostic is err-output (identity was lost), not err-discard (an Err value
// survived and was ignored).
func (c *lintCollector) stringifiedFallibleExpr(expr ast.Expr) (ast.Expr, string, bool) {
	switch n := expr.(type) {
	case *ast.Interp:
		for _, part := range n.Parts {
			if source, name, ok := c.embeddedFallibleExpr(part); ok {
				return source, name, true
			}
		}
	case *ast.Binary:
		if n.Op == token.TILDE {
			if source, name, ok := c.embeddedFallibleExpr(n.L); ok {
				return source, name, true
			}
			return c.embeddedFallibleExpr(n.R)
		}
	case *ast.Call:
		if name, ok := identName(n.Callee); ok && !c.isShadowed(name) {
			switch name {
			case "str", "format":
				for i, arg := range n.Args {
					if outputSinkArg(name, i) {
						if source, child, ok := c.embeddedFallibleExpr(arg); ok {
							return source, child, true
						}
					}
				}
			case "join":
				return c.firstJoinStringified(n.Args)
			}
		}
	case *ast.Pipe:
		if name, ok := identName(n.Call.Callee); ok && !c.isShadowed(name) {
			switch name {
			case "str", "format":
				if outputSinkArg(name, 0) {
					if source, child, ok := c.embeddedFallibleExpr(n.Lhs); ok {
						return source, child, true
					}
				}
				for i, arg := range n.Call.Args {
					if outputSinkArg(name, i+1) {
						if source, child, ok := c.embeddedFallibleExpr(arg); ok {
							return source, child, true
						}
					}
				}
			case "join":
				return c.firstJoinStringified(append([]ast.Expr{n.Lhs}, n.Call.Args...))
			}
		}
	}
	return nil, "", false
}

func (c *lintCollector) firstJoinStringified(args []ast.Expr) (ast.Expr, string, bool) {
	if len(args) == 0 {
		return nil, "", false
	}
	arr, ok := args[0].(*ast.ArrayLit)
	if !ok {
		return nil, "", false
	}
	for _, elem := range arr.Elems {
		if source, name, ok := c.embeddedFallibleExpr(elem); ok {
			return source, name, true
		}
	}
	if len(args) > 1 && len(arr.Elems) > 1 {
		return c.embeddedFallibleExpr(args[1])
	}
	return nil, "", false
}

func (c *lintCollector) expr(expr ast.Expr) {
	if expr == nil {
		return
	}
	switch n := expr.(type) {
	case *ast.StringLit:
		if escapedWindowsPath(n) {
			c.add(lintWindowsPath,
				"escape-processing string looks like a Windows path; use q{...} or a raw single-quoted string to preserve backslashes", n)
		}
	case *ast.Interp:
		for _, part := range n.Parts {
			c.outputOperand(part, "string interpolation")
			c.expr(part)
		}
	case *ast.Unary:
		if n.Op == token.BANG {
			c.booleanOperand(n.X, "logical negation")
		}
		c.expr(n.X)
	case *ast.Binary:
		if n.Op == token.TILDE {
			c.outputOperand(n.L, "string concatenation")
			c.outputOperand(n.R, "string concatenation")
		}
		c.expr(n.L)
		c.expr(n.R)
	case *ast.Logical:
		c.booleanOperand(n.L, "a boolean operand")
		c.expr(n.L)
		c.expr(n.R)
	case *ast.DefOr:
		c.expr(n.X)
		c.expr(n.Fallback)
	case *ast.Propagate:
		c.expr(n.X)
	case *ast.Call:
		if name, ok := identName(n.Callee); ok && !c.isShadowed(name) {
			if name == "bool" && len(n.Args) > 0 {
				c.booleanOperand(n.Args[0], "bool()")
			}
			if name == "join" {
				c.joinStringifiedOperands(n.Args, "join()")
			} else {
				for i, arg := range n.Args {
					if outputSinkArg(name, i) {
						c.outputOperand(arg, name+"()")
					}
				}
			}
		}
		c.expr(n.Callee)
		for _, arg := range n.Args {
			c.expr(arg)
		}
	case *ast.Pipe:
		if name, ok := identName(n.Call.Callee); ok && !c.isShadowed(name) {
			if name == "bool" {
				c.booleanOperand(n.Lhs, "bool()")
			}
			if name == "join" {
				c.joinStringifiedOperands(append([]ast.Expr{n.Lhs}, n.Call.Args...), "join()")
			} else {
				if outputSinkArg(name, 0) {
					c.outputOperand(n.Lhs, name+"()")
				}
				for i, arg := range n.Call.Args {
					if outputSinkArg(name, i+1) {
						c.outputOperand(arg, name+"()")
					}
				}
			}
		}
		c.expr(n.Lhs)
		c.expr(n.Call)
	case *ast.Index:
		c.expr(n.X)
		c.expr(n.Idx)
	case *ast.Field:
		c.expr(n.X)
	case *ast.ArrayLit:
		for _, elem := range n.Elems {
			c.expr(elem)
		}
	case *ast.MapLit:
		for i := range n.Keys {
			c.expr(n.Keys[i])
			c.expr(n.Vals[i])
		}
	case *ast.RangeLit:
		c.expr(n.Lo)
		c.expr(n.Hi)
	case *ast.Lambda:
		c.pushScope(nil)
		defer c.popScope()
		for i, d := range n.Defaults {
			if d != nil {
				c.expr(d)
			}
			c.bind(n.Params[i])
		}
		c.block(n.Body, true)
	}
}

func (c *lintCollector) booleanOperand(expr ast.Expr, context string) {
	if source, name, ok := c.fallibleExpr(expr); ok {
		c.add(lintErrBool,
			fmt.Sprintf("%s may return Err, which is truthy; handle the Err before using it as %s", name, context), source)
	}
}

func (c *lintCollector) outputOperand(expr ast.Expr, context string) {
	c.eachEmbeddedFallible(expr, func(source ast.Expr, name string) {
		c.add(lintErrOutput,
			fmt.Sprintf("%s may return Err; handle it before passing it to %s", name, context), source)
	})
}

// eachEmbeddedFallible visits every independently fallible source whose Err can
// reach a recursive stringifying sink. Equality and the explicit Err inspectors
// deliberately stop traversal: there the Err is first-class data, not output.
func (c *lintCollector) eachEmbeddedFallible(expr ast.Expr, visit func(ast.Expr, string)) {
	if expr == nil {
		return
	}
	switch n := expr.(type) {
	case *ast.Propagate:
		return // handled before the enclosing sink runs
	case *ast.DefOr:
		c.eachEmbeddedFallible(n.Fallback, visit) // the left Err is handled
		return
	case *ast.ArrayLit:
		for _, elem := range n.Elems {
			c.eachEmbeddedFallible(elem, visit)
		}
		return
	case *ast.MapLit:
		for i := range n.Keys {
			c.eachEmbeddedFallible(n.Keys[i], visit)
			c.eachEmbeddedFallible(n.Vals[i], visit)
		}
		return
	case *ast.Interp:
		for _, part := range n.Parts {
			c.eachEmbeddedFallible(part, visit)
		}
		return
	case *ast.Unary:
		if n.Op == token.MINUS {
			c.eachEmbeddedFallible(n.X, visit)
		}
		return
	case *ast.Binary:
		if n.Op != token.EQ && n.Op != token.NE {
			c.eachEmbeddedFallible(n.L, visit)
			c.eachEmbeddedFallible(n.R, visit)
		}
		return
	case *ast.Logical:
		// Logical operators use truthiness to select and return an operand; they
		// do not recursively render an Err. `and` drops a truthy left Err and
		// returns the RHS; `or` can return that truthy Err unchanged.
		if n.Op == token.OR {
			c.eachEmbeddedFallible(n.L, visit)
		}
		c.eachEmbeddedFallible(n.R, visit)
		return
	case *ast.Index:
		if arr, ok := n.X.(*ast.ArrayLit); ok {
			if idx, ok := n.Idx.(*ast.IntLit); ok {
				i := idx.Value
				if i < 0 {
					i += int64(len(arr.Elems))
				}
				if i >= 0 && i < int64(len(arr.Elems)) {
					c.eachEmbeddedFallible(arr.Elems[i], visit)
				}
				return
			}
		}
		if m, ok := n.X.(*ast.MapLit); ok {
			if selected, found, exact := selectedLiteralMapValue(m, n.Idx, false); exact {
				if found {
					c.eachEmbeddedFallible(selected, visit)
				}
				return
			}
		}
		c.eachEmbeddedFallible(n.X, visit)
		c.eachEmbeddedFallible(n.Idx, visit)
		return
	case *ast.Field:
		if m, ok := n.X.(*ast.MapLit); ok {
			key := &ast.StringLit{Value: n.Name}
			if selected, found, exact := selectedLiteralMapValue(m, key, false); exact {
				if found {
					c.eachEmbeddedFallible(selected, visit)
				}
				return
			}
		}
		c.eachEmbeddedFallible(n.X, visit)
		return
	case *ast.Call:
		if name, ok := identName(n.Callee); ok && !c.isShadowed(name) {
			switch name {
			case "is_err", "err_msg", "err_code", "bool":
				return
			case "str":
				for i, arg := range n.Args {
					if outputSinkArg(name, i) {
						c.eachEmbeddedFallible(arg, visit)
					}
				}
				// str can itself return a resource Err after attempting Display.
				visit(n, name)
				return
			case "format":
				for i, arg := range n.Args {
					if outputSinkArg(name, i) {
						c.eachEmbeddedFallible(arg, visit)
					}
				}
				return
			case "join":
				c.joinEmbeddedFallible(n, n.Args, visit)
				return
			}
		}
	case *ast.Pipe:
		if name, ok := identName(n.Call.Callee); ok && !c.isShadowed(name) {
			switch name {
			case "is_err", "err_msg", "err_code", "bool":
				return
			case "str":
				c.eachEmbeddedFallible(n.Lhs, visit)
				visit(n, name)
				return
			case "format":
				for i, arg := range n.Call.Args {
					if outputSinkArg(name, i+1) {
						c.eachEmbeddedFallible(arg, visit)
					}
				}
				return
			case "join":
				c.joinEmbeddedFallible(n, append([]ast.Expr{n.Lhs}, n.Call.Args...), visit)
				return
			}
		}
	}
	if source, name, ok := c.directFallibleCall(expr); ok {
		visit(source, name)
	}
}

func (c *lintCollector) joinEmbeddedFallible(source ast.Expr, args []ast.Expr, visit func(ast.Expr, string)) {
	if len(args) == 0 {
		return
	}
	arr, literal := args[0].(*ast.ArrayLit)
	if !literal {
		// A fallible non-array first argument is consumed by join's type check;
		// join returns its own Err rather than rendering the original value.
		visit(source, "join")
		if _, _, fallible := c.fallibleExpr(args[0]); !fallible && len(args) > 1 {
			c.eachEmbeddedFallible(args[1], visit)
		}
		return
	}
	for _, elem := range arr.Elems {
		c.eachEmbeddedFallible(elem, visit)
	}
	if len(args) > 1 && len(arr.Elems) > 1 {
		c.eachEmbeddedFallible(args[1], visit)
	}
}

func (c *lintCollector) joinStringifiedOperands(args []ast.Expr, context string) {
	if len(args) == 0 {
		return
	}
	if arr, ok := args[0].(*ast.ArrayLit); ok {
		for _, elem := range arr.Elems {
			c.outputOperand(elem, context)
		}
		if len(args) > 1 && len(arr.Elems) > 1 {
			c.outputOperand(args[1], context)
		}
		return
	}
	if _, _, fallible := c.fallibleExpr(args[0]); !fallible && len(args) > 1 {
		c.outputOperand(args[1], context)
	}
}

// embeddedFallibleExpr recognizes a fallible value even after it has been
// nested in a value that an output sink recursively renders. It intentionally
// does not make collection construction itself suspicious: [read_file("x")]
// remains valid data until it is passed to say/join/write_file/etc.
func (c *lintCollector) embeddedFallibleExpr(expr ast.Expr) (ast.Expr, string, bool) {
	if source, name, ok := c.fallibleExpr(expr); ok {
		return source, name, true
	}
	switch n := expr.(type) {
	case *ast.ArrayLit:
		for _, elem := range n.Elems {
			if source, name, ok := c.embeddedFallibleExpr(elem); ok {
				return source, name, true
			}
		}
	case *ast.MapLit:
		for i := range n.Keys {
			if source, name, ok := c.embeddedFallibleExpr(n.Keys[i]); ok {
				return source, name, true
			}
			if source, name, ok := c.embeddedFallibleExpr(n.Vals[i]); ok {
				return source, name, true
			}
		}
	case *ast.Interp:
		for _, part := range n.Parts {
			if source, name, ok := c.embeddedFallibleExpr(part); ok {
				return source, name, true
			}
		}
	case *ast.Pipe:
		if name, ok := identName(n.Call.Callee); ok && !c.isShadowed(name) {
			if name == "join" {
				return c.firstJoinStringified(append([]ast.Expr{n.Lhs}, n.Call.Args...))
			}
			if outputSinkArg(name, 0) {
				if source, child, ok := c.embeddedFallibleExpr(n.Lhs); ok {
					return source, child, true
				}
			}
			for i, arg := range n.Call.Args {
				if outputSinkArg(name, i+1) {
					if source, child, ok := c.embeddedFallibleExpr(arg); ok {
						return source, child, true
					}
				}
			}
		}
	case *ast.Call:
		if name, ok := identName(n.Callee); ok && !c.isShadowed(name) {
			switch name {
			case "str", "format":
				for i, arg := range n.Args {
					if outputSinkArg(name, i) {
						if source, child, ok := c.embeddedFallibleExpr(arg); ok {
							return source, child, true
						}
					}
				}
			case "join":
				return c.firstJoinStringified(n.Args)
			}
		}
	}
	return nil, "", false
}

func identName(expr ast.Expr) (string, bool) {
	id, ok := expr.(*ast.Ident)
	if !ok {
		return "", false
	}
	return id.Name, true
}

func (c *lintCollector) directFallibleCall(expr ast.Expr) (ast.Expr, string, bool) {
	switch n := expr.(type) {
	case *ast.Call:
		if name, ok := identName(n.Callee); ok && knownFallibleNames[name] && !c.isShadowed(name) {
			return n, name, true
		}
	case *ast.Pipe:
		if name, ok := identName(n.Call.Callee); ok && knownFallibleNames[name] && !c.isShadowed(name) {
			return n, name, true
		}
	}
	return nil, "", false
}

// fallibleExpr recognizes expressions whose result can itself be Err. Explicit
// handlers stop the flow. Equality deliberately consumes Err as first-class data
// and is not suspicious; arithmetic/order/unary/index/field operations propagate
// an Err result and remain recognizable even in a discarded outer expression.
func (c *lintCollector) fallibleExpr(expr ast.Expr) (ast.Expr, string, bool) {
	if source, name, ok := c.directFallibleCall(expr); ok {
		return source, name, true
	}
	switch n := expr.(type) {
	case *ast.Propagate:
		return nil, "", false
	case *ast.DefOr:
		return c.fallibleExpr(n.Fallback)
	case *ast.Unary:
		if n.Op == token.MINUS {
			return c.fallibleExpr(n.X)
		}
	case *ast.Binary:
		switch n.Op {
		case token.PLUS, token.MINUS, token.STAR, token.SLASH, token.PERCENT,
			token.LT, token.LE, token.GT, token.GE, token.SPACESHIP:
			if source, name, ok := c.fallibleExpr(n.L); ok {
				return source, name, true
			}
			return c.fallibleExpr(n.R)
		}
	case *ast.Logical:
		if n.Op == token.OR {
			if source, name, ok := c.fallibleExpr(n.L); ok {
				return source, name, true
			}
		}
		return c.fallibleExpr(n.R)
	case *ast.Index:
		if source, name, ok := c.fallibleExpr(n.X); ok {
			return source, name, true
		}
		if arr, ok := n.X.(*ast.ArrayLit); ok {
			if idx, ok := n.Idx.(*ast.IntLit); ok {
				i := idx.Value
				if i < 0 {
					i += int64(len(arr.Elems))
				}
				if i >= 0 && i < int64(len(arr.Elems)) {
					if source, name, ok := c.fallibleExpr(arr.Elems[i]); ok {
						return source, name, true
					}
				}
			}
		}
		if m, ok := n.X.(*ast.MapLit); ok {
			if selected, found, exact := selectedLiteralMapValue(m, n.Idx, false); exact {
				if found {
					return c.fallibleExpr(selected)
				}
				return nil, "", false
			}
		}
		return c.fallibleExpr(n.Idx)
	case *ast.Field:
		if source, name, ok := c.fallibleExpr(n.X); ok {
			return source, name, true
		}
		if m, ok := n.X.(*ast.MapLit); ok {
			key := &ast.StringLit{Value: n.Name}
			if selected, found, exact := selectedLiteralMapValue(m, key, false); exact {
				if found {
					return c.fallibleExpr(selected)
				}
				return nil, "", false
			}
		}
	}
	return nil, "", false
}

type literalMapKey struct {
	kind byte
	num  int64
	str  string
}

// selectedLiteralMapValue models the runtime map's last-write-wins lookup when
// every key and the lookup key are statically known hashable scalar literals.
// Bare identifiers are strings only in map-key position, never in an index.
func selectedLiteralMapValue(m *ast.MapLit, index ast.Expr, indexBareIdent bool) (ast.Expr, bool, bool) {
	want, ok := staticLiteralMapKey(index, indexBareIdent)
	if !ok {
		return nil, false, false
	}
	keys := make([]literalMapKey, len(m.Keys))
	for i, expr := range m.Keys {
		key, ok := staticLiteralMapKey(expr, true)
		if !ok {
			return nil, false, false
		}
		keys[i] = key
	}
	for i := len(keys) - 1; i >= 0; i-- {
		if keys[i] == want {
			return m.Vals[i], true, true
		}
	}
	return nil, false, true
}

func staticLiteralMapKey(expr ast.Expr, bareIdent bool) (literalMapKey, bool) {
	switch n := expr.(type) {
	case *ast.Ident:
		if bareIdent {
			return literalMapKey{kind: 's', str: n.Name}, true
		}
	case *ast.StringLit:
		return literalMapKey{kind: 's', str: n.Value}, true
	case *ast.IntLit:
		return literalMapKey{kind: 'n', num: n.Value}, true
	case *ast.FloatLit:
		if !math.IsNaN(n.Value) && n.Value >= -0x1p63 && n.Value < 0x1p63 && n.Value == float64(int64(n.Value)) {
			return literalMapKey{kind: 'n', num: int64(n.Value)}, true
		}
	case *ast.BoolLit:
		if n.Value {
			return literalMapKey{kind: 'b', num: 1}, true
		}
		return literalMapKey{kind: 'b'}, true
	}
	return literalMapKey{}, false
}

func escapedWindowsPath(s *ast.StringLit) bool {
	if s.Raw == "" || (s.Form != ast.FormDQuote && s.Form != ast.FormQQ) {
		return false
	}
	body := ""
	switch {
	case strings.HasPrefix(s.Raw, "\"") && strings.HasSuffix(s.Raw, "\""):
		body = s.Raw[1 : len(s.Raw)-1]
	case strings.HasPrefix(s.Raw, "qq") && len(s.Raw) >= 4:
		body = s.Raw[3 : len(s.Raw)-1]
	}
	if len(body) >= 3 && isASCIIAlpha(body[0]) && body[1] == ':' && body[2] == '\\' {
		return true
	}
	return strings.HasPrefix(body, `\\`)
}

func isASCIIAlpha(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

func buildLintSuppressions(src string, comments []lexer.Comment) map[int]map[string]bool {
	result := make(map[int]map[string]bool)
	type directive struct {
		col   int
		codes []string
	}
	directives := make(map[int][]directive)
	for _, comment := range comments {
		codes, ok := lintIgnoreCodes(comment.Text)
		if !ok {
			continue
		}
		directives[comment.Line] = append(directives[comment.Line], directive{col: comment.Col, codes: codes})
	}
	if len(directives) == 0 {
		return result
	}

	pending := make(map[string]bool)
	lineNo := 1
	for start := 0; ; lineNo++ {
		relEnd := strings.IndexByte(src[start:], '\n')
		last := relEnd < 0
		end := len(src)
		if !last {
			end = start + relEnd
		}
		line := src[start:end]
		trimmed := strings.TrimSpace(line)
		isSource := trimmed != "" && !strings.HasPrefix(trimmed, "#")
		if isSource && len(pending) != 0 {
			mergeLintCodes(result, lineNo, pending)
			clear(pending)
		}
		for _, d := range directives[lineNo] {
			if lintCommentIsStandalone(line, d.col) {
				for _, code := range d.codes {
					pending[code] = true
				}
			} else {
				codes := make(map[string]bool, len(d.codes))
				for _, code := range d.codes {
					codes[code] = true
				}
				mergeLintCodes(result, lineNo, codes)
			}
		}
		if last {
			break
		}
		start = end + 1
	}
	return result
}

func mergeLintCodes(result map[int]map[string]bool, line int, codes map[string]bool) {
	if result[line] == nil {
		result[line] = make(map[string]bool, len(codes))
	}
	for code := range codes {
		result[line][code] = true
	}
}

func lintIgnoreCodes(text string) ([]string, bool) {
	body := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "#"))
	const marker = "lint:ignore"
	if !strings.HasPrefix(body, marker) {
		return nil, false
	}
	rest := body[len(marker):]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' && rest[0] != '=' && rest[0] != ':' {
		return nil, false
	}
	rest = strings.TrimSpace(strings.TrimLeft(rest, "=:"))
	if rest == "" {
		return []string{"*"}, true
	}
	codes := strings.FieldsFunc(rest, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	return codes, len(codes) > 0
}

func lintCommentIsStandalone(line string, col int) bool {
	if col < 1 {
		return false
	}
	offset := col - 1
	if offset > len(line) {
		offset = len(line)
	}
	return strings.TrimSpace(line[:offset]) == ""
}
