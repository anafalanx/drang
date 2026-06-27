// Package eval is drang's tree-walking evaluator.
//
// Implemented: programs and brace blocks (lexically scoped, shadowing allowed),
// declarations ($x := e mutable, $x ::= e constant) and assignment, if/else,
// while loops, user functions with closures and explicit/implicit return, calls
// to user functions and builtins, literals, prefix -/!, infix arithmetic/concat/
// comparison, and the scoped error model: errors are values, ? propagates (fails
// loudly / returns the error from the enclosing function), and `or` supplies a
// fallback. (Must-use enforcement is intentionally not implemented.)
//
// Collections: array/map/range values, indexing and field reads, lvalue and
// compound assignment with write-side autovivification, and for-in.
//
// Deferred: stringy numeric coercion (still an open design question). break/
// continue, lambdas, and the orchestration builtins once listed here are now built.
package eval

import (
	_ "embed"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/anafalanx/drang/internal/ast"
	"github.com/anafalanx/drang/internal/parser"
	"github.com/anafalanx/drang/internal/token"
	"github.com/anafalanx/drang/internal/value"
)

// binding is a value plus whether it is a constant.
type binding struct {
	v      value.Value
	frozen bool
	merged bool // imported into this scope by `use` (flat-merge) — not re-exported
}

// Env is a lexical scope chain.
type Env struct {
	vars         map[string]binding
	parent       *Env
	moduleDir    string          // base directory for relative `use` paths (set on the top env per run/module)
	loadingChain map[string]bool // canonical paths being loaded up the import chain (cycle detection)
}

// NewEnv returns a fresh top-level scope.
func NewEnv() *Env { return &Env{vars: map[string]binding{}} }

func (e *Env) child() *Env { return &Env{vars: map[string]binding{}, parent: e} }

// SetModuleDir sets the base directory for resolving relative `use` paths. The CLI
// sets it on the top env: the importing file's directory, or cwd for -e/stdin/REPL.
func (e *Env) SetModuleDir(dir string) { e.moduleDir = dir }

// baseDir returns the nearest module base directory in the env chain (or "").
func (e *Env) baseDir() string {
	for s := e; s != nil; s = s.parent {
		if s.moduleDir != "" {
			return s.moduleDir
		}
	}
	return ""
}

// loading reports whether canon is already being loaded up this import chain — an
// import cycle. It is per-chain (threaded through module envs), not global, so
// concurrent loads of the same module from different goroutines do not collide.
func (e *Env) loading(canon string) bool {
	for s := e; s != nil; s = s.parent {
		if s.loadingChain[canon] {
			return true
		}
	}
	return false
}

// chainWith returns this env's import chain extended with canon, for a module env
// about to be loaded.
func (e *Env) chainWith(canon string) map[string]bool {
	m := map[string]bool{canon: true}
	for s := e; s != nil; s = s.parent {
		for k := range s.loadingChain {
			m[k] = true
		}
	}
	return m
}

// snapshot returns an isolated copy of the env CHAIN (each scope's bindings map
// is copied) so a spawned goroutine reads its own maps and never races the main
// goroutine's ongoing defines/sets. Binding VALUES are shared: frozen constants
// and immutable scalars/strings are safe; a captured MUTABLE container remains
// the documented "pure callback" caveat. This fixes the shared-map data race.
func (e *Env) snapshot() *Env {
	if e == nil {
		return nil
	}
	vars := make(map[string]binding, len(e.vars))
	for k, b := range e.vars {
		vars[k] = b
	}
	return &Env{vars: vars, parent: e.parent.snapshot(), moduleDir: e.moduleDir, loadingChain: e.loadingChain}
}

func (e *Env) get(name string) (value.Value, bool) {
	for s := e; s != nil; s = s.parent {
		if b, ok := s.vars[name]; ok {
			return b.v, true
		}
	}
	return value.MakeNil(), false
}

// define binds name in the current scope (shadowing any outer binding). It
// refuses to redeclare a constant already bound in this same scope.
func (e *Env) define(name string, v value.Value, frozen bool) error {
	if b, ok := e.vars[name]; ok && b.frozen {
		if strings.HasPrefix(name, ".") {
			return fmt.Errorf("cannot redefine %s (it is already defined, e.g. imported by use)", name)
		}
		return fmt.Errorf("cannot redeclare constant $%s", name)
	}
	if frozen {
		value.Freeze(v) // a constant's value is deeply immutable, not just its binding
	}
	e.vars[name] = binding{v: v, frozen: frozen}
	return nil
}

// set reassigns the nearest existing binding of name. It returns whether a
// binding existed, and (when assignment is refused) whether it was a constant.
func (e *Env) set(name string, v value.Value) (ok, frozen bool) {
	for s := e; s != nil; s = s.parent {
		if b, exists := s.vars[name]; exists {
			if b.frozen {
				return false, true
			}
			s.vars[name] = binding{v: v}
			return true, false
		}
	}
	return false, false
}

// Function is a user-defined function value: a closure over its defining scope.
// Proto is its body compiled to bytecode (nil if the body did not compile, in
// which case callFunction tree-walks Body instead).
type Function struct {
	Name     string
	Params   []string
	Defaults []ast.Expr // parallel to Params; a nil entry means the parameter is required
	Body     *ast.Block
	Env      *Env
	Proto    *Proto
}

// vmEnabled controls whether functions compile their bodies to bytecode (and thus
// run on the VM). It defaults on; the parity tests flip it off to obtain a pure
// tree-walking oracle to diff the VM against.
var vmEnabled = true

// newFunction builds a function value, compiling its body to bytecode when the
// compiler can, so the function runs on the VM no matter which backend created it
// (a tree-walked program's functions still get a Proto and run on the VM when
// called). A body the compiler doesn't handle leaves Proto nil and tree-walks.
func newFunction(name string, params []string, defaults []ast.Expr, body *ast.Block, env *Env) *Function {
	fn := &Function{Name: name, Params: params, Defaults: defaults, Body: body, Env: env}
	if vmEnabled {
		// nil shadow set: without whole-program context, assume every name could be
		// shadowed (no direct dispatch). This path is the rare walker fallback;
		// compiled programs route through templates, which carry the real set.
		if proto, ok := compileFunctionBody(params, body, nil); ok {
			fn.Proto = proto
		}
	}
	return fn
}

func (f *Function) TypeName() string { return "function" }

func (f *Function) Display() string {
	if f.Name == "" {
		return "<fn>"
	}
	return "<fn " + f.Name + ">"
}

func (f *Function) Len() int { return 0 }

func (f *Function) Equal(o value.Obj) bool {
	other, ok := o.(*Function)
	return ok && other == f
}

// DeepCopy returns the function itself: functions are frozen code, shared not copied.
func (f *Function) DeepCopy(visited map[value.Obj]value.Obj) value.Obj { return f }

// returnSignal carries a return value up to the function-call boundary.
type returnSignal struct{ v value.Value }

func (returnSignal) Error() string { return "return outside of a function" }

// breakSignal and nextSignal carry loop control up to the nearest enclosing loop,
// which catches them. The parser rejects break/next outside a loop, so a loop in
// the same function always catches them; callFunction converts any that somehow
// escape a function body into a hard error rather than letting them cross into a
// caller's loop.
type breakSignal struct{}

func (breakSignal) Error() string { return "break outside a loop" }

type nextSignal struct{}

func (nextSignal) Error() string { return "next outside a loop" }

// errSignal carries a propagated error (from ?) up to the call boundary, where
// it becomes the enclosing function's error result, or aborts at the top level.
type errSignal struct{ e value.Value }

