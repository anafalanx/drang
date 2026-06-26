package eval

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anafalanx/drang/internal/parser"
	"github.com/anafalanx/drang/internal/value"
)

func str(s string) value.Value { return value.MakeStr(s) }

// callBuiltin invokes a registered builtin, failing the test on an aborting
// (Go) error. A returned Err VALUE is passed back to the caller to inspect.
func callBuiltin(t *testing.T, name string, args ...value.Value) value.Value {
	t.Helper()
	b, ok := builtins[name]
	if !ok {
		t.Fatalf("no builtin %q", name)
	}
	v, err := b(args)
	if err != nil {
		t.Fatalf("%s aborted: %v", name, err)
	}
	return v
}

func TestPathHelpers(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"dirname", callBuiltin(t, "dirname", str("a/b/c.txt")).AsStr(), filepath.FromSlash("a/b")},
		{"basename", callBuiltin(t, "basename", str("a/b/c.txt")).AsStr(), "c.txt"},
		{"ext", callBuiltin(t, "ext", str("a/b/c.txt")).AsStr(), ".txt"},
		{"stem", callBuiltin(t, "stem", str("a/b/c.txt")).AsStr(), "c"},
		{"slash", callBuiltin(t, "slash", str(filepath.Join("a", "b"))).AsStr(), "a/b"},
		{"join", callBuiltin(t, "join", str("a"), str("b"), str("c")).AsStr(), filepath.Join("a", "b", "c")},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
	// non-string argument aborts
	if _, err := builtins["dirname"]([]value.Value{value.MakeInt(1)}); err == nil {
		t.Error("dirname of a non-string should abort")
	}
}

func TestFilesystemOps(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "x.txt")

	if v := callBuiltin(t, "write_file", str(f), str("hello")); v.AsStr() != f {
		t.Errorf("write_file returned %q", v.AsStr())
	}
	if v := callBuiltin(t, "read_file", str(f)); v.AsStr() != "hello" {
		t.Errorf("read_file = %q", v.AsStr())
	}
	if v := callBuiltin(t, "size", str(f)); v.AsInt() != 5 {
		t.Errorf("size = %d, want 5", v.AsInt())
	}
	if !callBuiltin(t, "exists", str(f)).AsBool() {
		t.Error("exists(file) = false")
	}
	if callBuiltin(t, "isdir", str(f)).AsBool() {
		t.Error("isdir(file) = true")
	}
	if !callBuiltin(t, "isdir", str(dir)).AsBool() {
		t.Error("isdir(dir) = false")
	}
	if callBuiltin(t, "exists", str(filepath.Join(dir, "nope"))).AsBool() {
		t.Error("exists(missing) = true")
	}

	// read_file of a missing file is a catchable Err, not an abort
	if v := callBuiltin(t, "read_file", str(filepath.Join(dir, "nope"))); !v.IsErr() {
		t.Error("read_file(missing) should be an Err value")
	}

	// glob: flat vs recursive
	sub := filepath.Join(dir, "sub")
	callBuiltin(t, "mkdir", str(sub))
	callBuiltin(t, "write_file", str(filepath.Join(sub, "y.txt")), str("deep"))
	if g := callBuiltin(t, "glob", str(filepath.Join(dir, "*.txt"))); g.Obj().Len() != 1 {
		t.Errorf("flat glob len = %d, want 1", g.Obj().Len())
	}
	if g := callBuiltin(t, "glob", str(filepath.Join(dir, "**", "*.txt"))); g.Obj().Len() != 2 {
		t.Errorf("recursive glob len = %d, want 2", g.Obj().Len())
	}
	if g := callBuiltin(t, "glob", str(filepath.Join(dir, "*.none"))); g.Obj().Len() != 0 {
		t.Errorf("no-match glob len = %d, want 0 (empty, not error)", g.Obj().Len())
	}

	// a bare ** enumerates the tree CONTENTS, never the base dir itself
	bare := callBuiltin(t, "glob", str(filepath.Join(dir, "**")))
	for _, e := range bare.Obj().(*value.Array).Elems {
		if e.AsStr() == dir {
			t.Errorf("glob(dir/**) must not include the base dir %q", dir)
		}
	}
	if bare.Obj().Len() != 3 { // x.txt, sub, sub/y.txt
		t.Errorf("glob(dir/**) len = %d, want 3", bare.Obj().Len())
	}
	// a malformed pattern is a catchable Err on BOTH the plain and ** paths
	if !callBuiltin(t, "glob", str("a[b")).IsErr() {
		t.Error("glob(malformed) plain should be an Err")
	}
	if !callBuiltin(t, "glob", str("**/a[b")).IsErr() {
		t.Error("glob(malformed) ** path should be an Err")
	}

	// copy, rename, rm
	c := filepath.Join(dir, "c.txt")
	callBuiltin(t, "copy", str(f), str(c))
	if callBuiltin(t, "read_file", str(c)).AsStr() != "hello" {
		t.Error("copy did not preserve content")
	}
	r := filepath.Join(dir, "r.txt")
	callBuiltin(t, "rename", str(c), str(r))
	if callBuiltin(t, "exists", str(c)).AsBool() {
		t.Error("rename left the source behind")
	}
	if !callBuiltin(t, "exists", str(r)).AsBool() {
		t.Error("rename did not create the destination")
	}
	callBuiltin(t, "rm", str(r))
	if callBuiltin(t, "exists", str(r)).AsBool() {
		t.Error("rm did not remove the file")
	}
	// rm of a missing path is idempotent (no error)
	if v := callBuiltin(t, "rm", str(filepath.Join(dir, "gone"))); v.IsErr() {
		t.Error("rm(missing) should be idempotent")
	}
}

