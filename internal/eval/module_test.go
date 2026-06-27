package eval

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	writeMod(t, dir, "util.dr", "fn .shout($s) { upper($s) ~ \"!\" }\n$G ::= \"hi\"")
	out, err := runMod(t, dir, "use \"./util\"\nsay(.shout(\"hey\"))\nsay($G)")
	if err != nil {
		t.Fatal(err)
	}
	if out != "HEY!\nhi\n" {
		t.Errorf("got %q, want %q", out, "HEY!\nhi\n")
	}
}

func TestModuleIsolated(t *testing.T) {
	dir := t.TempDir()
	writeMod(t, dir, "util.dr", "fn .shout($s) { upper($s) ~ \"!\" }\n$G ::= \"hi\"")
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
	if err == nil || !strings.Contains(err.Error(), "only functions and constants") {
		t.Errorf("want a frozen-export error, got %v", err)
	}
}

func TestModuleCollisionErrors(t *testing.T) {
	dir := t.TempDir()
	writeMod(t, dir, "util.dr", "fn .shout($s) { $s }")
	_, err := runMod(t, dir, "fn .shout($x) { \"mine\" }\nuse \"./util\"")
	if err == nil || !strings.Contains(err.Error(), "already defined") {
		t.Errorf("want a collision error, got %v", err)
	}
}

func TestModuleCycleErrors(t *testing.T) {
	dir := t.TempDir()
	writeMod(t, dir, "a.dr", "fn .a() { 1 }\nuse \"./b\"")
	writeMod(t, dir, "b.dr", "fn .b() { 1 }\nuse \"./a\"")
	_, err := runMod(t, dir, "use \"./a\"")
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("want a cycle error, got %v", err)
	}
}

func TestModuleImportOnceDiamond(t *testing.T) {
	// A -> B -> D and A -> C -> D: D loads exactly once, and flat-merge is NOT
	// transitive (D's .d is not re-exported by B/C, so no collision in A).
	dir := t.TempDir()
	writeMod(t, dir, "d.dr", "say(\"loaded D\")\nfn .d() { \"D\" }")
	writeMod(t, dir, "b.dr", "use \"./d\"\nfn .b() { .d() }")
	writeMod(t, dir, "c.dr", "use \"./d\"\nfn .c() { .d() }")
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
	writeMod(t, dir, "ex.dr", "say(\"in module\")\nexit(3)\nfn .x() { 1 }")
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
	writeMod(t, dir, "d.dr", "$DSECRET ::= \"d-secret\"\nfn .d() { 1 }")
	writeMod(t, dir, "b.dr", "use \"./d\"\nfn .b() { .d() }")
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
	writeMod(t, dir, "tiny.dr", "fn .val() { 42 }")
	out, err := runMod(t, dir, "$res := [1,2,3,4,5,6,7,8] |> pmap(|$x| { use(\"./tiny\") })\nsay(len($res))")
	if err != nil {
		t.Fatalf("use inside pmap errored (false cycle?): %v", err)
	}
	if strings.TrimSpace(out) != "8" {
		t.Errorf("got %q, want 8 records", out)
	}
}

func TestModuleExportIsFrozen(t *testing.T) {
	// A constant array exported by a module is immutable: pushing to it (via the
	// flat-merged binding) is a catchable error, not a silent mutation.
	dir := t.TempDir()
	writeMod(t, dir, "data.dr", "$LIST ::= [1,2,3]\nfn .get() { $LIST }")
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
	writeMod(t, dir, "data.dr", "$M ::= {\"a\": 1}\nfn .x() { 1 }")
	_, err := runMod(t, dir, "$u := use(\"./data\")\n$u.M[\"a\"] = 99")
	if err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Errorf("expected a frozen-map error on index-assign, got %v", err)
	}
}

func TestModuleExportNoCachePoisoning(t *testing.T) {
	// The original Boundary-1 bug: an importer mutating an export poisoned the
	// shared cache. Now the mutation is rejected, so a later import sees the original.
	dir := t.TempDir()
	writeMod(t, dir, "reg.dr", "$REGISTRY ::= [\"a\",\"b\"]\nfn .reg() { $REGISTRY }")
	out, err := runMod(t, dir, "$u := use(\"./reg\")\npush($u.REGISTRY, \"POISON\")\n$v := use(\"./reg\")\nsay(len($v.REGISTRY))")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "2" {
		t.Errorf("export cache was poisoned: expected len 2, got %q", out)
	}
}

func TestModuleFailedLoadNotCached(t *testing.T) {
	// A failed load must not be cached, or it would poison a later valid import.
	dir := t.TempDir()
	if _, err := runMod(t, dir, "$u := use(\"./later\")"); err != nil {
		t.Fatalf("a missing module via the captured form should be catchable, got %v", err)
	}
	writeMod(t, dir, "later.dr", "fn .hi() { \"hello\" }")
	out, err := runMod(t, dir, "$u := use(\"./later\")\nsay($u.hi())")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "hello" {
		t.Errorf("failed load appears to have been cached/poisoned: got %q", out)
	}
}