func (s errSignal) Error() string { return s.e.ErrMsg() }

// exitSignal carries an explicit exit() up to the top of the program — past
// function boundaries, loops, ?, and // — to be recognized only by the CLI, which
// ends the process with the given code. Like the other signals it is never wrapped
// in a posError.
type exitSignal struct{ code int }

func (exitSignal) Error() string { return "exit outside of a program" }

// posError tags a runtime (aborting) error with the source position where it
// occurred, so the CLI can point at the offending line and column. It is a leaf
// error — only the formatter inspects it, and the control-flow signals
// (returnSignal/errSignal) are never wrapped in it.
type posError struct {
	line, col int
	msg       string
}

func (e *posError) Error() string { return e.msg }

// ErrorPos reports a runtime error's source position, if it carries one.
func ErrorPos(err error) (line, col int, ok bool) {
	if pe, ok := err.(*posError); ok {
		return pe.line, pe.col, true
	}
	return 0, 0, false
}

// RunProgram evaluates a whole program against env.
func RunProgram(prog *ast.Program, env *Env) error {
	for _, s := range prog.Stmts {
		if _, err := evalStmt(s, env); err != nil {
			return err
		}
	}
	return nil
}

// NewREPLEnv returns a fresh global env seeded with $ARGV (empty) and $ENV, for
// the interactive REPL.
func NewREPLEnv() *Env {
	env := NewEnv()
	seedArgv(env, nil)
	_ = RunPrelude(env) // the embedded prelude is validated by tests; ignore errors here
	return env
}

// EvalREPL evaluates one REPL submission in env (which persists across inputs) and
// returns the value of its last statement, for the REPL to echo. It runs on the
// tree-walker — the VM's behavioral oracle, so results match normal execution —
// while functions defined here still compile and run on the VM when later called.
func EvalREPL(prog *ast.Program, env *Env) (value.Value, error) {
	last := value.MakeNil()
	for _, s := range prog.Stmts {
		v, err := evalStmt(s, env)
		if err != nil {
			if es, ok := err.(errSignal); ok {
				return es.e, nil // a top-level ? becomes the echoed Err value, not an abort
			}
			return value.MakeNil(), err
		}
		last = v
	}
	return last, nil
}

// RunProgramWithArgs seeds the $ARGV (program arguments) and $ENV (process
// environment) globals, then runs the program. The CLI uses this; tests that
// don't need argv may call RunProgram directly.
func RunProgramWithArgs(prog *ast.Program, env *Env, argv []string) error {
	seedArgv(env, argv)
	if err := RunPrelude(env); err != nil {
		return err
	}
	return RunProgramVM(prog, env) // the VM is the production path (walker fallback if uncompilable)
}

//go:embed prelude.dr
var preludeSource string

var (
	preludeOnce sync.Once
	preludeProg *ast.Program
	preludeErr  error
)

// RunPrelude evaluates the embedded drang standard library into env, defining its
// functions as globals before the user program runs. The prelude source is fixed at
// build time, so it is parsed once per process and then evaluated into each env.
func RunPrelude(env *Env) error {
	preludeOnce.Do(func() {
		p := parser.NewStdlib(preludeSource) // the prelude defines bare stdlib functions
		preludeProg = p.ParseProgram()
		if errs := p.Errors(); len(errs) > 0 {
			preludeErr = fmt.Errorf("prelude parse error: %s", strings.Join(errs, "; "))
		}
	})
	if preludeErr != nil {
		return preludeErr
	}
	// Run on the VM so the prelude's functions compile with its real bound-name set
	// (direct builtin dispatch), exactly like normal user code, rather than the
	// walker's shadowed=nil path (correct but slower — each builtin call would fall
	// back through an env lookup).
	return RunProgramVM(preludeProg, env)
}

func seedArgv(env *Env, argv []string) {
	items := make([]value.Value, len(argv))
	for i, a := range argv {
		items[i] = value.MakeStr(a)
	}
	_ = env.define("ARGV", value.MakeArray(items), false)
	m := value.MakeMap()
	om := m.Obj().(*value.OrderedMap)
	for _, kv := range os.Environ() {
		if eq := strings.IndexByte(kv, '='); eq >= 0 {
			om.Set(value.MakeStr(kv[:eq]), value.MakeStr(kv[eq+1:]))
		}
	}
	_ = env.define("ENV", m, false)
}

// ExitCode maps a top-level error to a process exit code: an error propagated by
// ? carries the failing Err's code (clamped to 1..255); any other error is 1.
func ExitCode(err error) int {
	if es, ok := err.(errSignal); ok {
		return clampCode(es.e.ErrCode())
	}
	return 1
}

// ExitRequested reports whether err is an explicit exit()/die() and its code, so
// the CLI can end the process cleanly without printing an error.
func ExitRequested(err error) (code int, ok bool) {
	if es, ok := err.(exitSignal); ok {
		return es.code, true
	}
	return 0, false
}

func evalStmt(s ast.Stmt, env *Env) (value.Value, error) {
	switch n := s.(type) {
	case *ast.DeclStmt:
		v, err := evalExpr(n.Value, env)
		if err != nil {
			return value.MakeNil(), err
		}
		if err := env.define(n.Name, v, n.Const); err != nil {
			return value.MakeNil(), err
		}
		return v, nil
	case *ast.AssignStmt:
		return evalAssign(n, env)
	case *ast.FnDecl:
		fn := newFunction(n.Name, n.Params, n.Defaults, n.Body, env)
		if err := env.define(n.Name, value.MakeObj(value.Func, fn), false); err != nil {
			return value.MakeNil(), err
		}
		return value.MakeNil(), nil
	case *ast.ReturnStmt:
		rv := value.MakeNil()
		if n.Value != nil {
			v, err := evalExpr(n.Value, env)
			if err != nil {
				return value.MakeNil(), err
			}
			rv = v
		}
		return value.MakeNil(), returnSignal{v: rv}
	case *ast.BreakStmt:
		return value.MakeNil(), breakSignal{}
	case *ast.NextStmt:
		return value.MakeNil(), nextSignal{}
	case *ast.ExprStmt:
		return evalExpr(n.X, env)
	case *ast.IfStmt:
		return evalIf(n, env)
	case *ast.WhileStmt:
		return evalWhile(n, env)
	case *ast.ForStmt:
		return evalFor(n, env)
	case *ast.SpecialBlock:
		return value.MakeNil(), fmt.Errorf("%s blocks are only valid in -n/-p (one-liner) mode", n.Name)
	case *ast.UseStmt:
		return value.MakeNil(), mergeModule(n, env)
	case *ast.ExampleStmt:
		return value.MakeNil(), nil // a `drang test` assertion is a no-op in a normal run
	}
	return value.MakeNil(), fmt.Errorf("eval: unknown statement %T", s)
}

func evalBlock(b *ast.Block, env *Env) (value.Value, error) {
	last := value.MakeNil()
	for _, s := range b.Stmts {
		v, err := evalStmt(s, env)
		if err != nil {
			return value.MakeNil(), err
		}
		last = v
	}
	return last, nil
}

func evalIf(n *ast.IfStmt, env *Env) (value.Value, error) {
	c, err := evalExpr(n.Cond, env)
	if err != nil {
		return value.MakeNil(), err
	}
	if c.Truthy() {
		return evalBlock(n.Then, env.child())
	}
	switch e := n.Else.(type) {
	case *ast.Block:
		return evalBlock(e, env.child())
	case *ast.IfStmt:
		return evalIf(e, env)
	}
	return value.MakeNil(), nil
}

