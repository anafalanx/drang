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
