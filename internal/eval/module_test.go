package eval

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anafalanx/drang/internal/parser"
)

func writeMod(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runMod runs a main program with its module base directory set to dir, returning
// the captured output and any runtime error (so error cases can assert on it).
func runMod(t *testing.T, dir, src string) (string, error) {
	t.Helper()
	p := parser.New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	var buf bytes.Buffer
	old := stdout
	stdout = &buf
	defer func() { stdout = old }()
	env := NewEnv()
	env.SetModuleDir(dir)
	err := RunProgramWithArgs(prog, env, nil)
	return buf.String(), err
}

func TestModuleFlatMerge(t *testing.T) {
	dir := t.TempDir()
	writeMod(t, dir, "util.dr", "export fn .shout($s) { upper($s) ~ \"!\" }\nexport $G ::= \"hi\"")
	out, err := runMod(t, dir, "use \"./util\"\nsay(.shout(\"hey\"))\nsay($G)")
	if err != nil {
		t.Fatal(err)
	}
	if out != "HEY!\nhi\n" {
		t.Errorf("got %q, want %q", out, "HEY!\nhi\n")
	}
}

// A `use`d module is a file too, so a duplicate top-level fn in it must warn on the
// import path — not only when the module is run directly (the runProgram CLI path).
func TestModuleDuplicateFnWarns(t *testing.T) {
	dir := t.TempDir()
	writeMod(t, dir, "dup.dr", "export fn .helper() { 1 }\nexport fn .helper() { 2 }")
	var errBuf bytes.Buffer
	oldErr := stderr
	stderr = &errBuf
	defer func() { stderr = oldErr }()
	out, err := runMod(t, dir, "use \"./dup\"\nsay(.helper())")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "2" { // last definition still wins (behavior unchanged)
		t.Errorf("last-wins broken: got stdout %q, want 2", out)
	}
	warn := errBuf.String()
	if !strings.Contains(warn, "defined more than once") || !strings.Contains(warn, "dup.dr") {
		t.Errorf("module dup-fn warning missing; stderr=%q", warn)
	}
}

func TestModuleIsolated(t *testing.T) {
	dir := t.TempDir()
	writeMod(t, dir, "util.dr", "export fn .shout($s) { upper($s) ~ \"!\" }\nexport $G ::= \"hi\"")
	out, err := runMod(t, dir, "$u := use(\"./util\")\nsay($u.shout(\"hey\"))\nsay($u.G)")
	if err != nil {
		t.Fatal(err)
	}
	if out != "HEY!\nhi\n" {
		t.Errorf("got %q, want %q", out, "HEY!\nhi\n")
	}
}

func TestModuleFrozenExportReject(t *testing.T) {
	dir := t.TempDir()
	writeMod(t, dir, "bad.dr", "fn .ok() { 1 }\n$scratch := []")
	_, err := runMod(t, dir, "use \"./bad\"")
	if err == nil || !strings.Contains(err.Error(), "mutable top-level state") {
		t.Errorf("want a frozen-export error, got %v", err)
	}
}

func TestModuleCollisionErrors(t *testing.T) {
	dir := t.TempDir()
	writeMod(t, dir, "util.dr", "export fn .shout($s) { $s }")
	_, err := runMod(t, dir, "fn .shout($x) { \"mine\" }\nuse \"./util\"")
	if err == nil || !strings.Contains(err.Error(), "already defined") {
		t.Errorf("want a collision error, got %v", err)
	}
}

func TestModuleCycleErrors(t *testing.T) {
	dir := t.TempDir()
	writeMod(t, dir, "a.dr", "export fn .a() { 1 }\nuse \"./b\"")
	writeMod(t, dir, "b.dr", "export fn .b() { 1 }\nuse \"./a\"")
	_, err := runMod(t, dir, "use \"./a\"")
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("want a cycle error, got %v", err)
	}
}