func evalWhile(n *ast.WhileStmt, env *Env) (value.Value, error) {
	for {
		c, err := evalExpr(n.Cond, env)
		if err != nil {
			return value.MakeNil(), err
		}
		if !c.Truthy() {
			return value.MakeNil(), nil
		}
		if _, err := evalBlock(n.Body, env.child()); err != nil {
			switch err.(type) {
			case breakSignal:
				return value.MakeNil(), nil
			case nextSignal:
				continue
			}
			return value.MakeNil(), err
		}
	}
}

// evalFor iterates arrays (element, or index+element), maps (value, or
// key+value), ranges, and strings (by rune). The iterable is snapshotted before
// the loop, and each iteration runs the body in a fresh child scope so the loop
// var(s) are distinct per pass.
func evalFor(n *ast.ForStmt, env *Env) (value.Value, error) {
	iter, err := evalExpr(n.Iter, env)
	if err != nil {
		return value.MakeNil(), err
	}
	two := len(n.Vars) == 2
	// bind runs one iteration; stop is true when the loop should end (break, or a
	// real error in err). next is absorbed here by returning stop=false.
	bind := func(a, b value.Value) (stop bool, err error) {
		child := env.child()
		if err := child.define(n.Vars[0], a, false); err != nil {
			return true, err
		}
		if two {
			if err := child.define(n.Vars[1], b, false); err != nil {
				return true, err
			}
		}
		if _, err := evalBlock(n.Body, child); err != nil {
			switch err.(type) {
			case breakSignal:
				return true, nil
			case nextSignal:
				return false, nil
			}
			return true, err
		}
		return false, nil
	}
	switch iter.Tag() {
	case value.Arr:
		// Copy the elements so the loop iterates a true snapshot: neither a
		// length change nor an in-place element overwrite in the body affects it.
		elems := append([]value.Value(nil), iter.Obj().(*value.Array).Elems...)
		for i, el := range elems {
			var stop bool
			var err error
			if two {
				stop, err = bind(value.MakeInt(int64(i)), el)
			} else {
				stop, err = bind(el, value.MakeNil())
			}
			if err != nil {
				return value.MakeNil(), err
			}
			if stop {
				break
			}
		}
	case value.Map:
		m := iter.Obj().(*value.OrderedMap)
		// Copy: Keys()/Vals() expose the live backing slices, which a delete or
		// insert in the body would otherwise shift out from under the loop.
		keys := append([]value.Value(nil), m.Keys()...)
		vals := append([]value.Value(nil), m.Vals()...)
		for i := range keys {
			var stop bool
			var err error
			if two {
				stop, err = bind(keys[i], vals[i])
			} else {
				stop, err = bind(vals[i], value.MakeNil()) // one-var over a map yields values
			}
			if err != nil {
				return value.MakeNil(), err
			}
			if stop {
				break
			}
		}
	case value.Range:
		r := iter.Obj().(*value.IntRange)
		if r.Hi >= r.Lo {
			idx := int64(0)
			// Break after binding r.Hi rather than incrementing past it, so a
			// range ending at the maximum int64 cannot overflow into an endless loop.
			for v := r.Lo; ; v, idx = v+1, idx+1 {
				var stop bool
				var err error
				if two {
					stop, err = bind(value.MakeInt(idx), value.MakeInt(v))
				} else {
					stop, err = bind(value.MakeInt(v), value.MakeNil())
				}
				if err != nil {
					return value.MakeNil(), err
				}
				if stop {
					break
				}
				if v == r.Hi {
					break
				}
			}
		}
	case value.Str:
		idx := int64(0)
		for _, rn := range iter.AsStr() { // ranging a string yields runes
			rs := value.MakeStr(string(rn))
			var stop bool
			var err error
			if two {
				stop, err = bind(value.MakeInt(idx), rs)
			} else {
				stop, err = bind(rs, value.MakeNil())
			}
			if err != nil {
				return value.MakeNil(), err
			}
			if stop {
				break
			}
			idx++
		}
	default:
		return value.MakeNil(), fmt.Errorf("cannot iterate over a %s", iter.TypeName())
	}
	return value.MakeNil(), nil
}

func evalAssign(n *ast.AssignStmt, env *Env) (value.Value, error) {
	rhs, err := evalExpr(n.Value, env)
	if err != nil {
		return value.MakeNil(), err
	}
	switch t := n.Target.(type) {
	case *ast.Var:
		return assignVar(t.Name, n.Op, rhs, env)
	case *ast.Index:
		k, err := evalExpr(t.Idx, env)
		if err != nil {
			return value.MakeNil(), err
		}
		container, err := resolveContainer(t.X, kindFor(k), env)
		if err != nil {
			return value.MakeNil(), err
		}
		return assignSlot(container, k, n.Op, rhs)
	case *ast.Field:
		container, err := resolveContainer(t.X, value.Map, env)
		if err != nil {
			return value.MakeNil(), err
		}
		return assignSlot(container, value.MakeStr(t.Name), n.Op, rhs)
	}
	return value.MakeNil(), fmt.Errorf("eval: cannot assign to %T", n.Target)
}

func assignVar(name string, op token.Kind, rhs value.Value, env *Env) (value.Value, error) {
	newv := rhs
	if op != token.ILLEGAL {
		cur, _ := env.get(name)
		nv, err := compound(op, cur, rhs)
		if err != nil {
			return value.MakeNil(), err
		}
		newv = nv
	}
	set, frozen := env.set(name, newv)
	if !set {
		if frozen {
			return value.MakeNil(), fmt.Errorf("cannot assign to constant $%s", name)
		}
		return value.MakeNil(), fmt.Errorf("undefined variable $%s (declare it with ':=' first)", name)
	}
	return newv, nil
}

// compound applies a compound-assignment op; an undef current value seeds 0.
func compound(op token.Kind, cur, rhs value.Value) (value.Value, error) {
	if cur.Tag() == value.Nil {
		cur = value.MakeInt(0)
	}
	return arith(op, cur, rhs)
}

// kindFor picks the container to autovivify for a key: int key -> array, else map.
func kindFor(key value.Value) value.Tag {
	if key.Tag() == value.Int {
		return value.Arr
	}
	return value.Map
}

func newContainer(kind value.Tag) value.Value {
	if kind == value.Arr {
		return value.MakeArray(nil)
	}
	return value.MakeMap()
}

// containerForWrite decides the container to write through for an lvalue base
// holding cur: an existing array/map is used as-is; nil autovivifies a fresh
// container of neededKind; anything else is an error. created reports that a new
// container was made, which the caller persists back to the base's slot or var.
// Shared by the walker's resolveContainer and the VM's OpResolve*Container.
func containerForWrite(cur value.Value, neededKind value.Tag, name string) (cont value.Value, created bool, err error) {
	switch cur.Tag() {
	case value.Arr, value.Map:
		return cur, false, nil
	case value.Nil:
		return newContainer(neededKind), true, nil
	}
	return value.MakeNil(), false, fmt.Errorf("cannot index-assign through $%s (a %s)", name, cur.TypeName())
}

