package eval

// Persistent JSON key-value store. `store(path?)` opens (or creates) a store and
// returns a handle; store_get/set/has/delete/keys/all/clear read and mutate it;
// store_update is an atomic read-modify-write; with_store batches a group of
// mutations into one all-or-nothing commit.
//
// Design (see DESIGN.md, the store decision record):
//   - Values are ordinary JSON-serializable drang values (scalars, arrays, maps),
//     stored through drang's own JSON codec so int stays 64-bit exact and map key
//     order is preserved. A value carrying a channel/task/process/function/regex is
//     rejected with a catchable Err, exactly as to_json would reject it.
//   - Durability is atomic-snapshot-per-write: every mutation rewrites the whole
//     file via a temp file + fsync + atomic rename, keeping the previous good copy
//     as <path>.bak. The file is therefore never observed torn; a crash leaves
//     either the old or the new complete snapshot.
//   - A store holds a process-exclusive advisory lock on <path>.lock for its
//     lifetime, so two drang processes never write the same store concurrently; a
//     second opener gets a catchable "store busy" Err. The data file itself is never
//     locked, so other tools can still read it and the atomic rename is unhindered.
//   - The handle is a shared reference type (DeepCopy returns itself, like a
//     channel), guarded by its own mutex, so it is safe to hand to spawn/pmap
//     workers; concurrent access is serialized, not raced.
//   - store() with no path defaults to a .drang/<script>.store subfolder next to the
//     running script — a predictable, env-var-free location. -e/stdin have no script
//     file, so they must pass an explicit path.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/anafalanx/drang/internal/value"
	"golang.org/x/sys/windows"
)

// maxStoreBytes caps the serialized store, keeping this a small key-value store
// rather than a database. Exceeding it is a catchable Err, consistent with the
// read_file / capture size caps.
const maxStoreBytes = 64 << 20 // 64 MiB

// Store is a persistent JSON key-value handle. Like Chan/Task/Proc it is an
// intentionally SHARED reference type (DeepCopy returns itself) so send/spawn hand
// every goroutine the same store rather than a clone; mu guards all state.
type Store struct {
	mu         sync.Mutex
	data       *value.OrderedMap // in-memory view (insertion-ordered, JSON-faithful)
	path       string            // backing .store file (absolute)
	lock       *storeLock        // exclusive advisory lock on <path>.lock
	batchDepth int               // >0 while inside with_store: defer the flush to commit
	closed     bool
}

func (s *Store) TypeName() string { return "store" }
func (s *Store) Display() string  { return fmt.Sprintf("<store %s>", s.path) }

func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Len()
}

// Equal is identity: a store is a unique mutable handle (like Chan/Task/Proc).
func (s *Store) Equal(o value.Obj) bool {
	other, ok := o.(*Store)
	return ok && other == s
}

// DeepCopy shares the handle: copy-on-send must not clone the store.
func (s *Store) DeepCopy(map[value.Obj]value.Obj) value.Obj { return s }

// --- open registry: one live *Store per absolute path per process (idempotent open) ---

var openStores = struct {
	mu sync.Mutex
	m  map[string]*Store
}{m: map[string]*Store{}}

// openStore returns the store for abs(path), opening+locking+loading it on first
// use and returning the cached handle thereafter. The bool is false when it returns
// a catchable Err value instead.
func openStore(path string) (*Store, value.Value, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, value.MakeErr(fmt.Sprintf("store: %v", err), 1), false
	}
	openStores.mu.Lock()
	defer openStores.mu.Unlock()
	if s, ok := openStores.m[abs]; ok {
		return s, value.MakeNil(), true // idempotent: same handle for the same path
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, value.MakeErr(fmt.Sprintf("store: %v", err), 1), false
	}
	lk, err := acquireExclusive(abs + ".lock")
	if err != nil {
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, value.MakeErr(fmt.Sprintf("store busy: %s is open in another process", abs), 1), false
		}
		return nil, value.MakeErr(fmt.Sprintf("store: cannot lock %s: %v", abs, err), 1), false
	}
	data, err := loadStoreData(abs)
	if err != nil {
		lk.release()
		return nil, value.MakeErr(fmt.Sprintf("store: %v", err), 1), false
	}
	s := &Store{data: data, path: abs, lock: lk}
	openStores.m[abs] = s
	return s, value.MakeNil(), true
}

