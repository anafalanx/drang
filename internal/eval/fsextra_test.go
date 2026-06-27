package eval

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/anafalanx/drang/internal/value"
)

func TestPathAndEnvBuiltins(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"is_abs-rel", `say(is_abs("rel/path"))`, "false\n"},
		{"is_abs-cwd", `say(is_abs(cwd()))`, "true\n"},
		{"clean", `say(slash(clean("a/./b/../c")))`, "a/c\n"},
		{"rel", `say(slash(rel("a/b", "a/b/c/d")))`, "c/d\n"},
		{"within-true", `say(within("a/b", "a/b/c"))`, "true\n"},
		{"within-eq", `say(within("a/b", "a/b"))`, "true\n"},
		{"within-false", `say(within("a/b", "a/x"))`, "false\n"},
		{"within-escape", `say(within("a/b", "a/b/../.."))`, "false\n"},
		{"path-list-sep-len", `say(len(path_list_sep()))`, "1\n"},
		{"env-default", `say(env("DRANG_UNSET_VAR_XYZ", "dflt"))`, "dflt\n"},
		{"env-nil-recovers", `say(env("DRANG_UNSET_VAR_XYZ") // "wasnil")`, "wasnil\n"},
		{"env-path-nonempty", `say(len(env("PATH")) > 0)`, "true\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := run(t, c.src); got != c.want {
				t.Errorf("%s\n got %q\nwant %q", c.src, got, c.want)
			}
		})
	}
}

func TestReadDir(t *testing.T) {
	dir := t.TempDir()
	must := func(e error) {
		if e != nil {
			t.Fatal(e)
		}
	}
	must(os.WriteFile(filepath.Join(dir, "b.txt"), []byte("x"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644))
	must(os.Mkdir(filepath.Join(dir, "sub"), 0o755))

	v, err := builtinReadDir([]value.Value{value.MakeStr(dir)})
	if err != nil || v.IsErr() {
		t.Fatalf("read_dir: err=%v v=%s", err, v.Display())
	}
	arr := v.Obj().(*value.Array).Elems
	if len(arr) != 3 {
		t.Fatalf("got %d entries, want 3", len(arr))
	}
	// os.ReadDir is sorted by name: a.txt, b.txt, sub
	first := arr[0].Obj().(*value.OrderedMap)
	if name, _ := first.Get(value.MakeStr("name")); name.AsStr() != "a.txt" {
		t.Errorf("first entry name = %q, want a.txt", name.AsStr())
	}
	sub := arr[2].Obj().(*value.OrderedMap)
	if isDir, _ := sub.Get(value.MakeStr("is_dir")); !isDir.AsBool() {
		t.Errorf("sub should be is_dir=true")
	}
	if p, _ := sub.Get(value.MakeStr("path")); p.AsStr() != filepath.Join(dir, "sub") {
		t.Errorf("sub path = %q", p.AsStr())
	}

	// A missing directory is a catchable Err, not an abort.
	v2, err2 := builtinReadDir([]value.Value{value.MakeStr(filepath.Join(dir, "nope"))})
	if err2 != nil || !v2.IsErr() {
		t.Errorf("read_dir of missing dir: want Err value, got err=%v v=%s", err2, v2.Display())
	}
}