// assignSlot writes (or compound-updates) container[key].
func assignSlot(container, key value.Value, op token.Kind, rhs value.Value) (value.Value, error) {
	switch container.Tag() {
	case value.Arr:
		a := container.Obj().(*value.Array)
		if a.IsFrozen() {
			return value.MakeNil(), fmt.Errorf("cannot modify a frozen array")
		}
		if key.Tag() != value.Int {
			return value.MakeNil(), fmt.Errorf("array index must be an int, got %s", key.TypeName())
		}
		length := int64(a.Len())
		i := key.AsInt()
		if i < 0 {
			i += length
		}
		newv := rhs
		if op != token.ILLEGAL {
			cur := value.MakeNil()
			if i >= 0 && i < length {
				cur = a.Elems[i]
			}
			nv, err := compound(op, cur, rhs)
			if err != nil {
				return value.MakeNil(), err
			}
			newv = nv
		}
		if i >= 0 && i < length {
			a.Elems[i] = newv
			return newv, nil
		}
		if i == length {
			a.Elems = append(a.Elems, newv)
			return newv, nil
		}
		return value.MakeNil(), fmt.Errorf("index %d past end of array (len %d)", key.AsInt(), length)
	case value.Map:
		if !value.Hashable(key) {
			return value.MakeNil(), fmt.Errorf("unhashable map key: %s", key.TypeName())
		}
		m := container.Obj().(*value.OrderedMap)
		if m.IsFrozen() {
			return value.MakeNil(), fmt.Errorf("cannot modify a frozen map")
		}
		newv := rhs
		if op != token.ILLEGAL {
			cur, _ := m.Get(key)
			nv, err := compound(op, cur, rhs)
			if err != nil {
				return value.MakeNil(), err
			}
			newv = nv
		}
		m.Set(key, newv)
		return newv, nil
	}
	return value.MakeNil(), fmt.Errorf("cannot index-assign into a %s", container.TypeName())
}

// resolveContainer returns the array/map that e denotes as an assignment target,
// autovivifying undef bases/slots along the write path (kind chosen by the next
// access's key; neededKind is used when e itself is undef).
func resolveContainer(e ast.Expr, neededKind value.Tag, env *Env) (value.Value, error) {
	switch t := e.(type) {
	case *ast.Var:
		v, _ := env.get(t.Name)
		cont, created, err := containerForWrite(v, neededKind, t.Name)
		if err != nil {
			return value.MakeNil(), err
		}
		if created {
			set, frozen := env.set(t.Name, cont)
			if !set {
				if frozen {
					return value.MakeNil(), fmt.Errorf("cannot assign to constant $%s", t.Name)
				}
				return value.MakeNil(), fmt.Errorf("undefined variable $%s (declare it with ':=' first)", t.Name)
			}
		}
		return cont, nil
	case *ast.Index:
		k, err := evalExpr(t.Idx, env)
		if err != nil {
			return value.MakeNil(), err
		}
		parent, err := resolveContainer(t.X, kindFor(k), env)
		if err != nil {
			return value.MakeNil(), err
		}
		return resolveSlot(parent, k, neededKind)
	case *ast.Field:
		parent, err := resolveContainer(t.X, value.Map, env)
		if err != nil {
			return value.MakeNil(), err
		}
		return resolveSlot(parent, value.MakeStr(t.Name), neededKind)
	}
	return value.MakeNil(), fmt.Errorf("cannot assign through %T", e)
}

// resolveSlot reads parent[key] as a container, autovivifying it if undef.
func resolveSlot(parent, key value.Value, neededKind value.Tag) (value.Value, error) {
	cur := slotRead(parent, key)
	switch cur.Tag() {
	case value.Arr, value.Map:
		return cur, nil
	case value.Nil:
		c := newContainer(neededKind)
		if _, err := assignSlot(parent, key, token.ILLEGAL, c); err != nil {
			return value.MakeNil(), err
		}
		return c, nil
	}
	return value.MakeNil(), fmt.Errorf("cannot index-assign through a %s", cur.TypeName())
}

// slotRead reads container[key] for the walker; absent/out-of-range yields undef.
func slotRead(container, key value.Value) value.Value {
	switch container.Tag() {
	case value.Arr:
		a := container.Obj().(*value.Array)
		if key.Tag() != value.Int {
			return value.MakeNil()
		}
		length := int64(a.Len())
		i := key.AsInt()
		if i < 0 {
			i += length
		}
		if i >= 0 && i < length {
			return a.Elems[i]
		}
		return value.MakeNil()
	case value.Map:
		m := container.Obj().(*value.OrderedMap)
		v, _ := m.Get(key)
		return v
	}
	return value.MakeNil()
}

// evalIndexRead reads c[k]: array index (negative ok, out-of-bounds is an Err
// value), map key (miss is undef), and an Err for any other container/key. It
// short-circuits when the container is itself an error (without evaluating the
// index), then defers to indexRead — the shared helper the VM's OpIndex also uses.
func evalIndexRead(n *ast.Index, env *Env) (value.Value, error) {
	c, err := evalExpr(n.X, env)
	if err != nil {
		return value.MakeNil(), err
	}
	if c.IsErr() {
		return c, nil // pass the original error through unchanged
	}
	k, err := evalExpr(n.Idx, env)
	if err != nil {
		return value.MakeNil(), err
	}
	return indexRead(c, k), nil
}

// indexRead is the container[key] lookup, on already-evaluated values. Shared by
// the tree-walker and the VM so indexing behaves identically on either backend.
func indexRead(c, k value.Value) value.Value {
	if c.IsErr() {
		return c
	}
	if k.IsErr() {
		return k
	}
	switch c.Tag() {
	case value.Arr:
		a := c.Obj().(*value.Array)
		switch k.Tag() {
		case value.Int:
			i := k.AsInt()
			length := int64(a.Len())
			if i < 0 {
				i += length
			}
			if i < 0 || i >= length {
				return value.MakeErr(fmt.Sprintf("index %d out of range (len %d)", k.AsInt(), a.Len()), 1)
			}
			return a.Elems[i]
		case value.Range:
			r := k.Obj().(*value.IntRange)
			return arraySlice(a, r.Lo, r.Hi)
		default:
			return value.MakeErr(fmt.Sprintf("array index must be an int or range, got %s", k.TypeName()), 1)
		}
	case value.Str:
		s := c.AsStr()
		switch k.Tag() {
		case value.Int:
			return stringIndex(s, k.AsInt())
		case value.Range:
			r := k.Obj().(*value.IntRange)
			return stringSlice(s, r.Lo, r.Hi)
		default:
			return value.MakeErr(fmt.Sprintf("string index must be an int or range, got %s", k.TypeName()), 1)
		}
	case value.Map:
		if !value.Hashable(k) {
			return value.MakeErr("unhashable map key: "+k.TypeName(), 1)
		}
		m := c.Obj().(*value.OrderedMap)
		v, _ := m.Get(k)
		return v // missing key -> undef
	default:
		return value.MakeErr(fmt.Sprintf("cannot index a %s", c.TypeName()), 1)
	}
}

// clampSlice resolves a lo..hi range (inclusive, matching drang ranges) over a
// sequence of length n: negatives count from the end, bounds clamp, and an empty or
// reversed range yields ok=false. On ok, [from, to) is the half-open Go slice range.
func clampSlice(lo, hi, n int64) (from, to int64, ok bool) {
	if lo < 0 {
		lo += n
	}
	if hi < 0 {
		hi += n
	}
	if lo < 0 {
		lo = 0
	}
	if hi >= n {
		hi = n - 1
	}
	if lo > hi || lo >= n {
		return 0, 0, false
	}
	return lo, hi + 1, true
}

// arraySlice returns a new array holding a[lo..hi] (inclusive); out-of-range bounds
// clamp and an empty/reversed range yields an empty array.
func arraySlice(a *value.Array, lo, hi int64) value.Value {
	from, to, ok := clampSlice(lo, hi, int64(len(a.Elems)))
	if !ok {
		return value.MakeArray(nil)
	}
	out := make([]value.Value, to-from)
	copy(out, a.Elems[from:to])
	return value.MakeArray(out)
}