// loadStoreData reads and decodes the store file, recovering from the .bak snapshot
// if the primary file is unreadable/corrupt. A missing file is a fresh, empty store.
func loadStoreData(path string) (*value.OrderedMap, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return value.MakeMap().Obj().(*value.OrderedMap), nil
		}
		return nil, err
	}
	if m, derr := unmarshalStoreFile(b); derr == nil {
		return m, nil
	} else if bb, rerr := os.ReadFile(path + ".bak"); rerr == nil {
		if m2, berr := unmarshalStoreFile(bb); berr == nil {
			return m2, nil // recovered from the backup snapshot
		}
		return nil, fmt.Errorf("cannot read store %s: %v (backup also invalid)", path, derr)
	} else {
		return nil, fmt.Errorf("cannot read store %s: %v (no valid backup)", path, derr)
	}
}

func (s *Store) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if s.lock != nil {
		s.lock.release()
		s.lock = nil
	}
	path := s.path
	s.mu.Unlock()
	openStores.mu.Lock()
	if openStores.m[path] == s {
		delete(openStores.m, path)
	}
	openStores.mu.Unlock()
}

// --- codec (reuses the package-private JSON codec so round-trips are faithful) ---

func marshalStoreValue(v value.Value, indent string) ([]byte, error) {
	var b strings.Builder
	if err := encodeJSON(&b, v, indent, 0); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

func unmarshalStoreFile(data []byte) (*value.OrderedMap, error) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber() // preserve int64 vs float
	v, err := decodeJSON(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing data after JSON value")
	}
	if v.Tag() != value.Map {
		return nil, fmt.Errorf("store file is not a JSON object")
	}
	return v.Obj().(*value.OrderedMap), nil
}

// --- persistence (all *Locked methods require the caller to hold s.mu) ---

// flushLocked serializes the whole store and atomically replaces the file, keeping
// the previous good copy as <path>.bak. In batch mode the caller skips this.
func (s *Store) flushLocked() error {
	data, err := marshalStoreValue(value.MakeObj(value.Map, s.data), "  ")
	if err != nil {
		return err
	}
	if len(data) > maxStoreBytes {
		return fmt.Errorf("store exceeds the %d MiB cap", maxStoreBytes>>20)
	}
	if prev, rerr := os.ReadFile(s.path); rerr == nil {
		_ = atomicWriteFile(s.path+".bak", prev) // best-effort recovery snapshot
	}
	return atomicWriteFile(s.path, data)
}

func (s *Store) getLocked(key string) (value.Value, bool) {
	v, ok := s.data.Get(value.MakeStr(key))
	if !ok {
		return value.MakeNil(), false
	}
	return value.DeepCopyValue(v, map[value.Obj]value.Obj{}), true // copy-on-send out
}

func (s *Store) setLocked(key string, v value.Value) error {
	cp := value.DeepCopyValue(v, map[value.Obj]value.Obj{}) // copy-on-send in
	kv := value.MakeStr(key)
	old, had := s.data.Get(kv)
	s.data.Set(kv, cp)
	if s.batchDepth > 0 {
		return nil // committed at batch end
	}
	if err := s.flushLocked(); err != nil {
		if had { // roll back so memory and disk stay consistent
			s.data.Set(kv, old)
		} else {
			s.data.Delete(kv)
		}
		return err
	}
	return nil
}

func (s *Store) deleteLocked(key string) error {
	kv := value.MakeStr(key)
	old, had := s.data.Get(kv)
	if !had {
		return nil // idempotent
	}
	s.data.Delete(kv)
	if s.batchDepth > 0 {
		return nil
	}
	if err := s.flushLocked(); err != nil {
		s.data.Set(kv, old) // roll back
		return err
	}
	return nil
}

