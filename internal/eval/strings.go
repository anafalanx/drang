package eval

import (
	"fmt"
	"strings"

	"github.com/anafalanx/drang/internal/value"
)

// builtinSplit splits a string. With one arg it splits on runs of whitespace;
// with an empty separator it splits into runes; otherwise it splits on the sep.
func builtinSplit(args []value.Value) (value.Value, error) {
	if len(args) < 1 || len(args) > 2 {
		return value.MakeNil(), fmt.Errorf("split expects 1 or 2 arguments (s, sep?), got %d", len(args))
	}
	if args[0].Tag() != value.Str {
		return value.MakeNil(), fmt.Errorf("split expects a string, got %s", args[0].TypeName())
	}
	s := args[0].AsStr()
	var parts []string
	switch {
	case len(args) == 1:
		parts = strings.Fields(s)
	default:
		if args[1].Tag() != value.Str {
			return value.MakeNil(), fmt.Errorf("split separator must be a string, got %s", args[1].TypeName())
		}
		sep := args[1].AsStr()
		if sep == "" {
			for _, r := range s {
				parts = append(parts, string(r))
			}
		} else {
			parts = strings.Split(s, sep)
		}
	}
	out := make([]value.Value, len(parts))
	for i, p := range parts {
		out[i] = value.MakeStr(p)
	}
	return value.MakeArray(out), nil
}

func builtinReplace(args []value.Value) (value.Value, error) {
	if len(args) != 3 {
		return value.MakeNil(), fmt.Errorf("replace expects 3 arguments (s, old, new), got %d", len(args))
	}
	for i, a := range args {
		if a.Tag() != value.Str {
			return value.MakeNil(), fmt.Errorf("replace: argument %d must be a string, got %s", i+1, a.TypeName())
		}
	}
	return value.MakeStr(strings.ReplaceAll(args[0].AsStr(), args[1].AsStr(), args[2].AsStr())), nil
}

// builtinTrim trims whitespace, or the given cutset of characters.
func builtinTrim(args []value.Value) (value.Value, error) {
	if len(args) < 1 || len(args) > 2 {
		return value.MakeNil(), fmt.Errorf("trim expects 1 or 2 arguments (s, cutset?), got %d", len(args))
	}
	if args[0].Tag() != value.Str {
		return value.MakeNil(), fmt.Errorf("trim expects a string, got %s", args[0].TypeName())
	}
	if len(args) == 2 {
		if args[1].Tag() != value.Str {
			return value.MakeNil(), fmt.Errorf("trim cutset must be a string, got %s", args[1].TypeName())
		}
		return value.MakeStr(strings.Trim(args[0].AsStr(), args[1].AsStr())), nil
	}
	return value.MakeStr(strings.TrimSpace(args[0].AsStr())), nil
}

func builtinUpper(args []value.Value) (value.Value, error) {
	s, err := oneString("upper", args)
	if err != nil {
		return value.MakeNil(), err
	}
	return value.MakeStr(strings.ToUpper(s)), nil
}

func builtinLower(args []value.Value) (value.Value, error) {
	s, err := oneString("lower", args)
	if err != nil {
		return value.MakeNil(), err
	}
	return value.MakeStr(strings.ToLower(s)), nil
}

func builtinStartsWith(args []value.Value) (value.Value, error) {
	s, p, err := twoStrings("starts_with", args)
	if err != nil {
		return value.MakeNil(), err
	}
	return value.MakeBool(strings.HasPrefix(s, p)), nil
}

func builtinEndsWith(args []value.Value) (value.Value, error) {
	s, p, err := twoStrings("ends_with", args)
	if err != nil {
		return value.MakeNil(), err
	}
	return value.MakeBool(strings.HasSuffix(s, p)), nil
}

