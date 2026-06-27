package eval

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/anafalanx/drang/internal/ast"
	"github.com/anafalanx/drang/internal/parser"
	"github.com/anafalanx/drang/internal/value"
)

// Module support. `use "path"` (a statement) flat-merges a module's exported
// .functions and $CONSTs into the current scope; `$u := use("path")` (a call) binds
// the module's frozen export record, reached via $u.foo() / $u.CONST. A module is any
// .dr file; its top-level user functions (.foo) and constants ($CONST) are its
// exports — mutable top-level state is rejected, keeping exports frozen and
// pmap-safe. Modules load once per process (cached by canonical path), are
// diamond-safe, and report import cycles.

type moduleEntry struct {
	exports value.Value
	err     error
	loading bool
}

var (
	moduleMu    sync.Mutex
	moduleCache = map[string]*moduleEntry{}
)

// evalUse implements the captured form `$u := use("path")`: it returns the module's
// frozen export record, or a catchable Err if the module fails to load.
func evalUse(args []value.Value, env *Env) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("use expects 1 argument (a path string), got %d", len(args))
	}
	if args[0].Tag() != value.Str {
		return value.MakeNil(), fmt.Errorf("use expects a string path, got %s", args[0].TypeName())
	}
	rec, err := loadModule(args[0].AsStr(), env.baseDir())
	if err != nil {
		return value.MakeErr("use: "+err.Error(), 1), nil
	}
	return rec, nil
}

// mergeModule implements the directive `use "path"`: it flat-merges the module's
// exports into env's current scope. A name already bound here is an error.
func mergeModule(n *ast.UseStmt, env *Env) error {
	pv, err := evalExpr(n.Path, env)
	if err != nil {
		return err
	}
	if pv.Tag() != value.Str {
		return fmt.Errorf("use: path must be a string, got %s", pv.TypeName())
	}
	rec, err := loadModule(pv.AsStr(), env.baseDir())
	if err != nil {
		return fmt.Errorf("use %q: %v", pv.AsStr(), err)
	}
	om := rec.Obj().(*value.OrderedMap)
	keys, vals := om.Keys(), om.Vals()
	for i := range keys {
		key := keys[i].AsStr()
		if vals[i].Tag() == value.Func { // functions live in the .-namespace; constants stay bare
			key = "." + key
		}
		if _, exists := env.vars[key]; exists {
			return fmt.Errorf("use %q: %s is already defined here", pv.AsStr(), sigilName(key))
		}
		if e := env.define(key, vals[i], true); e != nil { // frozen: a later duplicate also trips define
			return fmt.Errorf("use %q: %v", pv.AsStr(), e)
		}
	}
	return nil
}

func sigilName(key string) string {
	if strings.HasPrefix(key, ".") {
		return key
	}
	return "$" + key
}

// loadModule resolves and loads a module, caching by canonical path (load-once,
// diamond-safe) and detecting import cycles.
func loadModule(pathArg, baseDir string) (value.Value, error) {
	canon, err := resolvePath(pathArg, baseDir)
	if err != nil {
		return value.MakeNil(), err
	}
	moduleMu.Lock()
	if e, ok := moduleCache[canon]; ok {
		loading, exports, lerr := e.loading, e.exports, e.err
		moduleMu.Unlock()
		if loading {
			return value.MakeNil(), fmt.Errorf("import cycle through %s", canon)
		}
		return exports, lerr
	}
	moduleCache[canon] = &moduleEntry{loading: true}
	moduleMu.Unlock()

	exports, lerr := runModule(canon)

	moduleMu.Lock()
	moduleCache[canon] = &moduleEntry{exports: exports, err: lerr}
	moduleMu.Unlock()
	return exports, lerr
}

// runModule reads, parses, and runs a module file into a fresh prelude-backed env,
// then collects its exports. The module's own top-level bindings land in modEnv (a
// child of the prelude env), so they are cleanly separable from prelude/seed names.
func runModule(canon string) (value.Value, error) {
	src, e := os.ReadFile(canon)
	if e != nil {
		return value.MakeNil(), fmt.Errorf("cannot read %s: %v", canon, e)
	}
	p := parser.New(string(src))
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		return value.MakeNil(), fmt.Errorf("parse error in %s: %s", canon, strings.Join(errs, "; "))
	}
	base := NewEnv()
	seedArgv(base, nil)
	if err := RunPrelude(base); err != nil {
		return value.MakeNil(), err
	}
	modEnv := base.child()
	modEnv.moduleDir = filepath.Dir(canon)
	if err := RunProgramVM(prog, modEnv); err != nil {
		if _, ok := ExitRequested(err); ok {
			return value.MakeNil(), fmt.Errorf("%s called exit() during import", canon)
		}
		return value.MakeNil(), fmt.Errorf("error in %s: %v", canon, err)
	}
	return collectExports(modEnv, canon)
}

// collectExports builds the frozen export record from a module env's own scope: each
// .foo user function (keyed without its dot, so $u.foo works) and each $CONST. A
// mutable top-level var is rejected — modules export only functions and constants.
func collectExports(modEnv *Env, canon string) (value.Value, error) {
	m := value.MakeMap()
	om := m.Obj().(*value.OrderedMap)
	for key, b := range modEnv.vars {
		switch {
		case strings.HasPrefix(key, "."):
			// Own functions are non-frozen; a frozen .foo was flat-merged from a
			// sub-module, so it is NOT re-exported (flat-merge is not transitive).
			if b.frozen {
				continue
			}
			om.Set(value.MakeStr(strings.TrimPrefix(key, ".")), b.v)
		case b.frozen:
			om.Set(value.MakeStr(key), b.v)
		default:
			return value.MakeNil(), fmt.Errorf("%s: a module may export only functions and constants, but $%s is a mutable top-level variable", canon, key)
		}
	}
	return m, nil
}

// resolvePath turns a use path into a canonical absolute path. A relative path joins
// onto baseDir (the importer's directory, or cwd when baseDir is empty); a ".dr"
// extension is added when the path has none and the bare path does not exist.
func resolvePath(pathArg, baseDir string) (string, error) {
	p := pathArg
	if !filepath.IsAbs(p) {
		p = filepath.Join(baseDir, p)
	}
	if filepath.Ext(p) == "" {
		if _, err := os.Stat(p); err != nil {
			p += ".dr"
		}
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("resolving %q: %v", pathArg, err)
	}
	return filepath.Clean(abs), nil
}
