package eval

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
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
// Modules load once per Env session (cached by canonical path), are diamond-safe, and
// import cycles error. Flat-merge is NOT transitive (a name a module itself merged is
// not re-exported). Exports are deeply immutable: collectExports freezes the record
// and everything reachable from it (value.Freeze), so the one shared cached copy is
// safe to read across importers and a mutation fails loudly instead of poisoning it.

const maxModuleBytes int64 = 64 << 20 // 64 MiB

// moduleRegistry scopes both completed cache entries and in-flight promises to one top-level Env
// session. Snapshots (pmap/spawn) share it so concurrent imports single-flight, while independent
// runs cannot observe each other's module state or retain their closures indefinitely.
type moduleRegistry struct {
	mu      sync.Mutex
	entries map[string]*moduleEntry
}

func newModuleRegistry() *moduleRegistry {
	return &moduleRegistry{entries: make(map[string]*moduleEntry)}
}

// readFileBounded reads at most limit+1 bytes. The sentinel byte detects oversized inputs without
// first allocating for the entire file; callers use it for untrusted module/store snapshots.
func readFileBounded(path string, limit int64, what string) ([]byte, error) {
	if limit < 0 {
		return nil, fmt.Errorf("%s has an invalid read limit", what)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s exceeds the %d-byte limit", what, limit)
	}
	return b, nil
}

// moduleEntry is both the successful-load cache entry and the promise shared by concurrent first
// importers. waitsFor contains only temporary edges between in-flight entries; it lets us reject a
// cross-goroutine A->B / B->A import cycle instead of deadlocking two single-flight waiters.
// All fields except the closed done channel are guarded by the owning moduleRegistry.mu.
type moduleEntry struct {
	canon    string
	done     chan struct{}
	exports  value.Value
	err      error
	loading  bool
	waitsFor map[*moduleEntry]bool
}

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
	registry := env.moduleRegistry()
	registry.mu.Lock()
	if entry, ok := registry.entries[canon]; ok {
		if !entry.loading {
			exports, lerr := entry.exports, entry.err
			registry.mu.Unlock()
			return exports, lerr
		}

		// If this import is itself running as a module leader, record the wait edge and
		// reject a cross-goroutine cycle before parking on entry.done.
		current := currentModuleEntry(env)
		edgeAdded := false
		if current != nil && current.loading {
			if moduleWaitPath(entry, current, map[*moduleEntry]bool{}) {
				registry.mu.Unlock()
				return value.MakeNil(), fmt.Errorf("concurrent import cycle through %s", canon)
			}
			if current.waitsFor == nil {
				current.waitsFor = map[*moduleEntry]bool{}
			}
			current.waitsFor[entry] = true
			edgeAdded = true
		}
		done := entry.done
		registry.mu.Unlock()
		<-done
		if edgeAdded {
			registry.mu.Lock()
			delete(current.waitsFor, entry)
			registry.mu.Unlock()
		}
		return entry.exports, entry.err
	}

	entry := &moduleEntry{canon: canon, done: make(chan struct{}), loading: true}
	registry.entries[canon] = entry
	registry.mu.Unlock()

	exports, lerr := runModule(canon, env, entry)
	registry.mu.Lock()
	entry.exports, entry.err, entry.loading = exports, lerr, false
	entry.waitsFor = nil
	close(entry.done)
	if lerr != nil && registry.entries[canon] == entry {
		delete(registry.entries, canon) // a failed load never poisons a later retry
	}
	registry.mu.Unlock()
	return exports, lerr
}

// moduleRegistry returns the registry attached to the nearest top-level environment. NewEnv always
// installs one; walking the chain keeps child scopes lightweight while snapshots retain the same
// session registry.
func (e *Env) moduleRegistry() *moduleRegistry {
	for s := e; s != nil; s = s.parent {
		if s.modules != nil {
			return s.modules
		}
	}
	// Env values are package-private and production roots come from NewEnv. Keep synthetic test
	// environments safe rather than panicking if one is ever introduced.
	return newModuleRegistry()
}

// currentModuleEntry finds the lexical module load containing env, if any.
func currentModuleEntry(env *Env) *moduleEntry {
	for s := env; s != nil; s = s.parent {
		if s.moduleLoad != nil {
			return s.moduleLoad
		}
	}
	return nil
}