func TestModuleImportOnceDiamond(t *testing.T) {
	// A -> B -> D and A -> C -> D: D loads exactly once, and flat-merge is NOT
	// transitive (D's .d is not re-exported by B/C, so no collision in A).
	dir := t.TempDir()
	writeMod(t, dir, "d.dr", "say(\"loaded D\")\nexport fn .d() { \"D\" }")
	writeMod(t, dir, "b.dr", "use \"./d\"\nexport fn .b() { .d() }")
	writeMod(t, dir, "c.dr", "use \"./d\"\nexport fn .c() { .d() }")
	out, err := runMod(t, dir, "use \"./b\"\nuse \"./c\"\nsay(.b() ~ .c())")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "loaded D") != 1 {
		t.Errorf("D should load exactly once, got output %q", out)
	}
	if !strings.HasSuffix(out, "DD\n") {
		t.Errorf("got %q, want it to end with DD", out)
	}
}

func TestModuleMissingFileErrors(t *testing.T) {
	dir := t.TempDir()
	_, err := runMod(t, dir, "use \"./nope\"")
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("want a missing-module error, got %v", err)
	}
}

func TestModuleExitPropagatesThroughCapturedUse(t *testing.T) {
	// exit()/die() during an import must terminate the program, even via the
	// catchable $u := use(...) form (it is NOT downgraded to a recoverable Err).
	dir := t.TempDir()
	writeMod(t, dir, "ex.dr", "say(\"in module\")\nexit(3)\nexport fn .x() { 1 }")
	_, err := runMod(t, dir, "$u := use(\"./ex\")\nsay(\"after\")")
	code, ok := ExitRequested(err)
	if !ok || code != 3 {
		t.Errorf("exit(3) in a module should propagate as code 3, got err=%v ok=%v code=%d", err, ok, code)
	}
}

func TestModuleConstNotTransitivelyReExported(t *testing.T) {
	// B flat-merges D (a $CONST among them); A imports B. D's constant must NOT be
	// re-exported by B — flat-merge is non-transitive for constants as well.
	dir := t.TempDir()
	writeMod(t, dir, "d.dr", "export $DSECRET ::= \"d-secret\"\nexport fn .d() { 1 }")
	writeMod(t, dir, "b.dr", "use \"./d\"\nexport fn .b() { .d() }")
	out, err := runMod(t, dir, "$u := use(\"./b\")\nsay(keys($u))")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "DSECRET") {
		t.Errorf("D's constant was transitively re-exported by B: %q", out)
	}
}

func TestModuleUseInsidePmapNoFalseCycle(t *testing.T) {
	// Concurrent first-loads of the same module from pmap workers must not
	// false-trigger the import-cycle check (cycle detection is per import chain).
	dir := t.TempDir()
	writeMod(t, dir, "tiny.dr", "export fn .val() { 42 }")
	out, err := runMod(t, dir, "$res := [1,2,3,4,5,6,7,8] |> pmap(|$x| { use(\"./tiny\") })\nsay(len($res))")
	if err != nil {
		t.Fatalf("use inside pmap errored (false cycle?): %v", err)
	}
	if strings.TrimSpace(out) != "8" {
		t.Errorf("got %q, want 8 records", out)
	}
}

func TestModuleConcurrentFirstLoadRunsOnce(t *testing.T) {
	dir := t.TempDir()
	writeMod(t, dir, "once.dr", "say(\"loaded once\")\nexport fn .val() { 42 }")
	out, err := runMod(t, dir, "$res := [1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16] |> pmap(|$x| use(\"./once\"))\nsay(len($res))")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out, "loaded once"); n != 1 {
		t.Fatalf("concurrent first import evaluated module %d times; output=%q", n, out)
	}
	if !strings.HasSuffix(out, "16\n") {
		t.Fatalf("unexpected result after concurrent import: %q", out)
	}
}

