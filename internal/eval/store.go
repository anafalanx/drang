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
	"runtime"
	"strings"
	"sync"

	"github.com/anafalanx/drang/internal/value"
	"golang.org/x/sys/windows"
)

// maxStoreBytes caps the serialized store, keeping this a small key-value store
// rather than a database. Exceeding it is a catchable Err, consistent with the
// read_file / capture size caps.
const maxStoreBytes = 64 << 20 // 64 MiB

var (
	errStoreClosed          = errors.New("store is closed")
	errStoreTxnBusy         = errors.New("store is busy in another transaction")
	errStoreReentry         = errors.New("same-store transaction reentry is not allowed")
	errStoreCloseTxn        = errors.New("store cannot be closed while a transaction is active")
	maxLiveStoresPerSession = 256 // variable so focused tests can lower the per-session cap
	maxStoreUpdateRetries   = 64  // optimistic pure-transform retries before a catchable busy Err
)

type storeTxnMode uint8

const (
	storeTxnNone storeTxnMode = iota
	storeTxnBatch
)

type storeTransaction struct {
	mode  storeTxnMode
	owner *executionStrand
}

// Store is a persistent JSON key-value handle. Like Chan/Task/Proc it is an
// intentionally SHARED reference type (DeepCopy returns itself) so send/spawn hand
// every goroutine the same store rather than a clone; mu guards all state.
type Store struct {
	mu       sync.Mutex
	data     *value.OrderedMap             // in-memory view (insertion-ordered, JSON-faithful)
	path     string                        // backing .store file (absolute)
	lock     *storeLock                    // exclusive advisory lock on <path>.lock
	registry *storeRegistry                // lifecycle owner; never points back to the cleanup target
	txn      storeTransaction              // exclusive with_store owner; protected by mu
	updates  map[*executionStrand]struct{} // active optimistic store_update callbacks
	version  uint64                        // increments after every durable mutation/batch commit
	closed   bool
}

func (s *Store) TypeName() string { return "store" }
func (s *Store) Display() string  { return fmt.Sprintf("<store %s>", s.path) }

func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0
	}
	return s.data.Len()
}

// Equal is identity: a store is a unique mutable handle (like Chan/Task/Proc).
func (s *Store) Equal(o value.Obj) bool {
	other, ok := o.(*Store)
	return ok && other == s
}

// DeepCopy shares the handle: copy-on-send must not clone the store.
func (s *Store) DeepCopy(map[value.Obj]value.Obj) value.Obj { return s }

// --- session registry: one live *Store per canonical path within one evaluator session ---

// storeSession is the reachability target for the registry cleanup. Envs, spawned snapshots,
// and module envs share this small object. The registry deliberately has no pointer back to the
// target, so runtime cleanup can run once the last owning execution scope disappears.
type storeSession struct {
	registry *storeRegistry
}

type storeRegistry struct {
	mu sync.Mutex
	m  map[string]*Store
}

func newStoreSession() *storeSession {
	r := &storeRegistry{m: make(map[string]*Store)}
	s := &storeSession{registry: r}
	runtime.AddCleanup(s, (*storeRegistry).closeAll, r)
	return s
}

// open returns the store for abs(path), opening+locking+loading it on first use and returning the
// cached handle thereafter. The bool is false when it returns a catchable Err value instead.
func (ss *storeSession) open(path string) (*Store, value.Value, bool) {
	s, errv, ok := ss.registry.open(path)
	runtime.KeepAlive(ss) // the cleanup target owns the registry throughout the open operation
	return s, errv, ok
}

func storeRegistryKey(abs string) string {
	// Windows paths are case-insensitive by default. Treat spelling-only case differences as the
	// same session handle rather than making the second open collide with our own advisory lock.
	return strings.ToLower(filepath.Clean(abs))
}

func (r *storeRegistry) open(path string) (*Store, value.Value, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, value.MakeErr(fmt.Sprintf("store: %v", err), 1), false
	}
	key := storeRegistryKey(abs)
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.m[key]; ok {
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if !closed {
			return s, value.MakeNil(), true // idempotent: same live handle for the same path
		}
		// Defensive repair for an entry left behind by an interrupted/older close path.
		// The close implementation below removes entries atomically, so normal execution
		// never reaches this branch.
		delete(r.m, key)
	}
	if maxLiveStoresPerSession <= 0 || len(r.m) >= maxLiveStoresPerSession {
		return nil, value.MakeErr(fmt.Sprintf("store: too many live store handles in this session (limit %d)", maxLiveStoresPerSession), 137), false
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
	s := &Store{data: data, path: abs, lock: lk, registry: r}
	r.m[key] = s
	return s, value.MakeNil(), true
}

