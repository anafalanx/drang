package eval

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/anafalanx/drang/internal/value"
)

func TestIsFileIsDirIsSymlink(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.txt")
	os.WriteFile(f, []byte("hi"), 0o644)

	if !callBuiltin(t, "is_file", str(f)).AsBool() {
		t.Error("is_file(regular file) should be true")
	}
	if callBuiltin(t, "is_file", str(dir)).AsBool() {
		t.Error("is_file(dir) should be false")
	}
	if !callBuiltin(t, "is_dir", str(dir)).AsBool() {
		t.Error("is_dir(dir) should be true")
	}
	if callBuiltin(t, "is_symlink", str(f)).AsBool() {
		t.Error("is_symlink(regular file) should be false")
	}
	// stat guards on a missing path are simply false, never an Err
	miss := filepath.Join(dir, "nope")
	if callBuiltin(t, "is_file", str(miss)).AsBool() || callBuiltin(t, "is_symlink", str(miss)).AsBool() {
		t.Error("stat guards on a missing path should be false")
	}
}

func TestSymlinkOps(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	os.WriteFile(target, []byte("x"), 0o644)
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create a symlink here (needs privilege / Developer Mode): %v", err)
	}
	if !callBuiltin(t, "is_symlink", str(link)).AsBool() {
		t.Error("is_symlink(link) should be true")
	}
	if callBuiltin(t, "is_symlink", str(target)).AsBool() {
		t.Error("is_symlink(target) should be false")
	}
	if got := callBuiltin(t, "readlink", str(link)); got.Tag() != value.Str || got.AsStr() != target {
		t.Errorf("readlink(link) = %s, want %q", got.Display(), target)
	}
}

func TestReadlinkNonSymlinkErr(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.txt")
	os.WriteFile(f, []byte("hi"), 0o644)
	if !callBuiltin(t, "readlink", str(f)).IsErr() {
		t.Error("readlink on a regular file should be a catchable Err")
	}
}

func TestWalk(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0o644)
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("yo"), 0o644)

	w := callBuiltin(t, "walk", str(dir))
	arr, ok := w.Obj().(*value.Array)
	if !ok {
		t.Fatalf("walk did not return an array: %s", w.Display())
	}
	if len(arr.Elems) != 3 {
		t.Errorf("walk found %d entries, want 3 (a.txt, sub, sub/b.txt)", len(arr.Elems))
	}
	for _, e := range arr.Elems {
		om := e.Obj().(*value.OrderedMap)
		for _, k := range []string{"name", "path", "is_dir", "is_symlink", "size", "mtime"} {
			if !om.Has(value.MakeStr(k)) {
				t.Errorf("walk record missing key %q", k)
			}
		}
	}
	// the root itself is not included
	for _, e := range arr.Elems {
		om := e.Obj().(*value.OrderedMap)
		if p, _ := om.Get(value.MakeStr("path")); p.AsStr() == dir {
			t.Error("walk should not include the root directory itself")
		}
	}
	if !callBuiltin(t, "walk", str(filepath.Join(dir, "a.txt"))).IsErr() {
		t.Error("walk on a file should be a catchable Err")
	}
	if !callBuiltin(t, "walk", str(filepath.Join(dir, "nope"))).IsErr() {
		t.Error("walk on a missing directory should be a catchable Err")
	}
}

func TestReadDirHasSymlinkField(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0o644)
	arr := callBuiltin(t, "read_dir", str(dir)).Obj().(*value.Array)
	if len(arr.Elems) != 1 {
		t.Fatalf("read_dir found %d entries, want 1", len(arr.Elems))
	}
	om := arr.Elems[0].Obj().(*value.OrderedMap)
	if !om.Has(value.MakeStr("is_symlink")) {
		t.Error("read_dir record should carry is_symlink")
	}
	if v, _ := om.Get(value.MakeStr("is_symlink")); v.AsBool() {
		t.Error("a.txt is_symlink should be false")
	}
}

func TestDrangPid(t *testing.T) {
	v := callBuiltin(t, "drang_pid")
	if v.Tag() != value.Int || v.AsInt() <= 0 {
		t.Errorf("drang_pid() = %s, want a positive int", v.Display())
	}
	if v.AsInt() != int64(os.Getpid()) {
		t.Errorf("drang_pid() = %d, want the process pid %d", v.AsInt(), os.Getpid())
	}
}

// derived, deterministic outputs match byte-for-byte on both backends
func TestCheapWinsParity(t *testing.T) {
	dir := filepath.ToSlash(t.TempDir()) // an existing, empty directory
	src := "say(is_file('" + dir + "'), is_dir('" + dir + "'), is_symlink('" + dir + "'))\n" +
		"say(drang_pid() > 0)\n" +
		"say(len(walk('" + dir + "')))"
	assertBoth(t, src, "false true false\ntrue\n0\n")
}

// --- one-liner -i (in-place edit) ---

func TestStreamInPlace(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f.txt")
	os.WriteFile(f, []byte("hello\nworld\n"), 0o644)
	prog := mustParseProg(t, `$_ = upper($_)`)
	if err := RunStream(prog, nil, StreamOpts{AutoPrint: true, InPlace: true, Files: []string{f}}); err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	got, _ := os.ReadFile(f)
	if string(got) != "HELLO\nWORLD\n" {
		t.Errorf("in-place result = %q, want %q", got, "HELLO\nWORLD\n")
	}
}

func TestStreamInPlaceBackup(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f.txt")
	os.WriteFile(f, []byte("a\nb\n"), 0o644)
	prog := mustParseProg(t, `$_ = "[" ~ $_ ~ "]"`)
	opts := StreamOpts{AutoPrint: true, InPlace: true, BackupSuffix: ".bak", Files: []string{f}}
	if err := RunStream(prog, nil, opts); err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	if got, _ := os.ReadFile(f); string(got) != "[a]\n[b]\n" {
		t.Errorf("edited file = %q, want %q", got, "[a]\n[b]\n")
	}
	if bak, _ := os.ReadFile(f + ".bak"); string(bak) != "a\nb\n" {
		t.Errorf("backup = %q, want the original %q", bak, "a\nb\n")
	}
}

func TestStreamInPlaceStdinErrors(t *testing.T) {
	prog := mustParseProg(t, `$_ = $_`)
	err := RunStream(prog, nil, StreamOpts{AutoPrint: true, InPlace: true, Files: []string{"-"}})
	if err == nil {
		t.Error("-i on stdin (\"-\") should error, not silently edit nothing")
	}
}