// stringIndex returns the i-th rune of s as a one-character string (negatives count
// from the end); an out-of-range index is an Err.
func stringIndex(s string, i int64) value.Value {
	rs := []rune(s)
	n := int64(len(rs))
	idx := i
	if idx < 0 {
		idx += n
	}
	if idx < 0 || idx >= n {
		// Report the original index and use the same wording as the array path.
		return value.MakeErr(fmt.Sprintf("index %d out of range (len %d)", i, n), 1)
	}
	return value.MakeStr(string(rs[idx]))
}

// stringSlice returns the substring s[lo..hi] by rune (inclusive); bounds clamp and
// an empty/reversed range yields "".
func stringSlice(s string, lo, hi int64) value.Value {
	rs := []rune(s)
	from, to, ok := clampSlice(lo, hi, int64(len(rs)))
	if !ok {
		return value.MakeStr("")
	}
	return value.MakeStr(string(rs[from:to]))
}

// evalFieldRead reads c.name: a string-keyed map lookup (miss is undef), else Err.
func evalFieldRead(n *ast.Field, env *Env) (value.Value, error) {
	c, err := evalExpr(n.X, env)
	if err != nil {
		return value.MakeNil(), err
	}
	return fieldRead(c, n.Name), nil
}

// fieldRead is the c.name lookup, on an already-evaluated container. Shared by the
// tree-walker and the VM's OpField.
func fieldRead(c value.Value, name string) value.Value {
	if c.IsErr() {
		return c
	}
	if c.Tag() == value.Map {
		m := c.Obj().(*value.OrderedMap)
		v, _ := m.Get(value.MakeStr(name))
		return v // missing -> undef
	}
	return value.MakeErr(fmt.Sprintf("cannot access field .%s of %s", name, c.TypeName()), 1)
}

func evalExpr(e ast.Expr, env *Env) (value.Value, error) {
	switch n := e.(type) {
	case *ast.IntLit:
		return value.MakeInt(n.Value), nil
	case *ast.FloatLit:
		return value.MakeFloat(n.Value), nil
	case *ast.StringLit:
		return value.MakeStr(n.Value), nil
	case *ast.BoolLit:
		return value.MakeBool(n.Value), nil
	case *ast.RegexLit:
		return makeRegex(n.Pattern), nil // compiled via the shared cache
	case *ast.Var:
		v, ok := env.get(n.Name)
		if !ok {
			return value.MakeNil(), fmt.Errorf("undefined variable $%s", n.Name)
		}
		return v, nil
	case *ast.Ident:
		if v, ok := env.get(n.Name); ok {
			return v, nil
		}
		return value.MakeNil(), fmt.Errorf("undefined: %s", n.Name)
	case *ast.Unary:
		return evalUnary(n, env)
	case *ast.Binary:
		return evalBinary(n, env)
	case *ast.Call:
		return evalCall(n, env)
	case *ast.Propagate:
		v, err := evalExpr(n.X, env)
		if err != nil {
			return value.MakeNil(), err
		}
		if v.IsErr() {
			return value.MakeNil(), errSignal{e: v}
		}
		return v, nil
	case *ast.Logical:
		l, err := evalExpr(n.L, env)
		if err != nil {
			return value.MakeNil(), err
		}
		switch n.Op {
		case token.OR:
			if l.Truthy() {
				return l, nil // short-circuit: keep the first truthy operand
			}
		case token.AND:
			if !l.Truthy() {
				return l, nil // short-circuit: keep the first falsy operand
			}
		}
		return evalExpr(n.R, env)
	case *ast.ArrayLit:
		elems := make([]value.Value, len(n.Elems))
		for i, el := range n.Elems {
			v, err := evalExpr(el, env)
			if err != nil {
				return value.MakeNil(), err
			}
			elems[i] = v
		}
		return value.MakeArray(elems), nil
	case *ast.MapLit:
		m := value.MakeMap()
		om := m.Obj().(*value.OrderedMap)
		for i := range n.Keys {
			var k value.Value
			if id, ok := n.Keys[i].(*ast.Ident); ok {
				// A bare identifier key is its name as a string ({cwd: x} == {"cwd": x}),
				// matching $m.cwd field access; use $var or a quoted/expr key otherwise.
				k = value.MakeStr(id.Name)
			} else {
				kv, err := evalExpr(n.Keys[i], env)
				if err != nil {
					return value.MakeNil(), err
				}
				k = kv
			}
			if !value.Hashable(k) {
				return value.MakeErr("unhashable map key: "+k.TypeName(), 1), nil
			}
			v, err := evalExpr(n.Vals[i], env)
			if err != nil {
				return value.MakeNil(), err
			}
			om.Set(k, v)
		}
		return m, nil
	case *ast.RangeLit:
		lo, err := evalExpr(n.Lo, env)
		if err != nil {
			return value.MakeNil(), err
		}
		hi, err := evalExpr(n.Hi, env)
		if err != nil {
			return value.MakeNil(), err
		}
		if lo.Tag() != value.Int || hi.Tag() != value.Int {
			return value.MakeErr(fmt.Sprintf("range bounds must be ints, got %s..%s", lo.TypeName(), hi.TypeName()), 1), nil
		}
		return value.MakeRange(lo.AsInt(), hi.AsInt()), nil
	case *ast.Index:
		return evalIndexRead(n, env)
	case *ast.Field:
		return evalFieldRead(n, env)
	case *ast.DefOr:
		v, err := evalExpr(n.X, env)
		if err != nil {
			return value.MakeNil(), err
		}
		if v.IsErr() || v.Tag() == value.Nil {
			return evalExpr(n.Fallback, env)
		}
		return v, nil
	case *ast.Lambda:
		return value.MakeObj(value.Func, newFunction("", n.Params, n.Defaults, n.Body, env)), nil
	}
	return value.MakeNil(), fmt.Errorf("eval: unknown expression %T", e)
}

func evalUnary(n *ast.Unary, env *Env) (value.Value, error) {
	x, err := evalExpr(n.X, env)
	if err != nil {
		return value.MakeNil(), err
	}
	switch n.Op {
	case token.MINUS:
		switch x.Tag() {
		case value.Int:
			return value.MakeInt(-x.AsInt()), nil
		case value.Float:
			return value.MakeFloat(-x.AsFloat()), nil
		}
		return value.MakeNil(), fmt.Errorf("cannot negate %s", x.TypeName())
	case token.BANG:
		return value.MakeBool(!x.Truthy()), nil
	}
	return value.MakeNil(), fmt.Errorf("eval: unknown unary op %s", n.Op)
}

func evalBinary(n *ast.Binary, env *Env) (value.Value, error) {
	l, err := evalExpr(n.L, env)
	if err != nil {
		return value.MakeNil(), err
	}
	r, err := evalExpr(n.R, env)
	if err != nil {
		return value.MakeNil(), err
	}
	switch n.Op {
	case token.TILDE:
		return value.MakeStr(l.Display() + r.Display()), nil
	case token.PLUS, token.MINUS, token.STAR, token.SLASH, token.PERCENT:
		return arith(n.Op, l, r)
	case token.EQ:
		return value.MakeBool(equal(l, r)), nil
	case token.NE:
		return value.MakeBool(!equal(l, r)), nil
	case token.LT, token.LE, token.GT, token.GE:
		return compare(n.Op, l, r)
	case token.SPACESHIP:
		cmp, err := threeway(l, r)
		if err != nil {
			return value.MakeNil(), err
		}
		return value.MakeInt(int64(cmp)), nil
	}
	return value.MakeNil(), fmt.Errorf("eval: unknown binary op %s", n.Op)
}

