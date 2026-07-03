package eval

import (
	"testing"

	"github.com/anafalanx/drang/internal/ast"
	"github.com/anafalanx/drang/internal/parser"
)

// pureIdents is the allowlist of bare identifiers FuzzBackendParity permits. It is
// deliberately conservative: only builtins and higher-order functions that are
// deterministic and free of side effects — no process control, filesystem, network,
// clock, RNG, environment, or concurrency. Anything NOT listed (read_file, run, now,
// rand, pmap, http, env, use, sleep, ...) makes the program ineligible, so a fuzz
// input can never drive the test suite to touch the outside world or observe
// nondeterminism. The two backends share the same value types and map ordering, so a
// pure program is guaranteed to agree unless there is a real compiler/VM bug — which
// is exactly what this target hunts for.
//
// Leading-dot identifiers (user functions, e.g. `.fib`) are always allowed: they
// resolve within the program, and any impure builtin they might call would itself
// appear — bare — somewhere in the walked AST and trip this gate.
var pureIdents = map[string]bool{
	// output — both backends write to the same captured buffer
	"say": true,
	// conversions & type predicates
	"int": true, "str": true, "float": true, "bool": true, "type": true,
	"is_err": true, "err_code": true, "err_msg": true,
	// collections
	"len": true, "push": true, "pop": true, "keys": true, "values": true,
	"pairs": true, "has": true, "delete": true, "chars": true, "contains": true,
	"join": true, "take": true, "drop": true, "uniq": true,
	// higher-order — the pure ones only; pmap is excluded (concurrency)
	"map": true, "filter": true, "reject": true, "each": true, "find": true,
	"any": true, "all": true, "count": true, "reduce": true, "flat_map": true,
	"sort": true, "sort_by": true, "min_by": true, "max_by": true,
	// strings
	"split": true, "replace_first": true, "replace_all": true, "trim": true, "upper": true, "lower": true,
	"starts_with": true, "ends_with": true, "format": true, "lines": true,
	"repeat": true, "find_index": true,
	// path string manipulation (no disk access)
	"dirname": true, "basename": true, "ext": true, "stem": true, "to_slash": true,
	"is_abs": true, "clean": true, "rel": true, "within": true, "path_list_sep": true,
	// encoding
	"from_json": true, "to_json": true, "from_csv": true, "to_csv": true,
	"to_hex": true, "from_hex": true, "to_url": true, "from_url": true,
	// math
	"abs": true, "sum": true, "min": true, "max": true, "floor": true, "ceil": true,
	"round": true, "sqrt": true, "pow": true, "log": true, "div": true,
	"sin": true, "cos": true, "tan": true, "asin": true, "acos": true, "atan": true,
	"atan2": true, "exp": true, "log2": true, "log10": true, "hypot": true, "cbrt": true,
	"pi": true, "e": true,
	// regex — compiling and matching are pure
	"re": true, "matches": true, "match": true, "find_all": true,
}

// pureProgram reports whether prog uses only allowlisted operations, so both backends
// can safely run it and are required to agree. It rejects `use`/BEGIN/END outright and
// any bare identifier outside pureIdents. The walk is total over the AST: an
// unrecognized node type is treated as impure, so adding a node kind later fails
// closed (skips) rather than open (runs something unvetted).
func pureProgram(prog *ast.Program) bool {
	pure := true

	var walkExpr func(ast.Expr)
	var walkStmt func(ast.Stmt)
	var walkBlock func(*ast.Block)

	walkBlock = func(b *ast.Block) {
		if b == nil {
			return
		}
		for _, s := range b.Stmts {
			walkStmt(s)
		}
	}

	walkExpr = func(e ast.Expr) {
		if !pure || e == nil {
			return
		}
		switch x := e.(type) {
		case *ast.IntLit, *ast.FloatLit, *ast.StringLit, *ast.BoolLit,
			*ast.RegexLit, *ast.Var:
			// leaves — nothing to check
		case *ast.Ident:
			// Bare builtins must be allowlisted; leading-dot user functions are fine.
			if len(x.Name) > 0 && x.Name[0] != '.' && !pureIdents[x.Name] {
				pure = false
			}
		case *ast.Interp:
			for _, p := range x.Parts {
				walkExpr(p)
			}
		case *ast.Unary:
			walkExpr(x.X)
		case *ast.Binary:
			walkExpr(x.L)
			walkExpr(x.R)
		case *ast.Logical:
			walkExpr(x.L)
			walkExpr(x.R)
		case *ast.DefOr:
			walkExpr(x.X)
			walkExpr(x.Fallback)
		case *ast.Propagate:
			walkExpr(x.X)
		case *ast.Call:
			walkExpr(x.Callee)
			for _, a := range x.Args {
				walkExpr(a)
			}
		case *ast.Pipe:
			walkExpr(x.Lhs)
			walkExpr(x.Call)
		case *ast.Index:
			walkExpr(x.X)
			walkExpr(x.Idx)
		case *ast.Field:
			walkExpr(x.X) // Name is a field key, not a callable identifier
		case *ast.ArrayLit:
			for _, el := range x.Elems {
				walkExpr(el)
			}
		case *ast.MapLit:
			for i := range x.Keys {
				walkExpr(x.Keys[i])
				walkExpr(x.Vals[i])
			}
		case *ast.RangeLit:
			walkExpr(x.Lo)
			walkExpr(x.Hi)
		case *ast.Lambda:
			for _, d := range x.Defaults {
				walkExpr(d)
			}
			walkBlock(x.Body)
		default:
			pure = false // unknown expression kind — fail closed
		}
	}

	walkStmt = func(s ast.Stmt) {
		if !pure || s == nil {
			return
		}
		switch x := s.(type) {
		case *ast.UseStmt, *ast.SpecialBlock, *ast.WhileStmt:
			// Module import / BEGIN-END / while-until: out of scope. `while` (and
			// `until`) are the only unbounded constructs in the language — `for`
			// ranges are finite and runaway recursion is depth-guarded into a clean
			// error — so a pure `while` can be a non-terminating program, which is a
			// hang, not a parity divergence. Excluding it keeps the fuzzer flowing.
			pure = false
		case *ast.BreakStmt, *ast.NextStmt:
			// leaves
		case *ast.ExprStmt:
			walkExpr(x.X)
		case *ast.DeclStmt:
			walkExpr(x.Value)
		case *ast.AssignStmt:
			walkExpr(x.Target)
			walkExpr(x.Value)
		case *ast.ExampleStmt:
			walkExpr(x.Subject)
			walkExpr(x.Want) // nil-safe
		case *ast.IfStmt:
			walkExpr(x.Cond)
			walkBlock(x.Then)
			walkStmt(x.Else) // nil-safe
		case *ast.ForStmt:
			walkExpr(x.Iter)
			walkBlock(x.Body)
		case *ast.FnDecl:
			for _, d := range x.Defaults {
				walkExpr(d)
			}
			walkBlock(x.Body)
		case *ast.ReturnStmt:
			walkExpr(x.Value) // nil-safe
		case *ast.Block:
			walkBlock(x)
		default:
			pure = false // unknown statement kind — fail closed
		}
	}

	for _, s := range prog.Stmts {
		walkStmt(s)
		if !pure {
			return false
		}
	}
	return pure
}

