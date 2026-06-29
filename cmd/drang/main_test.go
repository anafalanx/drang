package main

import (
	"strings"
	"testing"
)

// TestREPL drives the REPL loop over a scripted session and checks that state
// persists across lines, multi-line submissions work, results are echoed, and a
// parse error does not kill the loop.
func TestREPL(t *testing.T) {
	script := strings.Join([]string{
		`1 + 2`,          // expression -> 3
		`$x := 10`,       // declaration persists, echoes 10
		`$x * 5`,         // reads persisted $x -> 50
		`fn .sq($n) {`,   // multi-line: continues on "...>"
		`$n * $n`,        //
		`}`,              // function persists
		`.sq(9)`,         // -> 81 (runs the defined function)
		`$"v=$x"`,        // interpolation + persisted var -> v=10
		`@@@`,            // garbage -> parse error; loop must recover
		`100 + 1`,        // -> 101 proves recovery
		`fn .noop() { }`, // declaration -> nil, must NOT echo a value line
		`exit`,
	}, "\n") + "\n"

	var out strings.Builder
	replLoop(strings.NewReader(script), &out)
	got := out.String()

	for _, want := range []string{"50", "81", "v=10", "101"} {
		if !strings.Contains(got, want) {
			t.Errorf("REPL output missing %q\n--- full output ---\n%s", want, got)
		}
	}
	if strings.Contains(got, "error") && !strings.Contains(got, "101") {
		t.Errorf("REPL did not recover after a parse error\n%s", got)
	}
}
