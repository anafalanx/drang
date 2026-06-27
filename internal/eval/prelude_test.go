package eval

import (
	"bytes"
	"testing"

	"github.com/anafalanx/drang/internal/parser"
)

// runP runs src with the prelude loaded — the real program environment, where the
// drang-written stdlib is available. (The plain run() helper uses a bare env.)
func runP(t *testing.T, src string) string {
	t.Helper()
	p := parser.New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse errors for %q: %v", src, errs)
	}
	var buf bytes.Buffer
	old := stdout
	stdout = &buf
	defer func() { stdout = old }()
	env := NewEnv()
	if err := RunPrelude(env); err != nil {
		t.Fatalf("prelude: %v", err)
	}
	if err := RunProgram(prog, env); err != nil {
		t.Fatalf("runtime error for %q: %v", src, err)
	}
	return buf.String()
}

func TestPrelude(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"flatten", `say(to_json(flatten([[1,2],[3],[4,5]])))`, `[1,2,3,4,5]` + "\n"},
		{"flatten-empty", `say(to_json(flatten([])))`, "[]\n"},
		{"sum_by", `say(sum_by([{n: 3}, {n: 4}], |$x| $x.n))`, "7\n"},
		{"tally", `say(to_json(tally(["a", "a", "b"])))`, `{"a":2,"b":1}` + "\n"},
		{"count_by", `say(to_json(count_by(["aa", "bb", "c"], |$s| len($s))))`, `{"2":2,"1":1}` + "\n"},
		{"chunk", `say(to_json(chunk([1,2,3,4,5], 2)))`, `[[1,2],[3,4],[5]]` + "\n"},
		{"chunk-exact", `say(to_json(chunk([1,2,3,4], 2)))`, `[[1,2],[3,4]]` + "\n"},
		{"chunk-big-n", `say(to_json(chunk([1,2], 5)))`, `[[1,2]]` + "\n"},
		{"chunk-empty", `say(to_json(chunk([], 2)))`, "[]\n"},
		{"zip", `say(to_json(zip([1,2,3], ["a", "b"])))`, `[[1,"a"],[2,"b"]]` + "\n"},
		{"zip-empty", `say(to_json(zip([], [1,2])))`, "[]\n"},
		// a user .flatten coexists with the bare prelude flatten — no shadow, no clobber
		{"user-dot-coexists-with-prelude", `fn .flatten($x) { "mine" }  say(.flatten([1]) ~ "/" ~ to_json(flatten([[1],[2]])))`, "mine/[1,2]\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := runP(t, c.src); got != c.want {
				t.Errorf("%s\n got %q\nwant %q", c.src, got, c.want)
			}
		})
	}
}

func TestRunPreludeDefinesGlobals(t *testing.T) {
	env := NewEnv()
	if err := RunPrelude(env); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"flatten", "sum_by", "tally", "count_by", "chunk", "zip"} {
		if _, ok := env.get(name); !ok {
			t.Errorf("prelude did not define %q", name)
		}
	}
}
