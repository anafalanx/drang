package eval

import (
	"fmt"
	"strings"

	"github.com/anafalanx/drang/internal/value"
)

// CLI tooling helpers: stderr output and explicit process exit, so a drang script
// can behave like a well-formed command-line tool. Perl-flavored names alongside
// say/fail: warn (print to stderr), exit(code?), and die (stderr + exit 1).

// builtinWarn prints to stderr, exactly as say prints to stdout (same separator,
// same lock so parallel workers can't interleave with each other or with say).
func builtinWarn(args []value.Value) (value.Value, error) {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = a.Display()
	}
	outMu.Lock()
	fmt.Fprintln(stderr, strings.Join(parts, " "))
	outMu.Unlock()
	return value.MakeNil(), nil
}

// builtinExit ends the program with an exit code (default 0), clamped to 0..255.
// It unwinds past functions, loops, ?, and // to the top of the program.
func builtinExit(args []value.Value) (value.Value, error) {
	if len(args) > 1 {
		return value.MakeNil(), fmt.Errorf("exit expects 0 or 1 arguments (code?), got %d", len(args))
	}
	code := int64(0)
	if len(args) == 1 {
		if args[0].Tag() != value.Int {
			return value.MakeNil(), fmt.Errorf("exit code must be an int, got %s", args[0].TypeName())
		}
		code = args[0].AsInt()
	}
	return value.MakeNil(), exitSignal{code: clampExit(code)}
}

// builtinDie prints its message to stderr and exits with code 1 — the common
// fatal-error convention for a tool.
func builtinDie(args []value.Value) (value.Value, error) {
	_, _ = builtinWarn(args)
	return value.MakeNil(), exitSignal{code: 1}
}

func clampExit(code int64) int {
	switch {
	case code < 0:
		return 0
	case code > 255:
		return 255
	default:
		return int(code)
	}
}
