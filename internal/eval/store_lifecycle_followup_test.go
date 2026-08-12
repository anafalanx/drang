package eval

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStoreSessionLiveHandleCapIsCatchableAndCloseFreesSlot(t *testing.T) {
	oldCap := maxLiveStoresPerSession
	maxLiveStoresPerSession = 2
	defer func() { maxLiveStoresPerSession = oldCap }()

	dir := t.TempDir()
	ss := testStoreSession(t)
	first, errv, ok := ss.open(filepath.Join(dir, "first.store"))
	if !ok {
		t.Fatalf("first open: %s", errv.Display())
	}
	if _, errv, ok = ss.open(filepath.Join(dir, "second.store")); !ok {
		t.Fatalf("second open: %s", errv.Display())
	}
	thirdPath := filepath.Join(dir, "third.store")
	if _, errv, ok = ss.open(thirdPath); ok || !errv.IsErr() || errv.ErrCode() != 137 {
		t.Fatalf("over-cap open = (%v, %s), want catchable code 137", ok, errv.Display())
	}
	if err := first.close(); err != nil {
		t.Fatalf("close first: %v", err)
	}
	if _, errv, ok = ss.open(thirdPath); !ok {
		t.Fatalf("open after close did not reuse slot: %s", errv.Display())
	}
}

func TestDiscardedStoreSessionCleanupReleasesAdvisoryLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "discarded.store")
	registry := func() *storeRegistry {
		ss := newStoreSession()
		if _, errv, ok := ss.open(path); !ok {
			t.Fatalf("initial open: %s", errv.Display())
		}
		r := ss.registry
		runtime.KeepAlive(ss)
		return r // deliberately retain the cleanup argument, but not its target
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		runtime.GC()
		runtime.Gosched()
		registry.mu.Lock()
		remaining := len(registry.m)
		registry.mu.Unlock()
		if remaining == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("discarded storeSession cleanup did not release its registry/lock")
		}
		time.Sleep(10 * time.Millisecond)
	}

	next := testStoreSession(t)
	if _, errv, ok := next.open(path); !ok {
		t.Fatalf("new session could not acquire discarded session's lock: %s", errv.Display())
	}
}

func TestStoreSessionsAreIndependentAndExplicitCloseHandsOffLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.store")
	firstSession := testStoreSession(t)
	secondSession := testStoreSession(t)
	first, errv, ok := firstSession.open(path)
	if !ok {
		t.Fatalf("first open: %s", errv.Display())
	}
	again, errv, ok := firstSession.open(strings.ToUpper(path))
	if !ok || again != first {
		t.Fatalf("same-session case-variant open = (%p, %s, %v), want identical handle", again, errv.Display(), ok)
	}
	if _, errv, ok = secondSession.open(path); ok || !errv.IsErr() {
		t.Fatalf("second live session open = (%v, %s), want busy Err", ok, errv.Display())
	}
	if err := first.close(); err != nil {
		t.Fatalf("explicit close: %v", err)
	}
	if _, errv, ok = secondSession.open(path); !ok {
		t.Fatalf("second session could not acquire lock after close: %s", errv.Display())
	}
}

func TestStoreUpdateRetriesAfterConcurrentMutation(t *testing.T) {
	p := storeTestPath(t)
	src := "$s := store('" + p + "')\n" +
		"store_clear($s)\n" +
		"$entered := chan(1)\n" +
		"$release := chan(1)\n" +
		"$t := spawn(|$s, $entered, $release| store_update($s, \"n\", 0, |$n| {\n" +
		"  if $n == 0 { send($entered, true); recv($release) }\n" +
		"  $n + 1\n" +
		"}), $s, $entered, $release)\n" +
		"recv($entered)\n" +
		"store_set($s, \"n\", 10)\n" +
		"send($release, true)\n" +
		"say(await($t), store_get($s, \"n\"))"
	out, err := runBackend(t, src, true)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "11 11\n" {
		t.Fatalf("optimistic retry = %q, want %q", out, "11 11\n")
	}
}

