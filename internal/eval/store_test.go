package eval

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anafalanx/drang/internal/value"
)

// storeTestPath returns a fresh, forward-slashed store path under a temp dir, safe to
// embed in a single-quoted (raw) drang string on Windows.
func storeTestPath(t *testing.T) string {
	t.Helper()
	return filepath.ToSlash(filepath.Join(t.TempDir(), "kv.store"))
}

// The parity tests below embed an explicit temp path (never the default) and clear the
// store first, so the walker sub-run and the VM sub-run of assertBoth — which share the
// same process-global open handle — both start from a clean, deterministic slate.

func TestStoreRoundTripParity(t *testing.T) {
	defer resetStoresForTest()
	p := storeTestPath(t)
	src := "$s := store('" + p + "')\n" +
		"store_clear($s)\n" +
		"store_set($s, \"n\", 42)\n" +
		"store_set($s, \"name\", \"ada\")\n" +
		"say(store_get($s, \"n\"))\n" +
		"say(store_has($s, \"name\"), store_has($s, \"missing\"))\n" +
		"say(store_get($s, \"missing\", \"def\"))\n" +
		"say(store_keys($s))"
	assertBoth(t, src, "42\ntrue false\ndef\n[n, name]\n")
}

func TestStoreUpdateAtomicParity(t *testing.T) {
	defer resetStoresForTest()
	p := storeTestPath(t)
	// no seed: the first update uses the default (0) since the key is absent
	src := "$s := store('" + p + "')\n" +
		"store_clear($s)\n" +
		"store_update($s, \"c\", 0, |$n| $n + 1)\n" +
		"store_update($s, \"c\", 0, |$n| $n + 1)\n" +
		"say(store_get($s, \"c\"))"
	assertBoth(t, src, "2\n")
}

func TestWithStoreBatchParity(t *testing.T) {
	defer resetStoresForTest()
	p := storeTestPath(t)
	src := "$s := store('" + p + "')\n" +
		"store_clear($s)\n" +
		"with_store($s, |$s| {\n" +
		"  store_set($s, \"a\", 10)\n" +
		"  store_set($s, \"b\", 20)\n" +
		"})\n" +
		"say(store_get($s, \"a\") + store_get($s, \"b\"))"
	assertBoth(t, src, "30\n")
}

func TestWithStoreRollbackParity(t *testing.T) {
	defer resetStoresForTest()
	p := storeTestPath(t)
	// a failing batch commits nothing: "x" must be absent after rollback
	src := "$s := store('" + p + "')\n" +
		"store_clear($s)\n" +
		"fn .bad($s) {\n" +
		"  store_set($s, \"x\", 99)\n" +
		"  fail(\"boom\")\n" +
		"}\n" +
		"$r := with_store($s, .bad) // \"rolled-back\"\n" +
		"say($r, store_has($s, \"x\"))"
	assertBoth(t, src, "rolled-back false\n")
}

func TestStoreBadStoreArgParity(t *testing.T) {
	defer resetStoresForTest()
	// first arg not a store -> catchable Err on both backends
	assertBoth(t, "say(is_err(store_get(5, \"k\")))", "true\n")
}

// TestStoreArityAbortsParity: a wrong argument count aborts identically on both backends.
func TestStoreArityAbortsParity(t *testing.T) {
	defer resetStoresForTest()
	p := storeTestPath(t)
	src := "$s := store('" + p + "')\nstore_get($s)" // 1 arg -> abort
	_, werr := runBackend(t, src, false)
	resetStoresForTest()
	_, verr := runBackend(t, src, true)
	if (werr == nil) != (verr == nil) {
		t.Errorf("arity error-outcome mismatch: walker=%v vm=%v", werr, verr)
	}
	if werr == nil {
		t.Error("store_get with 1 argument should abort")
	}
}

