package eval

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/anafalanx/drang/internal/ast"
	"github.com/anafalanx/drang/internal/parser"
	"github.com/anafalanx/drang/internal/value"
)

// Module support. `use "./util"` (a statement) flat-merges a module's exported
// .functions and $CONSTs into the current scope; `$u := use("./util")` (a call) binds
// the module's export record, reached via $u.foo() / $u.CONST. A module is any .dr
// file; its top-level user functions (.foo) and constants ($CONST) are its exports —
// mutable top-level state is rejected, so exports are functions and constants only.
// Modules load once per process (cached by canonical path), are diamond-safe, and
// import cycles error. Flat-merge is NOT transitive (a name a module itself merged is
// not re-exported). Exports are deeply immutable: collectExports freezes the record
// and everything reachable from it (value.Freeze), so the one shared cached copy is
// safe to read across importers and a mutation fails loudly instead of poisoning it.

var (
	moduleMu    sync.Mutex
	moduleCache = map[string]value.Value{} // canonical path -> export record (successful loads only)
)

// evalUse implements the captured form `$u := use("path")`: it returns the module's
// export record, or a catchable Err if the module fails to load. An exit()/die()
// during the module's import is NOT caught — it propagates and ends the program.
func evalUse(args []value.Value, env *Env) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("use expects 1 argument (a path string), got %d", len(args))
	}
	if args[0].Tag() != value.Str {
		return value.MakeNil(), fmt.Errorf("use expects a string path, got %s", args[0].TypeName())
	}
	rec, err := loadModule(args[0].AsStr(), env)
	if err != nil {
		if _, ok := ExitRequested(err); ok {
			return value.MakeNil(), err
		}
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
	rec, err := loadModule(pv.AsStr(), env)
	if err != nil {
		if _, ok := ExitRequested(err); ok {
			return err
		}
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
		if e := env.define(key, vals[i], true); e != nil {
			return fmt.Errorf("use %q: %v", pv.AsStr(), e)
		}
		b := env.vars[key] // mark as merged so a re-export does not propagate it (non-transitive)
		b.merged = true
		env.vars[key] = b
	}
	return nil
}

func sigilName(key string) string {
	if strings.HasPrefix(key, ".") {
		return key
	}
	return "$" + key
}

// loadModule resolves and loads a module, caching successful loads by canonical path
// (load-once, diamond-safe). Import cycles are detected per import chain (threaded
// through env), so concurrent loads of the same module never false-trigger a cycle.
func loadModule(pathArg string, env *Env) (value.Value, error) {
	canon, err := resolvePath(pathArg, env.baseDir())
	if err != nil {
		return value.MakeNil(), err
	}
	if env.loading(canon) {
		return value.MakeNil(), fmt.Errorf("import cycle through %s", canon)
	}
	moduleMu.Lock()
	cached, ok := moduleCache[canon]
	moduleMu.Unlock()
	if ok {
		return cached, nil
	}
	exports, lerr := runModule(canon, env)
	if lerr == nil { // cache only successful loads, so a failure never poisons later imports
		moduleMu.Lock()
		moduleCache[canon] = exports
		moduleMu.Unlock()
	}
	return exports, lerr
}

// runModule reads, parses, and runs a module file into a fresh prelude-backed env,
// then collects its exports. The module's own top-level bindings land in modEnv (a
// child of the prelude env), cleanly separable from prelude/seed names.
func runModule(canon string, importerEnv *Env) (value.Value, error) {
	if fi, e := os.Stat(canon); e == nil && fi.IsDir() {
		return value.MakeNil(), fmt.Errorf("%s is a directory, not a module file", canon)
	}
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
	modEnv.loadingChain = importerEnv.chainWith(canon)
	if err := RunProgramVM(prog, modEnv); err != nil {
		if _, ok := ExitRequested(err); ok {
			return value.MakeNil(), err // exit()/die() during import — propagate, do not catch
		}
		return value.MakeNil(), fmt.Errorf("error in %s: %v", canon, err)
	}
	return collectExports(modEnv, canon)
}

// collectExports builds the export record from a module env's own scope, in a
// deterministic (sorted) order: each .foo user function (keyed without its dot, so
// $u.foo works) and each $CONST. Bindings flat-merged from a sub-module are skipped
// (re-export is non-transitive). A mutable top-level var is rejected.
func collectExports(modEnv *Env, canon string) (value.Value, error) {
	keys := make([]string, 0, len(modEnv.vars))
	for k := range modEnv.vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	m := value.MakeMap()
	om := m.Obj().(*value.OrderedMap)
	for _, key := range keys {
		b := modEnv.vars[key]
		if b.merged {
			continue
		}
		switch {
		case strings.HasPrefix(key, "."):
			om.Set(value.MakeStr(strings.TrimPrefix(key, ".")), b.v)
		case b.frozen:
			om.Set(value.MakeStr(key), b.v)
		default:
			return value.MakeNil(), fmt.Errorf("%s: a module may export only functions and constants, but $%s is a mutable top-level variable", canon, key)
		}
	}
	// Freeze the record (and, transitively, every exported array/map) so the one
	// cached copy is safe to share read-only across importers — mutating an export
	// fails loudly instead of poisoning the cache.
	value.Freeze(m)
	return m, nil
}

// resolvePath turns a use path into a canonical absolute path. A relative path joins
// onto baseDir (the importer's directory, or cwd when baseDir is empty); a ".dr"
// extension is added when the path has none and the bare path does not exist. On a
// case-insensitive filesystem (Windows) the key is lower-cased so one file maps to
// one cache entry.
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
	canon := filepath.Clean(abs)
	if runtime.GOOS == "windows" {
		canon = strings.ToLower(canon)
	}
	return canon, nil
}
