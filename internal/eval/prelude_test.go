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
		// collections
		{"group_by", `say(to_json(group_by([1,2,3,4], |$x| $x % 2)))`, `{"1":[1,3],"0":[2,4]}` + "\n"},
		{"partition", `say(to_json(partition([1,2,3,4], |$x| $x % 2 == 0)))`, `[[2,4],[1,3]]` + "\n"},
		{"uniq_by", `say(to_json(uniq_by([1,2,2,3,1], |$x| $x)))`, `[1,2,3]` + "\n"},
		{"enumerate", `say(to_json(enumerate(["a","b"])))`, `[[0,"a"],[1,"b"]]` + "\n"},
		// stats
		{"mean", `say(mean([2,4,6]))`, "4\n"},
		{"mean-empty", `say(mean([]) // "empty")`, "empty\n"},
		{"median-odd", `say(median([3,1,2]))`, "2\n"},
		{"median-even", `say(median([1,2,3,4]))`, "2.5\n"},
		// set ops
		{"intersect", `say(to_json(intersect([1,2,2,3], [2,3,4])))`, `[2,3]` + "\n"},
		{"union", `say(to_json(union([1,2], [2,3])))`, `[1,2,3]` + "\n"},
		{"difference", `say(to_json(difference([1,2,3], [2])))`, `[1,3]` + "\n"},
		// strings
		{"pad", `say("[" ~ pad("ab", 5) ~ "]")`, "[ab   ]\n"},
		{"pad-overflow", `say("[" ~ pad("abcdef", 3) ~ "]")`, "[abcdef]\n"},
		{"capitalize", `say(capitalize("hELLO"))`, "Hello\n"},
		{"reverse", `say(reverse("abc"))`, "cba\n"},
		{"reverse-rune", `say(reverse("héllo"))`, "olléh\n"},
		{"dedent", `say(dedent("    a\n      b") == "a\n  b")`, "true\n"},
		// numbers
		{"clamp", `say(clamp(15, 0, 10), clamp(-3, 0, 10), clamp(5, 0, 10))`, "10 0 5\n"},
		{"sign", `say(sign(-4), sign(0), sign(2.5))`, "-1 0 1\n"},
		// nested data
		{"get_in", `say(get_in({"a": {"b": [10, 20]}}, ["a", "b", 1]))`, "20\n"},
		{"get_in-miss", `say(get_in({"a": 1}, ["b"]) // "miss")`, "miss\n"},
		{"get_in-oob", `say(get_in({"a": [1, 2]}, ["a", 5]) // "oob")`, "oob\n"},
		{"deep_merge", `say(to_json(deep_merge({"a": 1, "x": {"p": 1}}, {"x": {"q": 2}})))`, `{"a":1,"x":{"p":1,"q":2}}` + "\n"},
		// control
		{"retry-succeeds", "$n := 0\n$r := retry(3, 0, || { $n = $n + 1; if $n < 3 { return fail(\"x\") } $n })\nsay($r)", "3\n"},
		{"retry-all-fail", `say(is_err(retry(2, 0, || fail("no"))))`, "true\n"},
		// a first-class builtin passed to a prelude HOF
		{"group_by-builtin", `say(to_json(group_by(["aa", "bb", "c"], len)))`, `{"2":["aa","bb"],"1":["c"]}` + "\n"},
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
	for _, name := range []string{
		"flatten", "sum_by", "tally", "count_by", "chunk", "zip",
		"group_by", "partition", "uniq_by", "enumerate", "mean", "median",
		"intersect", "union", "difference", "pad", "capitalize", "reverse",
		"dedent", "clamp", "sign", "get_in", "deep_merge", "retry",
	} {
		if _, ok := env.get(name); !ok {
			t.Errorf("prelude did not define %q", name)
		}
	}
}