// atomicWriteFile writes data to path via a temp file in the same directory, fsync,
// then an atomic rename (MoveFileEx REPLACE_EXISTING). Adapted from the CLI's
// formatter writer, with an added fsync for durability.
func atomicWriteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".drang-store-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(data)
	if werr == nil {
		werr = tmp.Sync()
	}
	cerr := tmp.Close()
	if werr != nil {
		os.Remove(name)
		return werr
	}
	if cerr != nil {
		os.Remove(name)
		return cerr
	}
	if fi, e := os.Stat(path); e == nil {
		os.Chmod(name, fi.Mode())
	}
	if rerr := os.Rename(name, path); rerr != nil {
		os.Remove(name)
		return rerr
	}
	return nil
}

// --- advisory lock (Windows LockFileEx on a separate <path>.lock file) ---

type storeLock struct {
	f  *os.File
	ol windows.Overlapped
}

func acquireExclusive(lockPath string) (*storeLock, error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	l := &storeLock{f: f}
	h := windows.Handle(f.Fd())
	// Try-lock (fail immediately) so a busy store is a clean Err, never a hang.
	if err := windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &l.ol); err != nil {
		f.Close()
		return nil, err
	}
	return l, nil
}

func (l *storeLock) release() {
	if l == nil || l.f == nil {
		return
	}
	windows.UnlockFileEx(windows.Handle(l.f.Fd()), 0, 1, 0, &l.ol)
	l.f.Close()
	l.f = nil
}

// --- argument helpers ---

// storeArg type-checks the first argument as a store, returning a catchable Err
// value (never a Go error) on a type mismatch.
func storeArg(name string, v value.Value) (*Store, value.Value, bool) {
	if v.Tag() != value.Store {
		return nil, value.MakeErr(fmt.Sprintf("%s expects a store, got %s", name, v.TypeName()), 1), false
	}
	return v.Obj().(*Store), value.MakeNil(), true
}

func storeStrKey(name string, v value.Value) (string, value.Value, bool) {
	if v.Tag() != value.Str {
		return "", value.MakeErr(fmt.Sprintf("%s key must be a string, got %s", name, v.TypeName()), 1), false
	}
	return v.AsStr(), value.MakeNil(), true
}

// --- special forms (need env or a lambda; registered in dispatchNonUser) ---

// evalStore implements store(path?). It is a special form because with no path it
// derives the default location from the running script, which needs env.
func evalStore(args []value.Value, env *Env) (value.Value, error) {
	if len(args) > 1 {
		return value.MakeNil(), fmt.Errorf("store expects 0 or 1 arguments (path?), got %d", len(args))
	}
	var path string
	if len(args) == 1 {
		if args[0].Tag() != value.Str {
			return value.MakeErr(fmt.Sprintf("store expects a path string, got %s", args[0].TypeName()), 1), nil
		}
		path = args[0].AsStr()
	} else {
		sp := env.scriptFilePath()
		if sp == "" || strings.HasPrefix(sp, "<") {
			return value.MakeErr("store() needs a script file to derive a default path; pass an explicit path (e.g. store(\"data.store\")) when running with -e or stdin", 1), nil
		}
		base := filepath.Base(sp)
		name := strings.TrimSuffix(base, filepath.Ext(base))
		path = filepath.Join(filepath.Dir(sp), ".drang", name+".store")
	}
	s, errv, ok := openStore(path)
	if !ok {
		return errv, nil
	}
	return value.MakeObj(value.Store, s), nil
}