// addOverflows/subOverflows/mulOverflows report int64 overflow for +/-/*, so
// integer arithmetic fails loudly (like division/modulo by zero) instead of
// wrapping silently — a footgun for byte/size math.
func addOverflows(a, b int64) bool {
	s := a + b
	return (a > 0 && b > 0 && s < 0) || (a < 0 && b < 0 && s >= 0)
}

func subOverflows(a, b int64) bool {
	return (b < 0 && a > math.MaxInt64+b) || (b > 0 && a < math.MinInt64+b)
}

func mulOverflows(a, b int64) bool {
	if a == 0 || b == 0 {
		return false
	}
	if (a == -1 && b == math.MinInt64) || (b == -1 && a == math.MinInt64) {
		return true
	}
	return a*b/a != b
}

func arith(op token.Kind, l, r value.Value) (value.Value, error) {
	if !l.IsNumber() || !r.IsNumber() {
		return value.MakeNil(), fmt.Errorf("cannot use %s and %s with '%s' (stringy coercion is a later slice)",
			l.TypeName(), r.TypeName(), opSym(op))
	}
	if l.Tag() == value.Int && r.Tag() == value.Int && op != token.SLASH {
		a, b := l.AsInt(), r.AsInt()
		switch op {
		case token.PLUS:
			if addOverflows(a, b) {
				return value.MakeNil(), fmt.Errorf("integer overflow: %d + %d", a, b)
			}
			return value.MakeInt(a + b), nil
		case token.MINUS:
			if subOverflows(a, b) {
				return value.MakeNil(), fmt.Errorf("integer overflow: %d - %d", a, b)
			}
			return value.MakeInt(a - b), nil
		case token.STAR:
			if mulOverflows(a, b) {
				return value.MakeNil(), fmt.Errorf("integer overflow: %d * %d", a, b)
			}
			return value.MakeInt(a * b), nil
		case token.PERCENT:
			if b == 0 {
				return value.MakeNil(), fmt.Errorf("modulo by zero")
			}
			return value.MakeInt(a % b), nil
		}
	}
	if op == token.PERCENT {
		return value.MakeNil(), fmt.Errorf("'%%' requires integer operands")
	}
	a, b := l.Num(), r.Num()
	switch op {
	case token.PLUS:
		return value.MakeFloat(a + b), nil
	case token.MINUS:
		return value.MakeFloat(a - b), nil
	case token.STAR:
		return value.MakeFloat(a * b), nil
	case token.SLASH:
		if b == 0 {
			return value.MakeNil(), fmt.Errorf("division by zero")
		}
		return value.MakeFloat(a / b), nil
	}
	return value.MakeNil(), fmt.Errorf("eval: bad arithmetic op %s", op)
}

func equal(l, r value.Value) bool { return value.Equal(l, r) }

// threeway returns -1, 0, or 1 for l<r, l==r, l>r. Numbers compare numerically
// and strings lexicographically; any other pairing (or a mismatch) is an error.
// It is the shared core of compare, the <=> operator, and the ordering builtins.
func threeway(l, r value.Value) (int, error) {
	switch {
	case l.IsNumber() && r.IsNumber():
		a, b := l.Num(), r.Num()
		switch {
		case a < b:
			return -1, nil
		case a > b:
			return 1, nil
		}
		return 0, nil
	case l.Tag() == value.Str && r.Tag() == value.Str:
		return strings.Compare(l.AsStr(), r.AsStr()), nil
	}
	return 0, fmt.Errorf("cannot compare %s and %s", l.TypeName(), r.TypeName())
}

func compare(op token.Kind, l, r value.Value) (value.Value, error) {
	cmp, err := threeway(l, r)
	if err != nil {
		return value.MakeNil(), err
	}
	switch op {
	case token.LT:
		return value.MakeBool(cmp < 0), nil
	case token.LE:
		return value.MakeBool(cmp <= 0), nil
	case token.GT:
		return value.MakeBool(cmp > 0), nil
	case token.GE:
		return value.MakeBool(cmp >= 0), nil
	}
	return value.MakeNil(), fmt.Errorf("eval: bad comparison op %s", op)
}

func evalCall(n *ast.Call, env *Env) (value.Value, error) {
	args := make([]value.Value, len(n.Args))
	for i, a := range n.Args {
		v, err := evalExpr(a, env)
		if err != nil {
			return value.MakeNil(), err
		}
		args[i] = v
	}
	if id, ok := n.Callee.(*ast.Ident); ok {
		return resolveAndCall(id.Name, args, env)
	}
	cv, err := evalExpr(n.Callee, env)
	if err != nil {
		return value.MakeNil(), err
	}
	if fn, ok := asFunction(cv); ok {
		return callFunction(fn, args)
	}
	return value.MakeNil(), fmt.Errorf("cannot call a %s", cv.TypeName())
}

// resolveAndCall reproduces evalCall's Ident-callee resolution order — a user
// binding (which shadows builtins) first, then dispatch/spawn/HOFs, then a
// builtin, else unknown. It is the single call seam shared by the tree-walker
// and the bytecode VM, so a call resolves identically from either backend.
func resolveAndCall(name string, args []value.Value, env *Env) (value.Value, error) {
	if cv, found := env.get(name); found {
		if fn, ok := asFunction(cv); ok {
			return callFunction(fn, args)
		}
		return value.MakeNil(), fmt.Errorf("%s is not a function (it is a %s)", name, cv.TypeName())
	}
	return dispatchNonUser(name, args, env)
}

// dispatchNonUser resolves a call to a non-user target — dispatch/spawn/HOFs/
// builtins — skipping the env lookup. The VM emits a direct call to this (via
// OpCallBuiltin) only when whole-program analysis proves the name is never bound by
// the user, so env.get would always miss anyway.
func dispatchNonUser(name string, args []value.Value, env *Env) (value.Value, error) {
	if name == "dispatch" {
		return evalDispatch(args, env)
	}
	if name == "spawn" {
		return evalSpawn(args)
	}
	if name == "each_line" {
		return evalEachLine(args)
	}
	if name == "use" {
		return evalUse(args, env)
	}
	if hofNames[name] {
		return evalHOF(name, args, env)
	}
	if b, ok := builtins[name]; ok {
		return safeBuiltin(name, b, args)
	}
	return value.MakeNil(), fmt.Errorf("unknown function %s", name)
}

// isNonUserName reports whether name is a builtin, HOF, or special form (i.e. not
// a user-defined function) — a candidate for direct dispatch when unshadowed.
func isNonUserName(name string) bool {
	if name == "dispatch" || name == "spawn" || name == "each_line" || name == "use" || hofNames[name] {
		return true
	}
	_, ok := builtins[name]
	return ok
}

func asFunction(v value.Value) (*Function, bool) {
	if v.Tag() == value.Func {
		if f, ok := v.Obj().(*Function); ok {
			return f, true
		}
	}
	return nil, false
}

// bindArgs validates the argument count and fills any missing trailing arguments from
// the parameters' default expressions — evaluated at call time, left to right, so a
// default may reference an earlier parameter (and there is no shared-mutable-default
// gotcha). It returns the full positional argument list.
func bindArgs(fn *Function, args []value.Value) ([]value.Value, error) {
	np := len(fn.Params)
	if len(args) == np {
		return args, nil
	}
	if len(args) > np {
		return nil, arityError(fn, len(args))
	}
	full := make([]value.Value, np)
	copy(full, args)
	scope := fn.Env.child() // a throwaway scope so a default can see earlier parameters
	for i := 0; i < len(args); i++ {
		_ = scope.define(fn.Params[i], args[i], false)
	}
	for i := len(args); i < np; i++ {
		if i >= len(fn.Defaults) || fn.Defaults[i] == nil {
			return nil, arityError(fn, len(args))
		}
		v, err := evalExpr(fn.Defaults[i], scope)
		if err != nil {
			return nil, err
		}
		full[i] = v
		_ = scope.define(fn.Params[i], v, false)
	}
	return full, nil
}

