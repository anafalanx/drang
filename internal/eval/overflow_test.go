package eval

import (
	"testing"

	"github.com/anafalanx/drang/internal/parser"
)

// TestIntOverflow: +/-/* that overflow int64 are errors (like division by zero),
// on the production VM path — including via compound assignment.
func TestIntOverflow(t *testing.T) {
	mustErr := func(src string) {
		t.Helper()
		p := parser.New(src)
		prog := p.ParseProgram()
		if errs := p.Errors(); len(errs) > 0 {
			t.Fatalf("parse %q: %v", src, errs)
		}
		if err := RunProgramVM(prog, NewEnv()); err == nil {
			t.Errorf("%s: expected an overflow error, got none", src)
		}
	}
	mustErr(`say(9223372036854775807 + 1)`)
	mustErr(`say(9223372036854775807 * 2)`)
	mustErr(`$x := 0 - 9223372036854775807; say($x - 2)`)
	mustErr(`$x := 9223372036854775807; $x += 1`)
}

// TestIntOverflowNoFalsePositives: ordinary arithmetic is unaffected.
func TestIntOverflowNoFalsePositives(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"near-max-add", `say(9223372036854775806 + 1)`, "9223372036854775807\n"},
		{"big-mul-ok", `say(1000000 * 1000000)`, "1000000000000\n"},
		{"neg-mul", `say(-5 * -5)`, "25\n"},
		{"sub-ok", `say(100 - 250)`, "-150\n"},
		{"sum-overflow-is-err", `say(is_err(sum([9223372036854775807, 1])))`, "true\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := run(t, c.src); got != c.want {
				t.Errorf("%s\n got %q\nwant %q", c.src, got, c.want)
			}
		})
	}
}