// loadStoreData reads and decodes the store file, recovering from the .bak snapshot
// if the primary file is unreadable/corrupt. A missing file is a fresh, empty store.
func loadStoreData(path string) (*value.OrderedMap, error) {
	b, err := readFileBounded(path, maxStoreBytes, "store snapshot")
	if err != nil {
		if os.IsNotExist(err) {
			return value.MakeMap().Obj().(*value.OrderedMap), nil
		}
		return nil, err
	}
	if m, derr := unmarshalStoreFile(b); derr == nil {
		return m, nil
	} else if bb, rerr := readFileBounded(path+".bak", maxStoreBytes, "store backup snapshot"); rerr == nil {
		if m2, berr := unmarshalStoreFile(bb); berr == nil {
			return m2, nil // recovered from the backup snapshot
		}
		return nil, fmt.Errorf("cannot read store %s: %v (backup also invalid)", path, derr)
	} else {
		return nil, fmt.Errorf("cannot read store %s: %v (no valid backup)", path, derr)
	}
}

func (s *Store) close() error {
	// Registry -> store is the one lifecycle lock order, shared with storeRegistry.open. Holding
	// the registry until the advisory lock is released and the entry removed makes
	// close/reopen one atomic transition: an opener gets the old live handle or a fully
	// initialized new one, never a closed handle or a spurious busy error in between.
	r := s.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	if s.txn.mode != storeTxnNone || len(s.updates) != 0 {
		// Waiting could deadlock if store_close was called by a transaction callback itself.
		// Refuse deterministically; the caller may close after the transaction ends.
		return errStoreCloseTxn
	}
	s.closed = true
	if s.lock != nil {
		s.lock.release()
		s.lock = nil
	}
	key := storeRegistryKey(s.path)
	if r.m[key] == s {
		delete(r.m, key)
	}
	return nil
}

// closeAll is both the storeSession runtime cleanup and the deterministic test/host cleanup. It
// releases every advisory lock without calling Store.close (the registry lock is already held).
func (r *storeRegistry) closeAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, s := range r.m {
		s.mu.Lock()
		if !s.closed {
			s.closed = true
			if s.lock != nil {
				s.lock.release()
				s.lock = nil
			}
		}
		s.mu.Unlock()
		delete(r.m, key)
	}
}

func (s *Store) checkOpenLocked() error {
	if s.closed {
		return errStoreClosed
	}
	return nil
}

// checkAccessLocked rejects operations from an unrelated strand while an exclusive with_store is
// open. Its owner may use ordinary operations. A store_update callback is optimistic rather than
// exclusive, but its own strand cannot re-enter this store; unrelated strands remain free to make
// progress and any mutation makes the update retry from a fresh value.
func (s *Store) checkAccessLocked(owner *executionStrand) error {
	if err := s.checkOpenLocked(); err != nil {
		return err
	}
	if s.txn.mode == storeTxnNone {
		if owner == nil && len(s.updates) != 0 {
			return errStoreTxnBusy
		}
		if _, active := s.updates[owner]; active {
			return errStoreReentry
		}
		return nil
	}
	if owner == nil || owner != s.txn.owner {
		return errStoreTxnBusy
	}
	return nil
}

func (s *Store) beginBatchLocked(owner *executionStrand) error {
	if err := s.checkOpenLocked(); err != nil {
		return err
	}
	if owner == nil {
		return errors.New("store execution strand is unavailable")
	}
	if s.txn.mode != storeTxnNone {
		if s.txn.owner == owner {
			return errStoreReentry
		}
		return errStoreTxnBusy
	}
	if len(s.updates) != 0 {
		if _, reentrant := s.updates[owner]; reentrant {
			return errStoreReentry
		}
		return errStoreTxnBusy
	}
	s.txn = storeTransaction{mode: storeTxnBatch, owner: owner}
	return nil
}

func (s *Store) endTransactionLocked(owner *executionStrand) {
	if s.txn.owner == owner {
		s.txn = storeTransaction{}
	}
}