// TestStorePersistsAcrossOpens proves durability: a value set through one handle is
// readable after the handle is closed and the store reopened from disk.
func TestStorePersistsAcrossOpens(t *testing.T) {
	defer resetStoresForTest()
	p := filepath.Join(t.TempDir(), "kv.store")

	s1, _, ok := openStore(p)
	if !ok {
		t.Fatal("open 1 failed")
	}
	h1 := value.MakeObj(value.Store, s1)
	callBuiltin(t, "store_set", h1, str("k"), str("v"))
	callBuiltin(t, "store_set", h1, str("list"), value.MakeArray([]value.Value{value.MakeInt(1), value.MakeInt(2)}))
	s1.close() // release the lock and forget the handle

	s2, _, ok := openStore(p) // reopen fresh from disk
	if !ok {
		t.Fatal("open 2 failed")
	}
	h2 := value.MakeObj(value.Store, s2)
	if got := callBuiltin(t, "store_get", h2, str("k")); got.AsStr() != "v" {
		t.Errorf("persisted store_get(k) = %q, want v", got.AsStr())
	}
	if got := callBuiltin(t, "store_get", h2, str("list")); got.Tag() != value.Arr || got.Obj().Len() != 2 {
		t.Errorf("persisted store_get(list) = %s, want a 2-element array", got.Display())
	}
	s2.close()
}

// TestStoreRejectsNonSerializable: a value carrying a channel is a catchable Err and is
// not stored.
func TestStoreRejectsNonSerializable(t *testing.T) {
	defer resetStoresForTest()
	s, _, ok := openStore(filepath.Join(t.TempDir(), "kv.store"))
	if !ok {
		t.Fatal("open failed")
	}
	h := value.MakeObj(value.Store, s)
	ch := callBuiltin(t, "chan")
	if res := callBuiltin(t, "store_set", h, str("ch"), ch); !res.IsErr() {
		t.Errorf("store_set with a channel should be a catchable Err, got %s", res.Display())
	}
	if callBuiltin(t, "store_has", h, str("ch")).AsBool() {
		t.Error("a rejected value must not be stored")
	}
	s.close()
}

// TestStoreConcurrentRace: many spawned tasks hit one shared store at once. Run under
// -race, this catches any unsynchronized access; the atomic store_update also makes the
// shared counter exact (8 tasks -> 8), which a non-atomic read-modify-write would lose.
func TestStoreConcurrentRace(t *testing.T) {
	defer resetStoresForTest()
	p := filepath.ToSlash(filepath.Join(t.TempDir(), "kv.store"))
	src := "$s := store('" + p + "')\n" +
		"store_clear($s)\n" +
		"$ts := map([1, 2, 3, 4, 5, 6, 7, 8], |$i| spawn(|$s, $i| {\n" +
		"  store_set($s, \"k\" ~ str($i), $i)\n" +
		"  store_update($s, \"c\", 0, |$n| $n + 1)\n" +
		"}, $s, $i))\n" +
		"map($ts, |$t| await($t))\n" +
		"say(store_get($s, \"c\"))"
	out, err := runBackend(t, src, true) // VM = production path
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if out != "8\n" {
		t.Errorf("concurrent atomic updates: got %q, want %q", out, "8\n")
	}
}

// TestStoreDefaultPath: store() with no argument, given a script path via the env,
// resolves to .drang/<script>.store next to the script and creates it.
func TestStoreDefaultPath(t *testing.T) {
	defer resetStoresForTest()
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "cursor.dr") // need not exist on disk
	prog := mustParseProg(t, "$s := store()\nstore_set($s, \"k\", 7)\nsay(store_path($s))")
	env := NewEnv()
	env.SetScriptPath(scriptPath)

	var buf bytes.Buffer
	old := stdout
	stdout = &buf
	defer func() { stdout = old }()
	if err := RunProgramVM(prog, env); err != nil {
		t.Fatalf("run: %v", err)
	}

	want := filepath.Join(dir, ".drang", "cursor.store")
	if got := strings.TrimSpace(buf.String()); got != want {
		t.Errorf("default store path = %q, want %q", got, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf(".drang/cursor.store was not created: %v", err)
	}
}