// Per-path single-flight must not turn a concurrent A->B / B->A cycle into two goroutines waiting
// forever. The wait graph rejects one edge, both leaders unwind, and pmap surfaces an Err.
func TestModuleConcurrentCycleDoesNotDeadlock(t *testing.T) {
	dir := t.TempDir()
	writeMod(t, dir, "a.dr", "sleep(0.1)?\nuse \"./b\"\nexport fn .a() { 1 }")
	writeMod(t, dir, "b.dr", "sleep(0.1)?\nuse \"./a\"\nexport fn .b() { 1 }")
	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := runMod(t, dir, `$r := ["a", "b"] |> pmap(|$name| {
			if $name == "a" { use("./a") } else { use("./b") }
		})
		say(is_err($r))`)
		done <- result{out: out, err: err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("concurrent cycle aborted the whole run: %v", got.err)
		}
		if strings.TrimSpace(got.out) != "true" {
			t.Fatalf("concurrent cycle should surface an Err, got %q", got.out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent module cycle deadlocked single-flight waiters")
	}
}

func TestModuleCacheIsScopedToEnvSession(t *testing.T) {
	dir := t.TempDir()
	writeMod(t, dir, "session.dr", `say("loaded-v1")
export $VERSION ::= "v1"`)
	out, err := runMod(t, dir, `$a := use("./session")
$b := use("./session")
say($a.VERSION ~ "/" ~ $b.VERSION)`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "loaded-v1") != 1 || !strings.HasSuffix(out, "v1/v1\n") {
		t.Fatalf("same-session imports did not share one result: %q", out)
	}

	// A new top-level Env is a new run/session. It must not inherit a stale module closure or
	// completed cache entry from the previous run.
	writeMod(t, dir, "session.dr", `say("loaded-v2")
export $VERSION ::= "v2"`)
	out, err = runMod(t, dir, `$m := use("./session"); say($m.VERSION)`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "loaded-v2") != 1 || !strings.HasSuffix(out, "v2\n") {
		t.Fatalf("new session reused stale module cache: %q", out)
	}
}

func TestReadFileBoundedRejectsSentinelByte(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized")
	if err := os.WriteFile(path, []byte("123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readFileBounded(path, 8, "test input"); err == nil || !strings.Contains(err.Error(), "8-byte limit") {
		t.Fatalf("bounded read error = %v, want 8-byte limit", err)
	}
	b, err := readFileBounded(path, 9, "test input")
	if err != nil || string(b) != "123456789" {
		t.Fatalf("exact-limit read = %q, %v", b, err)
	}
}

func TestModuleExportIsFrozen(t *testing.T) {
	// A constant array exported by a module is immutable: pushing to it (via the
	// flat-merged binding) is a catchable error, not a silent mutation.
	dir := t.TempDir()
	writeMod(t, dir, "data.dr", "export $LIST ::= [1,2,3]\nfn .get() { $LIST }")
	out, err := runMod(t, dir, "use \"./data\"\nsay(push($LIST, 4) // \"frozen!\")")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "frozen!" {
		t.Errorf("expected the exported array to be frozen, got %q", out)
	}
}

func TestModuleExportIndexAssignRejected(t *testing.T) {
	dir := t.TempDir()
	writeMod(t, dir, "data.dr", "export $M ::= {\"a\": 1}\nfn .x() { 1 }")
	_, err := runMod(t, dir, "$u := use(\"./data\")\n$u.M[\"a\"] = 99")
	if err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Errorf("expected a frozen-map error on index-assign, got %v", err)
	}
}

func TestModuleExportNoCachePoisoning(t *testing.T) {
	// The original Boundary-1 bug: an importer mutating an export poisoned the
	// shared cache. Now the mutation is rejected, so a later import sees the original.
	dir := t.TempDir()
	writeMod(t, dir, "reg.dr", "export $REGISTRY ::= [\"a\",\"b\"]\nfn .reg() { $REGISTRY }")
	out, err := runMod(t, dir, "$u := use(\"./reg\")\npush($u.REGISTRY, \"POISON\")\n$v := use(\"./reg\")\nsay(len($v.REGISTRY))")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "2" {
		t.Errorf("export cache was poisoned: expected len 2, got %q", out)
	}
}

func TestModulePrivateNamesNotExported(t *testing.T) {
	// Unmarked top-level names are module-private: absent from the export record
	// (captured form) and never injected by a flat merge.
	dir := t.TempDir()
	writeMod(t, dir, "vis.dr",
		"export fn .pub() { .priv() }\nfn .priv() { \"inner\" }\nexport $PUB ::= 1\n$PRIV ::= 2")
	out, err := runMod(t, dir, "$u := use(\"./vis\")\nsay(join(keys($u), \",\"))\nsay($u.pub())")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "pub,PUB") { // sorted env-key order: ".pub" < "PUB" in ASCII
		t.Errorf("export record should hold exactly pub and PUB, got %q", out)
	}
	if strings.Contains(out, "priv") || strings.Contains(out, "PRIV") {
		t.Errorf("private names leaked into the export record: %q", out)
	}
	if !strings.Contains(out, "inner") {
		t.Errorf("an exported fn must still reach its private sibling, got %q", out)
	}
	// Flat merge: the private fn must not be injected, so a caller naming it errors.
	_, err = runMod(t, dir, "use \"./vis\"\n.priv()")
	if err == nil {
		t.Error("calling a private module fn after flat merge should fail, but succeeded")
	}
}