func (s *Store) beginUpdateLocked(owner *executionStrand) error {
	if err := s.checkOpenLocked(); err != nil {
		return err
	}
	if owner == nil {
		return errors.New("store execution strand is unavailable")
	}
	if s.txn.mode != storeTxnNone {
		if s.txn.owner == owner {
			return errStoreReentry
		}
		return errStoreTxnBusy
	}
	if s.updates == nil {
		s.updates = make(map[*executionStrand]struct{})
	}
	if _, active := s.updates[owner]; active {
		return errStoreReentry
	}
	s.updates[owner] = struct{}{}
	return nil
}

func (s *Store) endUpdateLocked(owner *executionStrand) { delete(s.updates, owner) }

// --- codec (reuses the package-private JSON codec so round-trips are faithful) ---

func marshalStoreValue(v value.Value, indent string) ([]byte, error) {
	b := newJSONBuffer(maxStoreBytes)
	if err := encodeJSON(b, v, indent, 0, &jsonItemBudget{}); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

func unmarshalStoreFile(data []byte) (*value.OrderedMap, error) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber() // preserve int64 vs float
	v, err := decodeJSON(dec, 0, &jsonItemBudget{})
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
	if err := s.checkOpenLocked(); err != nil {
		return err
	}
	data, err := marshalStoreValue(value.MakeObj(value.Map, s.data), "  ")
	if err != nil {
		return err
	}
	if len(data) > maxStoreBytes {
		return fmt.Errorf("store exceeds the %d MiB cap", maxStoreBytes>>20)
	}
	if prev, rerr := readFileBounded(s.path, maxStoreBytes, "previous store snapshot"); rerr == nil {
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
	if err := s.checkOpenLocked(); err != nil {
		return err
	}
	cp := value.DeepCopyValue(v, map[value.Obj]value.Obj{}) // copy-on-send in
	kv := value.MakeStr(key)
	old, had := s.data.Get(kv)
	s.data.Set(kv, cp)
	if s.txn.mode == storeTxnBatch {
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
	s.version++
	return nil
}

func (s *Store) deleteLocked(key string) error {
	if err := s.checkOpenLocked(); err != nil {
		return err
	}
	kv := value.MakeStr(key)
	old, had := s.data.Get(kv)
	if !had {
		return nil // idempotent
	}
	s.data.Delete(kv)
	if s.txn.mode == storeTxnBatch {
		return nil
	}
	if err := s.flushLocked(); err != nil {
		s.data.Set(kv, old) // roll back
		return err
	}
	s.version++
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
	ss := env.storeSession()
	if ss == nil {
		return value.MakeErr("store: evaluator session is unavailable", 1), nil
	}
	s, errv, ok := ss.open(path)
	if !ok {
		return errv, nil
	}
	return value.MakeObj(value.Store, s), nil
}

// evalStoreUpdate implements store_update(store, key, default, fn): an atomic
// read-modify-write. fn receives the current value, or `default` if the key is absent
// (so a counter never has to handle nil). The pure transform runs without the store mutex. If
// another strand commits meanwhile, it is retried from the new value; same-strand same-store
// callback access is a catchable reentry Err instead of a deadlock. Argument order mirrors reduce.
func evalStoreUpdate(args []value.Value, env *Env, depth int) (value.Value, error) {
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
	owner := env.executionStrand()
	defaultTemplate, cloneErr := newClosureSnapshotForStrand(owner).cloneValue(args[2], 0)
	if cloneErr != nil {
		return value.MakeErr(fmt.Sprintf("store_update: default: %v", cloneErr), 137), nil
	}
	cloneDefault := func() (value.Value, error) {
		return newClosureSnapshotForStrand(owner).cloneValue(defaultTemplate, 0)
	}
	initialDefault, cloneErr := cloneDefault()
	if cloneErr != nil {
		return value.MakeErr(fmt.Sprintf("store_update: default: %v", cloneErr), 137), nil
	}
	s.mu.Lock()
	if err := s.beginUpdateLocked(owner); err != nil {
		s.mu.Unlock()
		return value.MakeErr("store_update: "+err.Error(), 1), nil
	}
	observedVersion := s.version
	base, found := s.getLocked(key) // copied out if present
	if !found {
		base = initialDefault
	}
	s.mu.Unlock()

	// Clear callback ownership even on panic, exit, or runtime.Goexit. Keeping it active across
	// retries makes every same-strand same-store callback access deterministic reentry.
	active := true
	defer func() {
		if active {
			s.mu.Lock()
			s.endUpdateLocked(owner)
			s.mu.Unlock()
		}
	}()
	conflicts := 0
	for {
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

		s.mu.Lock()
		if s.version != observedVersion {
			conflicts++
			if maxStoreUpdateRetries <= 0 || conflicts >= maxStoreUpdateRetries {
				s.mu.Unlock()
				return value.MakeErr(fmt.Sprintf("store_update: store stayed busy after %d retries", conflicts), 1), nil
			}
			observedVersion = s.version
			base, found = s.getLocked(key)
			s.mu.Unlock()
			if !found {
				base, cloneErr = cloneDefault()
				if cloneErr != nil {
					return value.MakeErr(fmt.Sprintf("store_update: default: %v", cloneErr), 137), nil
				}
			}
			continue
		}
		err := s.setLocked(key, next)
		s.endUpdateLocked(owner)
		active = false
		s.mu.Unlock()
		if err != nil {
			return value.MakeErr(fmt.Sprintf("store_update: %v", err), 1), nil
		}
		return next, nil
	}
}

// evalWithStore implements with_store(store, fn): an all-or-nothing batch. Mutations
// inside the callback are held in memory and committed with a single atomic write when
// the callback returns; a callback error (propagated Err or exit/die) rolls the store
// back to its pre-batch state. The owner strand may re-enter through ordinary store
// operations; other strands and nested same-store transactions are rejected deterministically.
func evalWithStore(args []value.Value, env *Env, depth int) (value.Value, error) {
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
	owner := env.executionStrand()
	s.mu.Lock()
	if err := s.beginBatchLocked(owner); err != nil {
		s.mu.Unlock()
		return value.MakeErr("with_store: "+err.Error(), 1), nil
	}
	snap := value.DeepCopyValue(value.MakeObj(value.Map, s.data), map[value.Obj]value.Obj{}).Obj().(*value.OrderedMap)
	s.mu.Unlock()

	cleanupNeeded := true
	defer func() {
		if cleanupNeeded {
			s.mu.Lock()
			s.data = snap
			s.endTransactionLocked(owner)
			s.mu.Unlock()
		}
	}()
	result, cerr := callFunction(fn, []value.Value{args[0]}, depth)

	s.mu.Lock()
	cleanupNeeded = false
	defer s.mu.Unlock()
	defer s.endTransactionLocked(owner)
	if cerr != nil {
		s.data = snap
		return value.MakeNil(), cerr
	}
	if result.IsErr() {
		s.data = snap
		return result, nil
	}
	if err := s.flushLocked(); err != nil {
		s.data = snap
		return value.MakeErr(fmt.Sprintf("with_store: %v", err), 1), nil
	}
	s.version++
	return result, nil
}

// --- plain builtins (registered in the `builtins` map) ---

func isStoreBuiltin(name string) bool {
	switch name {
	case "store_get", "store_set", "store_has", "store_delete", "store_keys", "store_all", "store_clear", "store_path", "store_close":
		return true
	default:
		return false
	}
}

func callStoreBuiltin(name string, args []value.Value, env *Env) (value.Value, error) {
	var owner *executionStrand
	if env != nil {
		owner = env.executionStrand()
	}
	switch name {
	case "store_get":
		return storeGet(args, owner)
	case "store_set":
		return storeSet(args, owner)
	case "store_has":
		return storeHas(args, owner)
	case "store_delete":
		return storeDelete(args, owner)
	case "store_keys":
		return storeKeys(args, owner)
	case "store_all":
		return storeAll(args, owner)
	case "store_clear":
		return storeClear(args, owner)
	case "store_path":
		return storePath(args, owner)
	case "store_close":
		return builtinStoreClose(args)
	default:
		return value.MakeNil(), fmt.Errorf("unknown store builtin %s", name)
	}
}

func builtinStoreGet(args []value.Value) (value.Value, error) { return storeGet(args, nil) }

func storeGet(args []value.Value, owner *executionStrand) (value.Value, error) {
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
	if err := s.checkAccessLocked(owner); err != nil {
		s.mu.Unlock()
		return value.MakeErr("store_get: "+err.Error(), 1), nil
	}
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

func builtinStoreSet(args []value.Value) (value.Value, error) { return storeSet(args, nil) }

func storeSet(args []value.Value, owner *executionStrand) (value.Value, error) {
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
	if err := s.checkAccessLocked(owner); err != nil {
		s.mu.Unlock()
		return value.MakeErr("store_set: "+err.Error(), 1), nil
	}
	err := s.setLocked(key, args[2])
	s.mu.Unlock()
	if err != nil {
		return value.MakeErr(fmt.Sprintf("store_set: %v", err), 1), nil
	}
	return value.MakeBool(true), nil
}

func builtinStoreHas(args []value.Value) (value.Value, error) { return storeHas(args, nil) }

func storeHas(args []value.Value, owner *executionStrand) (value.Value, error) {
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
	if err := s.checkAccessLocked(owner); err != nil {
		s.mu.Unlock()
		return value.MakeErr("store_has: "+err.Error(), 1), nil
	}
	has := s.data.Has(value.MakeStr(key))
	s.mu.Unlock()
	return value.MakeBool(has), nil
}

func builtinStoreDelete(args []value.Value) (value.Value, error) { return storeDelete(args, nil) }

func storeDelete(args []value.Value, owner *executionStrand) (value.Value, error) {
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
	if err := s.checkAccessLocked(owner); err != nil {
		s.mu.Unlock()
		return value.MakeErr("store_delete: "+err.Error(), 1), nil
	}
	err := s.deleteLocked(key)
	s.mu.Unlock()
	if err != nil {
		return value.MakeErr(fmt.Sprintf("store_delete: %v", err), 1), nil
	}
	return value.MakeBool(true), nil
}

func builtinStoreKeys(args []value.Value) (value.Value, error) { return storeKeys(args, nil) }

func storeKeys(args []value.Value, owner *executionStrand) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("store_keys expects 1 argument (store), got %d", len(args))
	}
	s, errv, ok := storeArg("store_keys", args[0])
	if !ok {
		return errv, nil
	}
	s.mu.Lock()
	if err := s.checkAccessLocked(owner); err != nil {
		s.mu.Unlock()
		return value.MakeErr("store_keys: "+err.Error(), 1), nil
	}
	ks := s.data.Keys()
	out := make([]value.Value, len(ks))
	copy(out, ks) // keys are immutable string values
	s.mu.Unlock()
	return value.MakeArray(out), nil
}

func builtinStoreAll(args []value.Value) (value.Value, error) { return storeAll(args, nil) }

func storeAll(args []value.Value, owner *executionStrand) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("store_all expects 1 argument (store), got %d", len(args))
	}
	s, errv, ok := storeArg("store_all", args[0])
	if !ok {
		return errv, nil
	}
	s.mu.Lock()
	if err := s.checkAccessLocked(owner); err != nil {
		s.mu.Unlock()
		return value.MakeErr("store_all: "+err.Error(), 1), nil
	}
	cp := value.DeepCopyValue(value.MakeObj(value.Map, s.data), map[value.Obj]value.Obj{})
	s.mu.Unlock()
	return cp, nil
}

