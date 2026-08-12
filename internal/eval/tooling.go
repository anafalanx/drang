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
	b := newLimitedStringBuilder(maxStringBytes)
	for i, a := range args {
		if i > 0 {
			_ = b.WriteByte(' ')
		}
		s, ok := displayWithin(a, maxStringBytes-int64(b.Len()))
		if !ok {
			return value.MakeNil(), fmt.Errorf("warn: result exceeds the %d-byte string limit", maxStringBytes)
		}
		_, _ = b.WriteString(s)
		if err := b.Err(); err != nil {
			return value.MakeNil(), fmt.Errorf("warn: %w", err)
		}
	}
	outMu.Lock()
	_, err := fmt.Fprintln(stderr, b.String())
	outMu.Unlock()
	if err != nil {
		return value.MakeNil(), fmt.Errorf("warn: write stderr: %w", err)
	}
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
	if _, err := builtinWarn(args); err != nil {
		return value.MakeNil(), err
	}
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
			if !valueOpts[o.AsStr()] && len(valueOpts) >= maxCollectionItems {
				return value.MakeErr(fmt.Sprintf("parse_args: value_opts exceeds the %d-item collection limit", maxCollectionItems), 1), nil
			}
			valueOpts[o.AsStr()] = true
		}
	}

	out := value.MakeMap()
	m := out.Obj().(*value.OrderedMap)
	var positionals []value.Value
	optsEnded := false
	resultHasRoom := func(add int) bool {
		// Reserve one map entry for the positional "_" key, even when its array
		// is empty, because it is always present in the returned shape.
		used := len(positionals) + m.Len() + 1
		return add >= 0 && add <= maxCollectionItems-used
	}
	appendPositional := func(s string) bool {
		if !resultHasRoom(1) {
			return false
		}
		positionals = append(positionals, value.MakeStr(s))
		return true
	}
	setOption := func(key string, v value.Value) bool {
		kv := value.MakeStr(key)
		if !m.Has(kv) && !resultHasRoom(1) {
			return false
		}
		m.Set(kv, v)
		return true
	}
	if !resultHasRoom(0) {
		return value.MakeErr(fmt.Sprintf("parse_args: result exceeds the %d-item collection limit", maxCollectionItems), 1), nil
	}

	for i := 0; i < len(argv); i++ {
		a := argv[i].AsStr()
		switch {
		case optsEnded || a == "" || a == "-" || !strings.HasPrefix(a, "-"):
			if !appendPositional(a) {
				return value.MakeErr(fmt.Sprintf("parse_args: result exceeds the %d-item collection limit", maxCollectionItems), 1), nil
			}
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
				if !appendPositional(a) {
					return value.MakeErr(fmt.Sprintf("parse_args: result exceeds the %d-item collection limit", maxCollectionItems), 1), nil
				}
			case eq >= 0:
				if !setOption(key, value.MakeStr(name[eq+1:])) {
					return value.MakeErr(fmt.Sprintf("parse_args: result exceeds the %d-item collection limit", maxCollectionItems), 1), nil
				}
			case valueOpts[key]:
				// Consume the next token as the value, but never the `--` terminator.
				if i+1 < len(argv) && argv[i+1].AsStr() != "--" {
					i++
					if !setOption(key, value.MakeStr(argv[i].AsStr())) {
						return value.MakeErr(fmt.Sprintf("parse_args: result exceeds the %d-item collection limit", maxCollectionItems), 1), nil
					}
				} else {
					if !setOption(key, value.MakeStr("")) { // value missing (end of args or "--")
						return value.MakeErr(fmt.Sprintf("parse_args: result exceeds the %d-item collection limit", maxCollectionItems), 1), nil
					}
				}
			default:
				if !setOption(key, value.MakeBool(true)) {
					return value.MakeErr(fmt.Sprintf("parse_args: result exceeds the %d-item collection limit", maxCollectionItems), 1), nil
				}
			}
		}
	}
	m.Set(value.MakeStr("_"), value.MakeArray(positionals))
	return out, nil
}

// clampExit coerces an explicit exit(code) into a valid process status. exit(0) is a
// deliberate success, so 0 passes through; a NEGATIVE code is a failure, not success,
// so it maps to 1 — the same negative→1 rule the Err-dispatch clampCode uses, so the
// two exit paths agree.
func clampExit(code int64) int {
	switch {
	case code < 0:
		return 1
	case code > 255:
		return 255
	default:
		return int(code)
	}
}