// evalStoreUpdate implements store_update(store, key, default, fn): an atomic
// read-modify-write. fn receives the current value, or `default` if the key is absent
// (so a counter never has to handle nil). The store mutex is held across the callback so
// the read and write are one indivisible step (correct counters/accumulators). The update
// function must therefore be a pure transform and must NOT call back into this store (that
// would deadlock). Argument order mirrors reduce(arr, init, fn).
func evalStoreUpdate(args []value.Value, depth int) (value.Value, error) {
	if len(args) != 4 {
		return value.MakeNil(), fmt.Errorf("store_update expects 4 arguments (store, key, default, fn), got %d", len(args))
	}
	s, errv, ok := storeArg("store_update", args[0])
	if !ok {
		return errv, nil
	}
	key, kerr, ok := storeStrKey("store_update", args[1])
	if !ok {
		return kerr, nil
	}
	fn, ok := asFunction(args[3])
	if !ok {
		return value.MakeErr(fmt.Sprintf("store_update expects a function, got %s", args[3].TypeName()), 1), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	base, found := s.getLocked(key) // copied out if present
	if !found {
		base = value.DeepCopyValue(args[2], map[value.Obj]value.Obj{}) // the default, isolated
	}
	next, cerr := callFunction(fn, []value.Value{base}, depth)
	if cerr != nil {
		return value.MakeNil(), cerr // exit/die/abort from the callback: propagate
	}
	if next.IsErr() {
		return next, nil // callback returned/propagated an Err: leave the store unchanged
	}
	if _, err := marshalStoreValue(next, ""); err != nil {
		return value.MakeErr(fmt.Sprintf("store_update: %v", err), 1), nil
	}
	if err := s.setLocked(key, next); err != nil {
		return value.MakeErr(fmt.Sprintf("store_update: %v", err), 1), nil
	}
	return next, nil
}

// evalWithStore implements with_store(store, fn): an all-or-nothing batch. Mutations
// inside the callback are held in memory and committed with a single atomic write when
// the callback returns; a callback error (propagated Err or exit/die) rolls the store
// back to its pre-batch state. The store mutex is NOT held across the callback (the
// lambda re-enters the store), so a store must not be batched concurrently from
// multiple goroutines.
func evalWithStore(args []value.Value, depth int) (value.Value, error) {
	if len(args) != 2 {
		return value.MakeNil(), fmt.Errorf("with_store expects 2 arguments (store, fn), got %d", len(args))
	}
	s, errv, ok := storeArg("with_store", args[0])
	if !ok {
		return errv, nil
	}
	fn, ok := asFunction(args[1])
	if !ok {
		return value.MakeErr(fmt.Sprintf("with_store expects a function, got %s", args[1].TypeName()), 1), nil
	}
	s.mu.Lock()
	snap := value.DeepCopyValue(value.MakeObj(value.Map, s.data), map[value.Obj]value.Obj{}).Obj().(*value.OrderedMap)
	s.batchDepth++
	s.mu.Unlock()

	result, cerr := callFunction(fn, []value.Value{args[0]}, depth)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.batchDepth--
	if cerr != nil {
		s.data = snap
		return value.MakeNil(), cerr
	}
	if result.IsErr() {
		s.data = snap
		return result, nil
	}
	if s.batchDepth == 0 {
		if err := s.flushLocked(); err != nil {
			s.data = snap
			return value.MakeErr(fmt.Sprintf("with_store: %v", err), 1), nil
		}
	}
	return result, nil
}

// --- plain builtins (registered in the `builtins` map) ---

func builtinStoreGet(args []value.Value) (value.Value, error) {
	if len(args) < 2 || len(args) > 3 {
		return value.MakeNil(), fmt.Errorf("store_get expects 2 or 3 arguments (store, key, default?), got %d", len(args))
	}
	s, errv, ok := storeArg("store_get", args[0])
	if !ok {
		return errv, nil
	}
	key, kerr, ok := storeStrKey("store_get", args[1])
	if !ok {
		return kerr, nil
	}
	s.mu.Lock()
	v, found := s.getLocked(key)
	s.mu.Unlock()
	if found {
		return v, nil
	}
	if len(args) == 3 {
		return args[2], nil // default
	}
	return value.MakeNil(), nil // absent, no default -> nil (like map access)
}

func builtinStoreSet(args []value.Value) (value.Value, error) {
	if len(args) != 3 {
		return value.MakeNil(), fmt.Errorf("store_set expects 3 arguments (store, key, value), got %d", len(args))
	}
	s, errv, ok := storeArg("store_set", args[0])
	if !ok {
		return errv, nil
	}
	key, kerr, ok := storeStrKey("store_set", args[1])
	if !ok {
		return kerr, nil
	}
	if _, err := marshalStoreValue(args[2], ""); err != nil {
		return value.MakeErr(fmt.Sprintf("store_set: %v", err), 1), nil // non-serializable value
	}
	s.mu.Lock()
	err := s.setLocked(key, args[2])
	s.mu.Unlock()
	if err != nil {
		return value.MakeErr(fmt.Sprintf("store_set: %v", err), 1), nil
	}
	return value.MakeBool(true), nil
}

func builtinStoreHas(args []value.Value) (value.Value, error) {
	if len(args) != 2 {
		return value.MakeNil(), fmt.Errorf("store_has expects 2 arguments (store, key), got %d", len(args))
	}
	s, errv, ok := storeArg("store_has", args[0])
	if !ok {
		return errv, nil
	}
	key, kerr, ok := storeStrKey("store_has", args[1])
	if !ok {
		return kerr, nil
	}
	s.mu.Lock()
	has := s.data.Has(value.MakeStr(key))
	s.mu.Unlock()
	return value.MakeBool(has), nil
}

func builtinStoreDelete(args []value.Value) (value.Value, error) {
	if len(args) != 2 {
		return value.MakeNil(), fmt.Errorf("store_delete expects 2 arguments (store, key), got %d", len(args))
	}
	s, errv, ok := storeArg("store_delete", args[0])
	if !ok {
		return errv, nil
	}
	key, kerr, ok := storeStrKey("store_delete", args[1])
	if !ok {
		return kerr, nil
	}
	s.mu.Lock()
	err := s.deleteLocked(key)
	s.mu.Unlock()
	if err != nil {
		return value.MakeErr(fmt.Sprintf("store_delete: %v", err), 1), nil
	}
	return value.MakeBool(true), nil
}

func builtinStoreKeys(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("store_keys expects 1 argument (store), got %d", len(args))
	}
	s, errv, ok := storeArg("store_keys", args[0])
	if !ok {
		return errv, nil
	}
	s.mu.Lock()
	ks := s.data.Keys()
	out := make([]value.Value, len(ks))
	copy(out, ks) // keys are immutable string values
	s.mu.Unlock()
	return value.MakeArray(out), nil
}