// builtinFormat substitutes each {} placeholder with the next argument (rendered
// like say), or {:spec} with the argument formatted per a Python/Rust-style spec
// (see format.go); {{ and }} are literal braces, and any other brace run is left
// literal. The placeholder count must equal the argument count, otherwise it returns
// a catchable Err (so a printf-style format("%s", x), which has no placeholders,
// fails loudly instead of dropping the arg).
func builtinFormat(args []value.Value) (value.Value, error) {
	if len(args) < 1 {
		return value.MakeNil(), fmt.Errorf("format expects at least a format string")
	}
	if args[0].Tag() != value.Str {
		return value.MakeNil(), fmt.Errorf("format expects a string, got %s", args[0].TypeName())
	}
	f := args[0].AsStr()
	rest := args[1:]
	ai, holes := 0, 0
	var b strings.Builder
	for i := 0; i < len(f); i++ {
		c := f[i]
		if c == '}' {
			if i+1 < len(f) && f[i+1] == '}' { // literal }}
				i++
			}
			b.WriteByte('}')
			continue
		}
		if c != '{' {
			b.WriteByte(c)
			continue
		}
		if i+1 < len(f) && f[i+1] == '{' { // literal {{
			b.WriteByte('{')
			i++
			continue
		}
		// A placeholder only if it is {} or {:spec}; any other {...} stays literal.
		end := strings.IndexByte(f[i+1:], '}')
		inner := ""
		if end >= 0 {
			inner = f[i+1 : i+1+end]
		}
		if end < 0 || !(inner == "" || inner[0] == ':') {
			b.WriteByte('{')
			continue
		}
		i += 1 + end // advance to the closing '}'
		holes++
		if ai >= len(rest) { // too few args — reported by the arity check below
			ai++
			continue
		}
		arg := rest[ai]
		ai++
		if inner == "" {
			b.WriteString(arg.Display())
			continue
		}
		s, err := formatArg(inner[1:], arg)
		if err != nil {
			return value.MakeErr("format: "+err.Error(), 1), nil
		}
		b.WriteString(s)
	}
	// Strict arity: one placeholder per argument and vice versa. Catches the common
	// printf habit (format("%s", x) has no placeholders) and over/under-supply, as a
	// catchable Err rather than silently dropping or emitting a literal brace run.
	if holes != len(rest) {
		return value.MakeErr(fmt.Sprintf("format: template has %d placeholder(s) but got %d argument(s)", holes, len(rest)), 1), nil
	}
	return value.MakeStr(b.String()), nil
}

// builtinLines splits text into lines (CRLF-normalized), dropping a single
// trailing newline so "a\nb\n" yields ["a", "b"] and "" yields [].
func builtinLines(args []value.Value) (value.Value, error) {
	s, err := oneString("lines", args)
	if err != nil {
		return value.MakeNil(), err
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return value.MakeArray(nil), nil
	}
	parts := strings.Split(s, "\n")
	out := make([]value.Value, len(parts))
	for i, p := range parts {
		out[i] = value.MakeStr(p)
	}
	return value.MakeArray(out), nil
}

func builtinRepeat(args []value.Value) (value.Value, error) {
	if len(args) != 2 {
		return value.MakeNil(), fmt.Errorf("repeat expects 2 arguments (s, n), got %d", len(args))
	}
	if args[0].Tag() != value.Str {
		return value.MakeNil(), fmt.Errorf("repeat expects a string, got %s", args[0].TypeName())
	}
	if args[1].Tag() != value.Int {
		return value.MakeNil(), fmt.Errorf("repeat count must be an int, got %s", args[1].TypeName())
	}
	n := args[1].AsInt()
	if n < 0 {
		return value.MakeErr("repeat: negative count", 1), nil
	}
	s := args[0].AsStr()
	// Cap the result so an oversized count yields a catchable Err instead of a
	// strings.Repeat allocation panic.
	const maxLen = 1 << 30 // 1 GiB
	if len(s) > 0 && n > int64(maxLen/len(s)) {
		return value.MakeErr(fmt.Sprintf("repeat: result too large (%d copies of %d bytes)", n, len(s)), 1), nil
	}
	return value.MakeStr(strings.Repeat(s, int(n))), nil
}

// joinStrings implements the array form of the polymorphic join builtin:
// join(array, sep?) renders each element (like say) and joins them with sep.
func joinStrings(args []value.Value) (value.Value, error) {
	if len(args) > 2 {
		return value.MakeNil(), fmt.Errorf("join(array, sep) takes at most 2 arguments, got %d", len(args))
	}
	arr := args[0].Obj().(*value.Array)
	sep := ""
	if len(args) == 2 {
		sep = args[1].Display()
	}
	parts := make([]string, len(arr.Elems))
	for i, e := range arr.Elems {
		parts[i] = e.Display()
	}
	return value.MakeStr(strings.Join(parts, sep)), nil
}
