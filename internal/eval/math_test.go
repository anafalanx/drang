package eval

import "testing"

func TestNumericBuiltins(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"abs-int", `say(abs(-5))`, "5\n"},
		{"abs-float", `say(abs(-5.5))`, "5.5\n"},
		{"abs-pos", `say(abs(7))`, "7\n"},
		{"sum-array-int", `say(sum([1, 2, 3, 4]))`, "10\n"},
		{"sum-variadic", `say(sum(1, 2, 3))`, "6\n"},
		{"sum-mixed-float", `say(sum([1, 2.5]))`, "3.5\n"},
		{"sum-empty", `say(sum([]))`, "0\n"},
		{"min-array", `say(min([3, 1, 2]))`, "1\n"},
		{"max-variadic", `say(max(3, 9, 2))`, "9\n"},
		{"min-preserves-float", `say(min([2, 1.5]))`, "1.5\n"},
		{"floor", `say(floor(2.7))`, "2\n"},
		{"ceil", `say(ceil(2.1))`, "3\n"},
		{"round", `say(round(2.5))`, "3\n"},
		{"floor-int-passthrough", `say(floor(5))`, "5\n"},
		{"abspath-is-abs", `say(is_abs(abspath("x")))`, "true\n"},

		// error paths are catchable Err values, not aborts
		{"abs-bad-type", `say(is_err(abs("x")))`, "true\n"},
		{"min-empty-err", `say(is_err(min([])))`, "true\n"},
		{"sum-bad-elem", `say(is_err(sum([1, "x"])))`, "true\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := run(t, c.src); got != c.want {
				t.Errorf("%s\n got %q\nwant %q", c.src, got, c.want)
			}
		})
	}
}