// arityError reports a wrong argument count, noting the accepted range when the
// function has defaulted (optional) parameters.
func arityError(fn *Function, got int) error {
	name := fn.Name
	if name == "" {
		name = "function"
	}
	np := len(fn.Params)
	required := np
	for i := 0; i < len(fn.Defaults); i++ {
		if fn.Defaults[i] != nil {
			required = i
			break
		}
	}
	if required == np {
		return fmt.Errorf("%s expects %d argument(s), got %d", name, np, got)
	}
	return fmt.Errorf("%s expects %d to %d arguments, got %d", name, required, np, got)
}

func callFunction(fn *Function, args []value.Value) (value.Value, error) {
	args, err := bindArgs(fn, args)
	if err != nil {
		return value.MakeNil(), err
	}
	if fn.Proto != nil {
		return vmCallFunction(fn, args)
	}
	local := fn.Env.child()
	for i, p := range fn.Params {
		_ = local.define(p, args[i], false)
	}
	v, err := evalBlock(fn.Body, local)
	if err != nil {
		if r, ok := err.(returnSignal); ok {
			return r.v, nil
		}
		if r, ok := err.(errSignal); ok {
			return r.e, nil // a propagated error becomes this function's error result
		}
		switch err.(type) {
		case breakSignal, nextSignal:
			// Can't happen for parser-checked programs, but never let loop control
			// leak across a call boundary into the caller's loop.
			return value.MakeNil(), fmt.Errorf("%s", err)
		}
		return value.MakeNil(), err
	}
	return v, nil // implicit return: value of the last statement
}

func opSym(op token.Kind) string {
	switch op {
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
	}
	return op.String()
}

type builtin func(args []value.Value) (value.Value, error)

// safeBuiltin invokes a builtin, converting any panic (e.g. an oversized
// allocation deep in the Go stdlib) into a catchable Err value, so a script can
// never crash the interpreter with ordinary input.
func safeBuiltin(name string, b builtin, args []value.Value) (v value.Value, err error) {
	defer func() {
		if r := recover(); r != nil {
			v = value.MakeErr(fmt.Sprintf("%s: %v", name, r), 1)
			err = nil
		}
	}()
	return b(args)
}

var builtins = map[string]builtin{
	"say":         builtinSay,
	"warn":        builtinWarn,
	"fail":        builtinFail,
	"die":         builtinDie,
	"exit":        builtinExit,
	"parse_args":  builtinParseArgs,
	"int":         builtinInt,
	"is_err":      builtinIsErr,
	"err_code":    builtinErrCode,
	"err_msg":     builtinErrMsg,
	"run":         builtinRun,
	"capture":     builtinCapture,
	"capture_all": builtinCaptureAll,
	"pipe":        builtinPipe,
	"start":       builtinStart,
	"kill":        builtinKill,
	"pid":         builtinPid,
	"len":         builtinLen,
	"push":        builtinPush,
	"pop":         builtinPop,
	"keys":        builtinKeys,
	"values":      builtinValues,
	"pairs":       builtinPairs,
	"has":         builtinHas,
	"delete":      builtinDelete,
	"chars":       builtinChars,
	"contains":    builtinContains,

	// filesystem: path helpers
	"join":          builtinJoin,
	"dirname":       builtinDirname,
	"basename":      builtinBasename,
	"ext":           builtinExt,
	"stem":          builtinStem,
	"abspath":       builtinAbspath,
	"slash":         builtinSlash,
	"is_abs":        builtinIsAbs,
	"clean":         builtinClean,
	"rel":           builtinRel,
	"within":        builtinWithin,
	"path_list_sep": builtinPathListSep,
	// filesystem: stat guards
	"exists": builtinExists,
	"is_dir": builtinIsDir,
	// filesystem: fallible ops
	"glob":     builtinGlob,
	"read_dir": builtinReadDir,
	"mkdir":    builtinMkdir,
	"mtime":    builtinMtime,
	"newer":    builtinNewer,
	"stale":    builtinStale,
	// filesystem: file IO + atomic-swap family
	"read_file":  builtinReadFile,
	"write_file": builtinWriteFile,
	"rename":     builtinRename,
	"rm":         builtinRm,
	"copy":       builtinCopy,
	"size":       builtinSize,

	// text / strings
	"split":       builtinSplit,
	"replace":     builtinReplace,
	"trim":        builtinTrim,
	"upper":       builtinUpper,
	"lower":       builtinLower,
	"starts_with": builtinStartsWith,
	"ends_with":   builtinEndsWith,
	"format":      builtinFormat,
	"lines":       builtinLines,
	"repeat":      builtinRepeat,

	// JSON (thin binding over encoding/json)
	"from_json": builtinFromJSON,
	"to_json":   builtinToJSON,

	// CSV (thin binding over encoding/csv)
	"from_csv": builtinFromCSV,
	"to_csv":   builtinToCSV,

	// numeric (minimal daily-driver math)
	"abs":   builtinAbs,
	"sum":   builtinSum,
	"min":   builtinMin,
	"max":   builtinMax,
	"floor": builtinFloor,
	"ceil":  builtinCeil,
	"round": builtinRound,

	// regex (RE2)
	"re":       builtinRe,
	"matches":  builtinMatches,
	"match":    builtinMatch,
	"find_all": builtinFindAll,
	"gsub":     builtinGsub,

	// array ops (no callback)
	"take": builtinTake,
	"drop": builtinDrop,
	"uniq": builtinUniq,

	// concurrency: channels (spawn is a special form; pmap is in evalHOF)
	"await":   builtinAwait,
	"chan":    builtinChan,
	"send":    builtinSend,
	"recv":    builtinRecv,
	"recv_ok": builtinRecvOk,
	"close":   builtinCloseChan,
	"drain":   builtinDrain,

	// date/time (epoch seconds, Perl-style; strftime %-codes, local time)
	"now":        builtinNow,
	"sleep":      builtinSleep,
	"strftime":   builtinStrftime,
	"parse_time": builtinParseTime,
	"date_parts": builtinDateParts,

	// hashing + text encodings (thin Go-stdlib bindings)
	"sha256":      builtinSha256,
	"sha1":        builtinSha1,
	"md5":         builtinMd5,
	"to_base64":   builtinToBase64,
	"from_base64": builtinFromBase64,
	"to_hex":      builtinToHex,
	"from_hex":    builtinFromHex,
	"url_encode":  builtinURLEncode,
	"url_decode":  builtinURLDecode,

	// randomness (math/rand/v2; uuid uses crypto/rand)
	"rand":     builtinRand,
	"rand_int": builtinRandInt,
	"shuffle":  builtinShuffle,
	"sample":   builtinSample,
	"uuid":     builtinUUID,

	// runtime knobs
	"sys_gc": builtinSysGC,
	"cwd":    builtinCwd,
	"env":    builtinEnv,
}

// stdout is where say writes its output; tests override it to capture output.
var stdout io.Writer = os.Stdout

// stderr is where run streams a child's stderr; tests override it too.
var stderr io.Writer = os.Stderr

// outMu serializes say's writes to stdout so concurrent pmap workers can't
// interleave or race the shared writer. (run streams a child's stdio live and
// can't be fully fenced — prefer capture() inside parallel callbacks.)
var outMu sync.Mutex