func builtinStoreClear(args []value.Value) (value.Value, error) { return storeClear(args, nil) }

func storeClear(args []value.Value, owner *executionStrand) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("store_clear expects 1 argument (store), got %d", len(args))
	}
	s, errv, ok := storeArg("store_clear", args[0])
	if !ok {
		return errv, nil
	}
	s.mu.Lock()
	if cerr := s.checkAccessLocked(owner); cerr != nil {
		s.mu.Unlock()
		return value.MakeErr("store_clear: "+cerr.Error(), 1), nil
	}
	old := s.data
	s.data = value.MakeMap().Obj().(*value.OrderedMap)
	var err error
	if s.txn.mode != storeTxnBatch {
		if err = s.flushLocked(); err != nil {
			s.data = old // roll back
		} else {
			s.version++
		}
	}
	s.mu.Unlock()
	if err != nil {
		return value.MakeErr(fmt.Sprintf("store_clear: %v", err), 1), nil
	}
	return value.MakeBool(true), nil
}

func builtinStorePath(args []value.Value) (value.Value, error) { return storePath(args, nil) }

func storePath(args []value.Value, owner *executionStrand) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("store_path expects 1 argument (store), got %d", len(args))
	}
	s, errv, ok := storeArg("store_path", args[0])
	if !ok {
		return errv, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkAccessLocked(owner); err != nil {
		return value.MakeErr("store_path: "+err.Error(), 1), nil
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
	if err := s.close(); err != nil {
		return value.MakeErr("store_close: "+err.Error(), 1), nil
	}
	return value.MakeNil(), nil
}