func TestNewerAndStale(t *testing.T) {
	dir := t.TempDir()
	older := filepath.Join(dir, "older")
	newerF := filepath.Join(dir, "newer")
	if err := os.WriteFile(older, []byte("o"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newerF, []byte("n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t0 := time.Unix(1_000_000, 0)
	t1 := time.Unix(2_000_000, 0)
	os.Chtimes(older, t0, t0)
	os.Chtimes(newerF, t1, t1)

	if !callBuiltin(t, "newer", str(newerF), str(older)).AsBool() {
		t.Error("newer(new, old) = false")
	}
	if callBuiltin(t, "newer", str(older), str(newerF)).AsBool() {
		t.Error("newer(old, new) = true")
	}
	// missing operand -> catchable Err
	if v := callBuiltin(t, "newer", str(filepath.Join(dir, "gone")), str(older)); !v.IsErr() {
		t.Error("newer with a missing operand should be an Err value")
	}

	srcs := value.MakeArray([]value.Value{str(newerF)})
	if !callBuiltin(t, "stale", str(older), srcs).AsBool() {
		t.Error("stale(old-target, [newer-source]) should be true")
	}
	if callBuiltin(t, "stale", str(newerF), value.MakeArray([]value.Value{str(older)})).AsBool() {
		t.Error("stale(new-target, [older-source]) should be false")
	}
	if !callBuiltin(t, "stale", str(filepath.Join(dir, "missing")), srcs).AsBool() {
		t.Error("stale(missing-target, ...) should be true")
	}
	// missing source -> Err
	if v := callBuiltin(t, "stale", str(older), value.MakeArray([]value.Value{str(filepath.Join(dir, "gone"))})); !v.IsErr() {
		t.Error("stale with a missing source should be an Err value")
	}
}

func TestExecArgFlatten(t *testing.T) {
	args := []value.Value{
		str("gcc"),
		value.MakeArray([]value.Value{str("-O2"), str("-c")}),
		value.MakeInt(5),
	}
	got, err := execArgStrings("run", args)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gcc", "-O2", "-c", "5"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("arg %d = %q, want %q", i, got[i], want[i])
		}
	}
	// a nested (second-level) array aborts
	nested := []value.Value{value.MakeArray([]value.Value{value.MakeArray(nil)})}
	if _, err := execArgStrings("run", nested); err == nil {
		t.Error("a nested array argument should abort")
	}
}

func TestMergeEnvCaseInsensitive(t *testing.T) {
	os.Setenv("DRANG_TESTVAR", "orig")
	defer os.Unsetenv("DRANG_TESTVAR")
	overlay := value.MakeMap().Obj().(*value.OrderedMap)
	overlay.Set(str("drang_testvar"), str("new")) // different case on purpose
	env := mergeEnv(overlay)
	count, val := 0, ""
	for _, e := range env {
		if i := strings.IndexByte(e, '='); i >= 0 && strings.EqualFold(e[:i], "DRANG_TESTVAR") {
			count++
			val = e[i+1:]
		}
	}
	if count != 1 || val != "new" {
		t.Errorf("merge replaced %d entries with %q, want 1 entry of %q", count, val, "new")
	}
}

func TestExecErrorCannotStart(t *testing.T) {
	e := execError("nope", errors.New("not found"), "")
	if !e.IsErr() || e.ErrCode() != 127 {
		t.Errorf("cannot-start error code = %d, want 127", e.ErrCode())
	}
}

func TestDispatchResolve(t *testing.T) {
	var buf bytes.Buffer
	oldOut, oldErr := stdout, stderr
	stdout, stderr = &buf, &buf
	defer func() { stdout, stderr = oldOut, oldErr }()

	env := NewEnv()
	src := `fn ok($a) { say("ran", $a) }
fn boom($a) { fail("kaboom")? }
fn noargs() { say("noargs") }`
	p := parser.New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	if err := RunProgram(prog, env); err != nil {
		t.Fatalf("run: %v", err)
	}
	tasks := value.MakeMap().Obj().(*value.OrderedMap)
	for _, n := range []string{"ok", "boom", "noargs"} {
		v, _ := env.get(n)
		tasks.Set(value.MakeStr(n), v)
	}

	cases := []struct {
		argv []string
		code int
	}{
		{[]string{"ok", "x"}, 0},   // runs, success
		{[]string{"noargs"}, 0},    // zero-param task
		{[]string{"boom"}, 1},      // ?-propagated Err -> code 1
		{[]string{"nope"}, 2},      // unknown task
		{nil, 0},                   // list
		{[]string{"--list"}, 0},    // list keyword
	}
	for _, c := range cases {
		code, err := dispatchResolve(tasks, c.argv)
		if err != nil {
			t.Errorf("dispatchResolve(%v) aborted: %v", c.argv, err)
			continue
		}
		if code != c.code {
			t.Errorf("dispatchResolve(%v) = %d, want %d", c.argv, code, c.code)
		}
	}

	// a non-function task value is an aborting error
	bad := value.MakeMap().Obj().(*value.OrderedMap)
	bad.Set(value.MakeStr("x"), value.MakeInt(1))
	if _, err := dispatchResolve(bad, []string{"x"}); err == nil {
		t.Error("a non-function task should abort")
	}
}

// TestDispatchStreamRouting checks that the unknown-task diagnostic (header AND
// task list) goes entirely to stderr, while --list writes the listing to stdout.
func TestDispatchStreamRouting(t *testing.T) {
	var out, errb bytes.Buffer
	oldOut, oldErr := stdout, stderr
	stdout, stderr = &out, &errb
	defer func() { stdout, stderr = oldOut, oldErr }()

	env := NewEnv()
	p := parser.New(`fn build($a) { say("b") }`)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	if err := RunProgram(prog, env); err != nil {
		t.Fatalf("run: %v", err)
	}
	tasks := value.MakeMap().Obj().(*value.OrderedMap)
	bv, _ := env.get("build")
	tasks.Set(value.MakeStr("build"), bv)

	// unknown task: nothing on stdout; header + list on stderr
	if code, _ := dispatchResolve(tasks, []string{"nope"}); code != 2 {
		t.Errorf("unknown task code = %d, want 2", code)
	}
	if out.Len() != 0 {
		t.Errorf("unknown-task path wrote to stdout: %q", out.String())
	}
	if !strings.Contains(errb.String(), "unknown task") || !strings.Contains(errb.String(), "build") {
		t.Errorf("stderr missing full diagnostic: %q", errb.String())
	}

	// --list: the listing is the result, so it goes to stdout
	out.Reset()
	errb.Reset()
	dispatchResolve(tasks, []string{"--list"})
	if !strings.Contains(out.String(), "build") {
		t.Errorf("--list should write to stdout, got stdout=%q stderr=%q", out.String(), errb.String())
	}
}

func TestSafeBuiltinRecoversPanic(t *testing.T) {
	panicky := func(args []value.Value) (value.Value, error) { panic("boom") }
	v, err := safeBuiltin("panicky", panicky, nil)
	if err != nil {
		t.Fatalf("safeBuiltin returned a Go error instead of recovering: %v", err)
	}
	if !v.IsErr() {
		t.Errorf("a panicking builtin should yield a catchable Err value, got %q", v.Display())
	}
}

func TestRepeatHugeIsCatchable(t *testing.T) {
	v := callBuiltin(t, "repeat", str("a"), value.MakeInt(9_999_999_999_999_999))
	if !v.IsErr() {
		t.Errorf("repeat with a huge count should be a catchable Err, got %q", v.Display())
	}
}

func TestRegexArgErrorMessage(t *testing.T) {
	// A non-string, non-regex pattern is a catchable Err value (type errors are
	// recoverable with //, matching the builtin convention), not an abort.
	got, err := builtins["matches"]([]value.Value{str("a"), value.MakeInt(5)})
	if err != nil {
		t.Fatalf("matches type error should be a value, not an abort: %v", err)
	}
	if !got.IsErr() {
		t.Fatalf("matches with a non-string/regex pattern should yield an Err, got %v", got)
	}
	if msg := got.ErrMsg(); strings.Contains(msg, "path") || !strings.Contains(msg, "must be a string") {
		t.Errorf("regex arg error message should mention strings, not paths: %q", msg)
	}
}

// TestPmapRaceSafe runs pmap over many elements, each saying from its worker
// (exercising the output mutex) and returning a value, then checks ordering and
// completeness. Run under `go test -race` to catch data races.
func TestPmapRaceSafe(t *testing.T) {
	var buf bytes.Buffer
	oldOut := stdout
	stdout = &buf
	defer func() { stdout = oldOut }()

	env := NewEnv()
	const N = 300
	items := make([]value.Value, N)
	for i := range items {
		items[i] = value.MakeInt(int64(i))
	}
	env.define("items", value.MakeArray(items), false)

	src := `$r := pmap($items, |$x| { say($x); $x * 2 })
say("len", len($r))
say("ends", $r[0], $r[len($r) - 1])`
	p := parser.New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	if err := RunProgram(prog, env); err != nil {
		t.Fatalf("run: %v", err)
	}
	out := buf.String()
	if got := strings.Count(out, "\n"); got != N+2 {
		t.Errorf("line count = %d, want %d", got, N+2)
	}
	if !strings.Contains(out, "len 300") {
		t.Error("missing len summary")
	}
	if !strings.Contains(out, "ends 0 598") {
		t.Error("result ordering wrong: want result[0]=0, result[299]=598")
	}
}