// moduleWaitPath reports whether following in-flight wait edges from from reaches target.
// The owning moduleRegistry.mu must be held.
func moduleWaitPath(from, target *moduleEntry, seen map[*moduleEntry]bool) bool {
	if from == target {
		return true
	}
	if from == nil || !from.loading || seen[from] {
		return false
	}
	seen[from] = true
	for dep := range from.waitsFor {
		if moduleWaitPath(dep, target, seen) {
			return true
		}
	}
	return false
}

// runModule reads, parses, and runs a module file into a fresh prelude-backed env,
// then collects its exports. The module's own top-level bindings land in modEnv (a
// child of the prelude env), cleanly separable from prelude/seed names.
func runModule(canon string, importerEnv *Env, entry *moduleEntry) (value.Value, error) {
	if fi, e := os.Stat(canon); e == nil && fi.IsDir() {
		return value.MakeNil(), fmt.Errorf("%s is a directory, not a module file", canon)
	}
	src, e := readFileBounded(canon, maxModuleBytes, "module source")
	if e != nil {
		return value.MakeNil(), fmt.Errorf("cannot read %s: %v", canon, e)
	}
	p := parser.New(string(src))
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		return value.MakeNil(), fmt.Errorf("parse error in %s: %s", canon, strings.Join(errs, "; "))
	}
	WarnProgramLints(prog, string(src), canon, p.Comments(), stderr) // modules receive the same file diagnostics
	base := NewEnv()
	base.modules = importerEnv.moduleRegistry()
	base.stores = importerEnv.storeSession()
	base.exec = importerEnv.executionContext()
	base.strand = importerEnv.executionStrand()
	// The module's functions charge overflow-guard fires to the importing session's
	// counter, which program entry points reset. A private counter on this fresh module
	// env would otherwise bypass the caller's runaway-recursion budget.
	if c := importerEnv.stormCounter(); c != nil {
		base.overflowFires = c
	}
	seedArgv(base, nil)
	if err := RunPrelude(base); err != nil {
		return value.MakeNil(), err
	}
	modEnv := base.child()
	modEnv.moduleDir = filepath.Dir(canon)
	modEnv.loadingChain = importerEnv.chainWith(canon)
	modEnv.moduleLoad = entry
	if err := RunProgramVM(prog, modEnv); err != nil {
		if _, ok := ExitRequested(err); ok {
			return value.MakeNil(), err // exit()/die() during import — propagate, do not catch
		}
		return value.MakeNil(), fmt.Errorf("error in %s: %v", canon, err)
	}
	return collectExports(modEnv, canon)
}

// collectExports builds the export record from a module env's own scope, in a
// deterministic (sorted) order: each `export`-marked .foo user function (keyed
// without its dot, so $u.foo works) and each `export`-marked $CONST. Unmarked names
// are module-private and simply never enter the record — which is also what keeps
// them out of a flat merge. Bindings flat-merged from a sub-module are skipped
// (re-export is non-transitive; a DELIBERATE one is `export $sub ::= use(...)`).
// A mutable top-level var is rejected even when private: the module runs once and
// its env is shared by every importer, so mutable top-level state would be
// cross-importer shared state (and a data race under pmap) regardless of
// visibility.
func collectExports(modEnv *Env, canon string) (value.Value, error) {
	keys := make([]string, 0, len(modEnv.vars))
	for k := range modEnv.vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	m := value.MakeMap()
	om := m.Obj().(*value.OrderedMap)
	candidates := 0
	for _, key := range keys {
		b := modEnv.vars[key]
		if b.merged {
			continue
		}
		isFn := strings.HasPrefix(key, ".")
		if !isFn && !b.frozen {
			return value.MakeNil(), fmt.Errorf("%s: a module may not hold mutable top-level state — $%s must be a `::=` constant (or live inside a function)", canon, key)
		}
		candidates++
		if !b.exported {
			continue // unmarked = module-private
		}
		if isFn {
			om.Set(value.MakeStr(strings.TrimPrefix(key, ".")), b.v)
		} else {
			om.Set(value.MakeStr(key), b.v)
		}
	}
	// A module whose whole top level is private is almost always a migration miss
	// (pre-`e` modules exported everything) — say so instead of silently importing
	// nothing. stderr, not an error: an all-side-effect module is still legal.
	if candidates > 0 && len(om.Keys()) == 0 {
		fmt.Fprintf(stderr, "drang: warning: %s exports nothing — top-level names are module-private unless marked with `export`\n", canon)
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
	canon := strings.ToLower(filepath.Clean(abs)) // Windows paths are case-insensitive
	return canon, nil
}