func builtinSay(args []value.Value) (value.Value, error) {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = a.Display()
	}
	outMu.Lock()
	fmt.Fprintln(stdout, strings.Join(parts, " "))
	outMu.Unlock()
	return value.MakeNil(), nil
}

// builtinFail returns an error value (for testing the error model).
func builtinFail(args []value.Value) (value.Value, error) {
	msg := "failed"
	if len(args) > 0 {
		msg = args[0].Display()
	}
	return value.MakeErr(msg, 1), nil
}

// builtinInt converts to an int, returning an error value on failure.
func builtinInt(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("int expects 1 argument, got %d", len(args))
	}
	switch a := args[0]; a.Tag() {
	case value.Int:
		return a, nil
	case value.Float:
		return value.MakeInt(int64(a.AsFloat())), nil
	case value.Str:
		n, err := strconv.ParseInt(strings.TrimSpace(a.AsStr()), 10, 64)
		if err != nil {
			return value.MakeErr(fmt.Sprintf("cannot parse %q as int", a.AsStr()), 1), nil
		}
		return value.MakeInt(n), nil
	default:
		return value.MakeErr(fmt.Sprintf("cannot convert %s to int", a.TypeName()), 1), nil
	}
}

// builtinLen returns the element/entry count of a collection, or the rune count
// of a string.
func builtinLen(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("len expects 1 argument, got %d", len(args))
	}
	a := args[0]
	switch a.Tag() {
	case value.Arr, value.Map, value.Range:
		return value.MakeInt(int64(a.Obj().Len())), nil
	case value.Str:
		return value.MakeInt(int64(utf8.RuneCountInString(a.AsStr()))), nil
	}
	return value.MakeErr(fmt.Sprintf("cannot take len of %s", a.TypeName()), 1), nil
}

// builtinPush appends values to an array in place and returns the same array.
func builtinPush(args []value.Value) (value.Value, error) {
	if len(args) < 2 {
		return value.MakeNil(), fmt.Errorf("push expects at least 2 arguments (array, value...), got %d", len(args))
	}
	if args[0].Tag() != value.Arr {
		return value.MakeErr(fmt.Sprintf("push expects an array, got %s", args[0].TypeName()), 1), nil
	}
	a := args[0].Obj().(*value.Array)
	if a.IsFrozen() {
		return value.MakeErr("cannot push to a frozen array", 1), nil
	}
	a.Elems = append(a.Elems, args[1:]...)
	return args[0], nil
}

// builtinPop removes and returns the last element; an empty array is an Err.
func builtinPop(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("pop expects 1 argument, got %d", len(args))
	}
	if args[0].Tag() != value.Arr {
		return value.MakeErr(fmt.Sprintf("pop expects an array, got %s", args[0].TypeName()), 1), nil
	}
	a := args[0].Obj().(*value.Array)
	if a.IsFrozen() {
		return value.MakeErr("cannot pop from a frozen array", 1), nil
	}
	if len(a.Elems) == 0 {
		return value.MakeErr("pop from empty array", 1), nil
	}
	last := a.Elems[len(a.Elems)-1]
	a.Elems = a.Elems[:len(a.Elems)-1]
	return last, nil
}

// builtinKeys returns a fresh array of a map's keys, in insertion order.
func builtinKeys(args []value.Value) (value.Value, error) {
	m, errv, err := mapArg("keys", args)
	if err != nil {
		return value.MakeNil(), err
	}
	if m == nil {
		return errv, nil
	}
	out := make([]value.Value, m.Len())
	copy(out, m.Keys())
	return value.MakeArray(out), nil
}

// builtinValues returns a fresh array of a map's values, in insertion order.
func builtinValues(args []value.Value) (value.Value, error) {
	m, errv, err := mapArg("values", args)
	if err != nil {
		return value.MakeNil(), err
	}
	if m == nil {
		return errv, nil
	}
	out := make([]value.Value, m.Len())
	copy(out, m.Vals())
	return value.MakeArray(out), nil
}

// builtinPairs returns an array of [key, value] arrays, in insertion order.
func builtinPairs(args []value.Value) (value.Value, error) {
	m, errv, err := mapArg("pairs", args)
	if err != nil {
		return value.MakeNil(), err
	}
	if m == nil {
		return errv, nil
	}
	keys, vals := m.Keys(), m.Vals()
	out := make([]value.Value, len(keys))
	for i := range keys {
		out[i] = value.MakeArray([]value.Value{keys[i], vals[i]})
	}
	return value.MakeArray(out), nil
}

// builtinHas reports whether a map contains a key.
func builtinHas(args []value.Value) (value.Value, error) {
	if len(args) != 2 {
		return value.MakeNil(), fmt.Errorf("has expects 2 arguments (map, key), got %d", len(args))
	}
	if args[0].Tag() != value.Map {
		return value.MakeErr(fmt.Sprintf("has expects a map, got %s", args[0].TypeName()), 1), nil
	}
	m := args[0].Obj().(*value.OrderedMap)
	return value.MakeBool(m.Has(args[1])), nil
}

// builtinDelete removes a key from a map in place and returns the same map.
func builtinDelete(args []value.Value) (value.Value, error) {
	if len(args) != 2 {
		return value.MakeNil(), fmt.Errorf("delete expects 2 arguments (map, key), got %d", len(args))
	}
	if args[0].Tag() != value.Map {
		return value.MakeErr(fmt.Sprintf("delete expects a map, got %s", args[0].TypeName()), 1), nil
	}
	m := args[0].Obj().(*value.OrderedMap)
	if m.IsFrozen() {
		return value.MakeErr("cannot delete from a frozen map", 1), nil
	}
	m.Delete(args[1])
	return args[0], nil
}

// builtinChars splits a string into an array of single-rune strings.
func builtinChars(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("chars expects 1 argument, got %d", len(args))
	}
	if args[0].Tag() != value.Str {
		return value.MakeErr(fmt.Sprintf("chars expects a string, got %s", args[0].TypeName()), 1), nil
	}
	var out []value.Value
	for _, r := range args[0].AsStr() {
		out = append(out, value.MakeStr(string(r)))
	}
	return value.MakeArray(out), nil
}

// builtinContains reports array membership (structural ==) or string substring.
func builtinContains(args []value.Value) (value.Value, error) {
	if len(args) != 2 {
		return value.MakeNil(), fmt.Errorf("contains expects 2 arguments, got %d", len(args))
	}
	switch args[0].Tag() {
	case value.Arr:
		a := args[0].Obj().(*value.Array)
		for _, el := range a.Elems {
			if value.Equal(el, args[1]) {
				return value.MakeBool(true), nil
			}
		}
		return value.MakeBool(false), nil
	case value.Str:
		if args[1].Tag() != value.Str {
			return value.MakeErr("contains on a string needs a string needle", 1), nil
		}
		return value.MakeBool(strings.Contains(args[0].AsStr(), args[1].AsStr())), nil
	}
	return value.MakeErr(fmt.Sprintf("contains expects an array or string, got %s", args[0].TypeName()), 1), nil
}

// mapArg validates the single map argument shared by keys/values/pairs.
// Following the builtin convention, a wrong argument count is an aborting Go
// error, while a non-map argument is a catchable Err value (returned with m==nil).
func mapArg(name string, args []value.Value) (*value.OrderedMap, value.Value, error) {
	if len(args) != 1 {
		return nil, value.MakeNil(), fmt.Errorf("%s expects 1 argument, got %d", name, len(args))
	}
	if args[0].Tag() != value.Map {
		return nil, value.MakeErr(fmt.Sprintf("%s expects a map, got %s", name, args[0].TypeName()), 1), nil
	}
	return args[0].Obj().(*value.OrderedMap), value.MakeNil(), nil
}
