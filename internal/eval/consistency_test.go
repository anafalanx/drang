package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestErrPropagationHasPosition: a `?` that propagates an Err to the top level (and a for-in over
// an unhandled Err) now carries a source position, on both backends.
func TestErrPropagationHasPosition(t *testing.T) {
	cases := []string{
		"$e := fail(\"boom\")\n$e?",
		"$e := fail(\"boom\")\nfor $x in $e { }",
	}
	for _, src := range cases {
		for _, vm := range []bool{false, true} {
			_, err := runBackend(t, src, vm)
			if err == nil {
				t.Fatalf("vm=%v: expected a top-level abort for %q", vm, src)
			}
			if line, _, ok := ErrorPos(err); !ok || line == 0 {
				t.Errorf("vm=%v: propagated error lacks a source position (%q): %v", vm, src, err)
			}
		}
	}
}

// TestWalkerRuntimeErrorHasPosition: an ordinary aborting runtime error on the tree-walker path
// (one-liner mode, VM fallback) now carries a source position, like the VM already did.
func TestWalkerRuntimeErrorHasPosition(t *testing.T) {
	_, err := runBackend(t, `say("a" + 5)`, false) // false => tree-walker
	if err == nil {
		t.Fatal("expected an abort")
	}
	if line, _, ok := ErrorPos(err); !ok || line == 0 {
		t.Errorf("walker runtime error lacks a position: %v", err)
	}
}

// TestErrorPositionParity: both backends locate an error on the same LINE with a caret. The exact
// column within the line can differ for a general expression abort (the VM stamps per-instruction,
// the walker per-node) — invisible in practice since a program runs on one backend and the error
// message is identical. Control-flow propagation (?) is stamped from the same node on both, so it
// matches exactly.
func TestErrorPositionParity(t *testing.T) {
	exact := map[string]bool{"$e := fail(\"x\")\n$e?": true} // ?-propagation matches column too
	for _, src := range []string{`say("a" + 5)`, "$e := fail(\"x\")\n$e?"} {
		_, wErr := runBackend(t, src, false)
		_, vErr := runBackend(t, src, true)
		wl, wc, wok := ErrorPos(wErr)
		vl, vc, vok := ErrorPos(vErr)
		if !wok || !vok {
			t.Fatalf("%q: missing position: walker ok=%v vm ok=%v", src, wok, vok)
		}
		if wl != vl {
			t.Errorf("line mismatch for %q: walker=%d vm=%d", src, wl, vl)
		}
		if wc < 1 || vc < 1 {
			t.Errorf("%q: a caret column is missing: walker=%d vm=%d", src, wc, vc)
		}
		if exact[src] && wc != vc {
			t.Errorf("column mismatch for %q: walker=%d vm=%d", src, wc, vc)
		}
	}
}

// TestStringFamilyTypeErrorsCatchable: wrong-TYPE arguments to string/fs/encoding/json builtins
// are catchable Err values (composing with // and ?), matching math/array/csv — not hard aborts.
func TestStringFamilyTypeErrorsCatchable(t *testing.T) {
	for _, src := range []string{
		`say(is_err(upper(5)))`,
		`say(is_err(trim(5)))`,
		`say(is_err(split(5)))`,
		`say(is_err(replace(5, "a", "b")))`,
		`say(is_err(basename(5)))`,       // path helper (former carve-out) now catchable too
		`say(is_err(join(5, "x")))`,      // path-join form
		`say(is_err(read_file(5)))`,
		`say(is_err(sha256(5)))`,
		`say(is_err(to_base64(5)))`,
		`say(is_err(from_json(5)))`,
	} {
		assertBoth(t, src, "true\n")
	}
	assertBoth(t, `say(upper(5) // "R")`, "R\n")                                 // composes with //
	assertBoth(t, `say(err_msg(upper(5)))`, "upper expects a string, got int\n") // message preserved
}

// TestWrongArgCountStillAborts: a wrong argument COUNT remains a program abort (a Go error), not a
// catchable value — the deliberate split kept by the unification.
func TestWrongArgCountStillAborts(t *testing.T) {
	for _, vm := range []bool{false, true} {
		if _, err := runBackend(t, `upper("a", "b")`, vm); err == nil {
			t.Errorf("vm=%v: wrong arg count should abort, not be catchable", vm)
		}
	}
}

// TestDupStringifiedMapKeyRejected: distinct scalar keys that stringify identically (int 1 vs
// "1") would produce invalid JSON / duplicate CSV headers, so to_json/to_csv reject them (catchable).
func TestDupStringifiedMapKeyRejected(t *testing.T) {
	assertBoth(t, `say(is_err(to_json({1: "a", "1": "b"})))`, "true\n")
	assertBoth(t, `say(is_err(to_csv([{1: "a", "1": "b"}])))`, "true\n")
	assertBoth(t, `say(to_json({a: 1, b: 2}))`, "{\"a\":1,\"b\":2}\n") // normal case unaffected
}

// TestMapLiteralUnhashableKeyFailFast: an unhashable map-literal key short-circuits BEFORE the
// entry's value side effect (and later entries) run — identically on both backends.
func TestMapLiteralUnhashableKeyFailFast(t *testing.T) {
	src := `$log := []
$m := {[1]: push($log, "ran")} // "caught"
say($m ~ " " ~ str(len($log)))`
	assertBoth(t, src, "caught 0\n")
}

// TestStartRejectsTimeout: start is detached/unbounded, so {timeout} is rejected as a catchable
// Err rather than silently ignored.
func TestStartRejectsTimeout(t *testing.T) {
	assertBoth(t, `say(is_err(start("cmd", "/c", "exit", {timeout: 100})))`, "true\n")
}

// TestArg0RejectedForBatch: cmd.exe controls a batch file's argv[0], so {arg0} on a .bat/.cmd
// target is rejected as a catchable Err rather than silently ignored. (arg0 on a real exe still
// works — covered elsewhere.)
func TestArg0RejectedForBatch(t *testing.T) {
	dir := t.TempDir()
	bat := filepath.Join(dir, "a.bat")
	if err := os.WriteFile(bat, []byte("@echo off\r\necho hi\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// single-quoted (raw) drang string: the Windows backslashes are literal, not escapes
	src := "say(is_err(capture(['" + bat + "', 'hi'], {arg0: 'PRETEND'})))"
	assertBoth(t, src, "true\n")
}

// TestExecNotFoundSinglePrefix guards the fix for the double/triple `exec:` prefix in the
// command-not-found message: it must appear exactly once (matching the documented form and the
// env-PATH branch).
func TestExecNotFoundSinglePrefix(t *testing.T) {
	out := run(t, `say(err_msg(run("nope_xyz_no_such")))`)
	if n := strings.Count(out, "exec:"); n != 1 {
		t.Errorf("expected exactly one \"exec:\" prefix, got %d in %q", n, out)
	}
	if !strings.Contains(out, "executable file not found") {
		t.Errorf("message lost its reason: %q", out)
	}
}

// TestRepeatBadCountCatchable: a wrong-type repeat count is a catchable Err, like take/drop and
// like repeat's own negative/oversized-count paths.
func TestRepeatBadCountCatchable(t *testing.T) {
	assertBoth(t, `say(is_err(repeat("x", "5")))`, "true\n")
	assertBoth(t, `say(repeat("ab", 3))`, "ababab\n") // the good path is unaffected
}

// TestDatetimeOptValidation: a misspelled datetime option is rejected (catchable) rather than
// silently falling back to local time; the correct key still works.
func TestDatetimeOptValidation(t *testing.T) {
	assertBoth(t, `say(is_err(strftime(0, "%Y", {UTC: true})))`, "true\n")
	assertBoth(t, `say(is_err(strftime(0, "%Y", 5)))`, "true\n") // non-map opts rejected too
	assertBoth(t, `say(strftime(0, "%Y", {utc: true}))`, "1970\n")
	assertBoth(t, `say(is_err(date_parts(0, {bogus: 1})))`, "true\n")
}

// TestWriteFileOptValidation: an unknown write_file option is rejected (catchable) rather than
// silently ignored. The Err is returned before the file is opened, so the path is never touched.
func TestWriteFileOptValidation(t *testing.T) {
	assertBoth(t, `say(is_err(write_file("_never_created.txt", "hi", {apend: true})))`, "true\n")
}

// TestIndexOfArray: index_of is now polymorphic over arrays (first structurally-equal element
// index, else -1), the sibling of the polymorphic contains(); the string form is unchanged.
func TestIndexOfArray(t *testing.T) {
	assertBoth(t, `say(index_of([10, 20, 30], 20))`, "1\n")
	assertBoth(t, `say(index_of([10, 20], 99))`, "-1\n")
	assertBoth(t, `say(index_of(["a", "b", "c"], "c"))`, "2\n")
	assertBoth(t, `say(index_of([[1], [2]], [2]))`, "1\n") // structural equality, like contains
	assertBoth(t, `say(index_of("hello", "ll"))`, "2\n")   // string form unchanged
}