func TestStoreUpdateCallbackChildCanMutateWithoutDeadlock(t *testing.T) {
	p := storeTestPath(t)
	src := "$s := store('" + p + "')\n" +
		"store_clear($s)\n" +
		"$r := store_update($s, \"n\", 0, |$n| {\n" +
		"  if $n == 0 { $t := spawn(|$s| store_set($s, \"n\", 5), $s); await($t) }\n" +
		"  $n + 1\n" +
		"})\n" +
		"say($r, store_get($s, \"n\"))"
	out, err := runBackend(t, src, true)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "6 6\n" {
		t.Fatalf("callback-child retry = %q, want %q", out, "6 6\n")
	}
}

func TestStoreUpdateSameStrandReentryIsCatchableAndClearsOwner(t *testing.T) {
	p := storeTestPath(t)
	src := "$s := store('" + p + "')\n" +
		"store_clear($s)\n" +
		"$r := store_update($s, \"n\", 0, |$n| store_get($s, \"n\"))\n" +
		"say(is_err($r), store_has($s, \"n\"))\n" +
		"say(store_set($s, \"after\", 1))"
	assertBoth(t, src, "true false\ntrue\n")
}

func TestStoreUpdateDefaultSnapshotIsBounded(t *testing.T) {
	p := storeTestPath(t)
	src := "$s := store('" + p + "')\n" +
		"$x := 0\n" +
		"$i := 0\n" +
		"while $i < 600 { $x = [$x]; $i += 1 }\n" +
		"$r := store_update($s, \"missing\", $x, |$v| $v)\n" +
		"say(is_err($r), err_code($r))"
	out, err := runBackend(t, src, true)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "true 137\n" {
		t.Fatalf("bounded default = %q, want %q", out, "true 137\n")
	}
}

func TestWithStoreRejectsSiblingAndRollsBackWithoutLosingSuccessfulWrites(t *testing.T) {
	p := storeTestPath(t)
	src := "$s := store('" + p + "')\n" +
		"store_clear($s)\n" +
		"$entered := chan()\n" +
		"$release := chan()\n" +
		"$t := spawn(|$s, $entered, $release| with_store($s, |$tx| {\n" +
		"  store_set($tx, \"inside\", 1)\n" +
		"  send($entered, true)\n" +
		"  recv($release)\n" +
		"  fail(\"rollback\")\n" +
		"}), $s, $entered, $release)\n" +
		"recv($entered)\n" +
		"$outside := store_set($s, \"outside\", 2)\n" +
		"$close := store_close($s)\n" +
		"send($release, true)\n" +
		"$rolled := await($t)\n" +
		"say(is_err($outside), is_err($close), is_err($rolled), store_has($s, \"inside\"), store_has($s, \"outside\"))\n" +
		"say(store_set($s, \"outside\", 3), store_get($s, \"outside\"))"
	out, err := runBackend(t, src, true)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	want := "true true true false false\ntrue 3\n"
	if out != want {
		t.Fatalf("exclusive rollback = %q, want %q", out, want)
	}
}

func TestWithStoreNestedTransactionIsCatchableAndClearsOwner(t *testing.T) {
	p := storeTestPath(t)
	src := "$s := store('" + p + "')\n" +
		"store_clear($s)\n" +
		"$r := with_store($s, |$tx| with_store($tx, |$nested| true))\n" +
		"say(is_err($r), store_has($s, \"x\"))\n" +
		"say(store_set($s, \"x\", 1))"
	assertBoth(t, src, "true false\ntrue\n")
}

func TestWithStoreFirstClassBuiltinInChildKeepsSiblingStrand(t *testing.T) {
	p := storeTestPath(t)
	src := "$s := store('" + p + "')\n" +
		"store_clear($s)\n" +
		"$op := store_set\n" +
		"$r := with_store($s, |$tx| {\n" +
		"  $t := spawn(|$op, $s| $op($s, \"child\", 1), $op, $s)\n" +
		"  $child := await($t)\n" +
		"  if is_err($child) { store_set($tx, \"owner\", 1) } else { fail(\"child entered owner transaction\") }\n" +
		"})\n" +
		"say($r, store_has($s, \"child\"), store_has($s, \"owner\"))"
	out, err := runBackend(t, src, true)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "true false true\n" {
		t.Fatalf("first-class child store operation = %q, want %q", out, "true false true\n")
	}
}