func builtinStoreAll(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("store_all expects 1 argument (store), got %d", len(args))
	}
	s, errv, ok := storeArg("store_all", args[0])
	if !ok {
		return errv, nil
	}
	s.mu.Lock()
	cp := value.DeepCopyValue(value.MakeObj(value.Map, s.data), map[value.Obj]value.Obj{})
	s.mu.Unlock()
	return cp, nil
}

func builtinStoreClear(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("store_clear expects 1 argument (store), got %d", len(args))
	}
	s, errv, ok := storeArg("store_clear", args[0])
	if !ok {
		return errv, nil
	}
	s.mu.Lock()
	old := s.data
	s.data = value.MakeMap().Obj().(*value.OrderedMap)
	var err error
	if s.batchDepth == 0 {
		if err = s.flushLocked(); err != nil {
			s.data = old // roll back
		}
	}
	s.mu.Unlock()
	if err != nil {
		return value.MakeErr(fmt.Sprintf("store_clear: %v", err), 1), nil
	}
	return value.MakeBool(true), nil
}

func builtinStorePath(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("store_path expects 1 argument (store), got %d", len(args))
	}
	s, errv, ok := storeArg("store_path", args[0])
	if !ok {
		return errv, nil
	}
	return value.MakeStr(s.path), nil
}

func builtinStoreClose(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("store_close expects 1 argument (store), got %d", len(args))
	}
	s, errv, ok := storeArg("store_close", args[0])
	if !ok {
		return errv, nil
	}
	s.close()
	return value.MakeNil(), nil
}

// resetStoresForTest closes and forgets every open store. Test-only, so a test's
// temp-dir cleanup is not blocked by a still-held lock handle.
func resetStoresForTest() {
	openStores.mu.Lock()
	stores := make([]*Store, 0, len(openStores.m))
	for _, s := range openStores.m {
		stores = append(stores, s)
	}
	openStores.mu.Unlock()
	for _, s := range stores {
		s.close()
	}
}