func TestModulePrivateAllowsHelperCollision(t *testing.T) {
	// The point of privacy for flat merge: two modules with same-named PRIVATE
	// helpers can both be merged — helper names are no longer a cross-module contract.
	dir := t.TempDir()
	writeMod(t, dir, "left.dr", "export fn .l() { .check() }\nfn .check() { \"L\" }")
	writeMod(t, dir, "right.dr", "export fn .r() { .check() }\nfn .check() { \"R\" }")
	out, err := runMod(t, dir, "use \"./left\"\nuse \"./right\"\nsay(.l() ~ .r())")
	if err != nil {
		t.Fatalf("same-named private helpers must not collide on merge: %v", err)
	}
	if strings.TrimSpace(out) != "LR" {
		t.Errorf("each module must keep its own private helper: got %q, want LR", out)
	}
}

func TestModuleExportsNothingWarns(t *testing.T) {
	// An all-private module is almost always a pre-`export` migration miss — warn.
	dir := t.TempDir()
	writeMod(t, dir, "quiet.dr", "fn .helper() { 1 }")
	var errBuf bytes.Buffer
	oldErr := stderr
	stderr = &errBuf
	defer func() { stderr = oldErr }()
	if _, err := runMod(t, dir, "$u := use(\"./quiet\")\nsay(len(keys($u)))"); err != nil {
		t.Fatal(err)
	}
	if warn := errBuf.String(); !strings.Contains(warn, "exports nothing") {
		t.Errorf("expected the exports-nothing warning, stderr=%q", warn)
	}
}

func TestModuleExportedSubmoduleReExport(t *testing.T) {
	// Deliberate re-export: `export $sub ::= use("./sub")` exposes the captured record.
	dir := t.TempDir()
	writeMod(t, dir, "inner.dr", "export fn .greet() { \"hi\" }")
	writeMod(t, dir, "outer.dr", "export $inner ::= use(\"./inner\")\nexport fn .o() { 1 }")
	out, err := runMod(t, dir, "$u := use(\"./outer\")\nsay($u.inner.greet())")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "hi" {
		t.Errorf("re-exported submodule should be reachable as $u.inner.greet(): got %q", out)
	}
}

func TestModuleFailedLoadNotCached(t *testing.T) {
	// A failed load must not be cached, or it would poison a later valid import.
	dir := t.TempDir()
	if _, err := runMod(t, dir, "$u := use(\"./later\")"); err != nil {
		t.Fatalf("a missing module via the captured form should be catchable, got %v", err)
	}
	writeMod(t, dir, "later.dr", "export fn .hi() { \"hello\" }")
	out, err := runMod(t, dir, "$u := use(\"./later\")\nsay($u.hi())")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "hello" {
		t.Errorf("failed load appears to have been cached/poisoned: got %q", out)
	}
}