// fuzzParitySeeds seed the corpus with pure programs across the core language:
// arithmetic and comparison, string/collection ops, closures and recursion, the
// higher-order functions, control flow, and error values. These are the shapes where
// VM/walker divergences have historically lived (int-vs-float equality, structural
// equality, recursion). Impure mutations are skipped at runtime by pureProgram.
var fuzzParitySeeds = []string{
	`say(1 + 2 * 3 - 4 / 2)`,
	`say(7 % 3, 2 ** 0, -5, !true)`,
	`say(1 == 1.0, 1 < 2, "a" < "b", [1,2] == [1,2])`,
	`say("ab" ~ "cd", upper("hi"), len("hello"))`,
	`$xs := [3, 1, 2]
say(sort($xs), map($xs, |$x| $x * $x), sum($xs))`,
	`$m := {a: 1, b: 2}
say(keys($m), values($m), has($m, "a"))`,
	`fn .fib($n) {
	if $n < 2 { return $n }
	.fib($n - 1) + .fib($n - 2)
}
say(.fib(15))`,
	`fn .adder($n) { |$x| $x + $n }
$add10 := .adder(10)
say($add10(5))`,
	`$r := int("nope") // -1
say($r, is_err(int("x")))`,
	`$total := reduce([1, 2, 3, 4], 0, |$acc, $x| $acc + $x)
say($total)`,
	`say(filter(1..10, |$n| $n % 2 == 0))`,
	`$s := "a,b,c"
say(split($s, ","), join(["x","y"], "-"))`,
	`for $i in 1..5 { say($i * $i) }`,
	`say([1,2,3][1], {x: 10}.x)`,
	`say(to_json({a: [1, 2], b: "x"}))`,
}

// FuzzBackendParity is the deepest correctness net: the register VM (production) and
// the tree-walking oracle must produce byte-identical stdout and the same
// success/error outcome for every program. The fuzzer explores the pure core of the
// language (see pureProgram / pureIdents); a divergence is a real compiler or VM bug.
//
// Scope note: only side-effect-free, deterministic, terminating programs are run —
// impure, nondeterministic, or unbounded-`while` inputs are skipped, not failed (see
// pureProgram). This keeps the fuzzer flowing instead of stalling on a pure infinite
// loop, at the cost of not mutating `while` bodies; ordinary while-loop parity is
// still covered by the deterministic TestVMParity corpus.
func FuzzBackendParity(f *testing.F) {
	for _, s := range fuzzParitySeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		p := parser.New(src)
		prog := p.ParseProgram()
		if len(p.Errors()) > 0 {
			return // only well-formed programs are in scope
		}
		if !pureProgram(prog) {
			return // touches the world or is nondeterministic — skip
		}
		wOut, wErr := runBackend(t, src, false) // tree-walking oracle
		vOut, vErr := runBackend(t, src, true)  // register VM (production path)
		if wOut != vOut {
			t.Fatalf("output mismatch:\n--- src ---\n%s\n--- walker ---\n%q\n--- vm ---\n%q", src, wOut, vOut)
		}
		if (wErr == nil) != (vErr == nil) {
			t.Fatalf("error-outcome mismatch:\n--- src ---\n%s\n--- walker ---\n%v\n--- vm ---\n%v", src, wErr, vErr)
		}
	})
}
