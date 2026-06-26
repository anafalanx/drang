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

// builtinParseArgs parses a command-line argv array (all elements must be strings)
// into a flat map: each `--flag`/`-f` becomes a `true` field, each `--key=val` (or
// `--key val` when `key` is listed in the optional value_opts array) becomes a
// string field, and everything else — plus anything after a `--` terminator and a
// lone `-` — lands in the positional array under the `"_"` key. It is permissive:
// unknown options are not errors and duplicates keep the last value. `"_"` is
// reserved for positionals, so a literal `--_` is kept as a positional (never
// colliding with or dropping the positionals array).
func builtinParseArgs(args []value.Value) (value.Value, error) {
	if len(args) < 1 || len(args) > 2 {
		return value.MakeNil(), fmt.Errorf("parse_args expects 1 or 2 arguments (argv, value_opts?), got %d", len(args))
	}
	if args[0].Tag() != value.Arr {
		return value.MakeNil(), fmt.Errorf("parse_args expects an array of arguments, got %s", args[0].TypeName())
	}
	argv := args[0].Obj().(*value.Array).Elems
	for _, a := range argv { // a real argv is strings; reject non-strings rather than coerce -5 into a flag
		if a.Tag() != value.Str {
			return value.MakeNil(), fmt.Errorf("parse_args argv elements must be strings, got %s", a.TypeName())
		}
	}

	valueOpts := map[string]bool{} // option names (no dashes) that consume a following value
	if len(args) == 2 {
		if args[1].Tag() != value.Arr {
			return value.MakeNil(), fmt.Errorf("parse_args value_opts must be an array of names, got %s", args[1].TypeName())
		}
		for _, o := range args[1].Obj().(*value.Array).Elems {
			if o.Tag() != value.Str {
				return value.MakeNil(), fmt.Errorf("parse_args value_opts names must be strings, got %s", o.TypeName())
			}
			valueOpts[o.AsStr()] = true
		}
	}

	out := value.MakeMap()
	m := out.Obj().(*value.OrderedMap)
	var positionals []value.Value
	optsEnded := false

	for i := 0; i < len(argv); i++ {
		a := argv[i].AsStr()
		switch {
		case optsEnded || a == "" || a == "-" || !strings.HasPrefix(a, "-"):
			positionals = append(positionals, value.MakeStr(a))
		case a == "--":
			optsEnded = true
		default:
			name := strings.TrimLeft(a, "-")
			key := name
			eq := strings.IndexByte(name, '=')
			if eq >= 0 {
				key = name[:eq]
			}
			switch {
			case key == "_":
				// "_" is reserved for positionals; keep the raw token instead of
				// overwriting the positionals array or dropping the value.
				positionals = append(positionals, value.MakeStr(a))
			case eq >= 0:
				m.Set(value.MakeStr(key), value.MakeStr(name[eq+1:]))
			case valueOpts[key]:
				// Consume the next token as the value, but never the `--` terminator.
				if i+1 < len(argv) && argv[i+1].AsStr() != "--" {
					i++
					m.Set(value.MakeStr(key), value.MakeStr(argv[i].AsStr()))
				} else {
					m.Set(value.MakeStr(key), value.MakeStr("")) // value missing (end of args or "--")
				}
			default:
				m.Set(value.MakeStr(key), value.MakeBool(true))
			}
		}
	}
	m.Set(value.MakeStr("_"), value.MakeArray(positionals))
	return out, nil
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
