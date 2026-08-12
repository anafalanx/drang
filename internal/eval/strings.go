package eval

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/anafalanx/drang/internal/value"
)

// builtinFindIndex returns the index of the first occurrence of needle, or -1 if absent —
// the "where" sibling of find (find returns the element, this returns its position). For a
// string subject the index is in runes (to match chars()/slicing), not bytes, and an empty
// needle matches at 0. For an array subject
// it returns the first element index whose value structurally equals needle — the sibling
// of the polymorphic contains(). A subject that is neither a string nor an array aborts
// (the string-builtin convention).
func builtinFindIndex(args []value.Value) (value.Value, error) {
	if len(args) == 2 && args[0].Tag() == value.Arr {
		elems := args[0].Obj().(*value.Array).Elems
		for i, el := range elems {
			if value.Equal(el, args[1]) {
				return value.MakeInt(int64(i)), nil
			}
		}
		return value.MakeInt(-1), nil
	}
	s, needle, err := twoStrings("find_index", args)
	if err != nil {
		return value.MakeNil(), err
	}
	b := strings.Index(s, needle)
	if b < 0 {
		return value.MakeInt(-1), nil
	}
	return value.MakeInt(int64(utf8.RuneCountInString(s[:b]))), nil
}

// builtinSplit splits a string. With one arg it splits on runs of whitespace;
// with an empty separator it splits into runes; otherwise it splits on the sep.
func builtinSplit(args []value.Value) (value.Value, error) {
	if len(args) < 1 || len(args) > 2 {
		return value.MakeNil(), fmt.Errorf("split expects 1 or 2 arguments (s, sep?), got %d", len(args))
	}
	if args[0].Tag() != value.Str {
		return value.MakeNil(), typeErrf("split expects a string, got %s", args[0].TypeName())
	}
	s := args[0].AsStr()
	if int64(len(s)) > maxStringBytes {
		return value.MakeErr(fmt.Sprintf("split: input exceeds the %d-byte string limit", maxStringBytes), 1), nil
	}
	var parts []string
	switch {
	case len(args) == 1:
		if countFieldsOver(s, maxCollectionItems) {
			return value.MakeErr(fmt.Sprintf("split: result exceeds the %d-element collection limit", maxCollectionItems), 1), nil
		}
		parts = strings.Fields(s)
	default:
		if args[1].Tag() != value.Str {
			return value.MakeNil(), typeErrf("split separator must be a string, got %s", args[1].TypeName())
		}
		sep := args[1].AsStr()
		if sep == "" {
			if utf8.RuneCountInString(s) > maxCollectionItems {
				return value.MakeErr(fmt.Sprintf("split: result exceeds the %d-element collection limit", maxCollectionItems), 1), nil
			}
			for _, r := range s {
				parts = append(parts, string(r))
			}
		} else {
			if strings.Count(s, sep)+1 > maxCollectionItems {
				return value.MakeErr(fmt.Sprintf("split: result exceeds the %d-element collection limit", maxCollectionItems), 1), nil
			}
			parts = strings.Split(s, sep)
		}
	}
	out := make([]value.Value, len(parts))
	for i, p := range parts {
		out[i] = value.MakeStr(p)
	}
	return value.MakeArray(out), nil
}

func countFieldsOver(s string, limit int) bool {
	count, inField := 0, false
	for _, r := range s {
		if unicode.IsSpace(r) {
			inField = false
			continue
		}
		if !inField {
			count++
			if count > limit {
				return true
			}
			inField = true
		}
	}
	return false
}

// builtinTrim trims whitespace, or the given cutset of characters.
func builtinTrim(args []value.Value) (value.Value, error) {
	if len(args) < 1 || len(args) > 2 {
		return value.MakeNil(), fmt.Errorf("trim expects 1 or 2 arguments (s, cutset?), got %d", len(args))
	}
	if args[0].Tag() != value.Str {
		return value.MakeNil(), typeErrf("trim expects a string, got %s", args[0].TypeName())
	}
	if len(args) == 2 {
		if args[1].Tag() != value.Str {
			return value.MakeNil(), typeErrf("trim cutset must be a string, got %s", args[1].TypeName())
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
	if int64(len(s)) > maxStringBytes {
		return value.MakeErr(fmt.Sprintf("upper: input exceeds the %d-byte string limit", maxStringBytes), 1), nil
	}
	out := strings.ToUpper(s)
	if int64(len(out)) > maxStringBytes {
		return value.MakeErr(fmt.Sprintf("upper: result exceeds the %d-byte string limit", maxStringBytes), 1), nil
	}
	return value.MakeStr(out), nil
}

func builtinLower(args []value.Value) (value.Value, error) {
	s, err := oneString("lower", args)
	if err != nil {
		return value.MakeNil(), err
	}
	if int64(len(s)) > maxStringBytes {
		return value.MakeErr(fmt.Sprintf("lower: input exceeds the %d-byte string limit", maxStringBytes), 1), nil
	}
	out := strings.ToLower(s)
	if int64(len(out)) > maxStringBytes {
		return value.MakeErr(fmt.Sprintf("lower: result exceeds the %d-byte string limit", maxStringBytes), 1), nil
	}
	return value.MakeStr(out), nil
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
		return value.MakeNil(), typeErrf("format expects a string, got %s", args[0].TypeName())
	}
	f := args[0].AsStr()
	rest := args[1:]
	ai, holes := 0, 0
	b := newLimitedStringBuilder(maxStringBytes)
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
			s, ok := displayWithin(arg, maxStringBytes-int64(b.Len()))
			if !ok {
				return value.MakeErr(fmt.Sprintf("format: result exceeds the %d-byte string limit", maxStringBytes), 1), nil
			}
			b.WriteString(s)
			continue
		}
		s, err := formatArg(inner[1:], arg)
		if err != nil {
			return value.MakeErr("format: "+err.Error(), 1), nil
		}
		b.WriteString(s)
	}
	if err := b.Err(); err != nil {
		return value.MakeErr("format: "+err.Error(), 1), nil
	}
	// Strict arity: one placeholder per argument and vice versa. Catches the common
	// printf habit (format("%s", x) has no placeholders) and over/under-supply, as a
	// catchable Err rather than silently dropping or emitting a literal brace run.
	if holes != len(rest) {
		msg := fmt.Sprintf("format: template has %d placeholder(s) but got %d argument(s)", holes, len(rest))
		if looksLikePrintf(f) {
			msg += ". format uses {} / {:spec} placeholders, not %-style verbs (example: format(\"{} {:.2f}\", name, x))"
		}
		return value.MakeErr(msg, 1), nil
	}
	return value.MakeStr(b.String()), nil
}

// looksLikePrintf reports whether s carries a C/printf-style verb (%d, %s, %5.2f,
// %-10s, %x). It is the muscle-memory mistake when reaching for format() with %-style
// instead of {} placeholders, so the arity error points it out. Detection is strict —
// % then optional flags/width/precision then a KNOWN verb letter — so prose like
// "100% done" (no verb after the %) does not trip it.
func looksLikePrintf(s string) bool {
	const verbs = "bcdeEfFgGopqstTvxX"
	const flags = "#+-.0123456789"
	for i := 0; i+1 < len(s); i++ {
		if s[i] != '%' {
			continue
		}
		j := i + 1
		if s[j] == '%' { // %% is a literal percent, not a verb
			i = j
			continue
		}
		for j < len(s) && strings.IndexByte(flags, s[j]) >= 0 {
			j++
		}
		if j < len(s) && strings.IndexByte(verbs, s[j]) >= 0 {
			return true
		}
	}
	return false
}

// builtinLines splits text into lines (CRLF-normalized), dropping a single
// trailing newline so "a\nb\n" yields ["a", "b"] and "" yields [].
func builtinLines(args []value.Value) (value.Value, error) {
	s, err := oneString("lines", args)
	if err != nil {
		return value.MakeNil(), err
	}
	if int64(len(s)) > maxStringBytes {
		return value.MakeErr(fmt.Sprintf("lines: input exceeds the %d-byte string limit", maxStringBytes), 1), nil
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return value.MakeArray(nil), nil
	}
	if strings.Count(s, "\n")+1 > maxCollectionItems {
		return value.MakeErr(fmt.Sprintf("lines: result exceeds the %d-element collection limit", maxCollectionItems), 1), nil
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
		return value.MakeNil(), typeErrf("repeat expects a string, got %s", args[0].TypeName())
	}
	if args[1].Tag() != value.Int {
		// Catchable Err, like take/drop's count check — and consistent with repeat's own
		// negative/oversized-count Err paths below.
		return value.MakeErr(fmt.Sprintf("repeat count must be an int, got %s", args[1].TypeName()), 1), nil
	}
	n := args[1].AsInt()
	if n < 0 {
		return value.MakeErr("repeat: negative count", 1), nil
	}
	s := args[0].AsStr()
	// Cap the result so an oversized count yields a catchable Err instead of a
	// strings.Repeat allocation panic.
	if len(s) > 0 && n > maxStringBytes/int64(len(s)) {
		return value.MakeErr(fmt.Sprintf("repeat: result too large (%d copies of %d bytes)", n, len(s)), 1), nil
	}
	return value.MakeStr(strings.Repeat(s, int(n))), nil
}

// joinStrings implements the join builtin: join(array, sep?) renders each element
// (like say) and joins them with sep.
func joinStrings(args []value.Value) (value.Value, error) {
	if len(args) > 2 {
		return value.MakeNil(), fmt.Errorf("join(array, sep) takes at most 2 arguments, got %d", len(args))
	}
	arr := args[0].Obj().(*value.Array)
	sep := ""
	if len(args) == 2 && len(arr.Elems) > 1 {
		var ok bool
		sep, ok = displayWithin(args[1], maxStringBytes)
		if !ok {
			return value.MakeErr(fmt.Sprintf("join: result exceeds the %d-byte string limit", maxStringBytes), 1), nil
		}
	}
	b := newLimitedStringBuilder(maxStringBytes)
	for i, e := range arr.Elems {
		if i > 0 {
			if _, err := b.WriteString(sep); err != nil {
				return value.MakeErr("join: "+err.Error(), 1), nil
			}
		}
		s, ok := displayWithin(e, maxStringBytes-int64(b.Len()))
		if !ok {
			return value.MakeErr(fmt.Sprintf("join: result exceeds the %d-byte string limit", maxStringBytes), 1), nil
		}
		if _, err := b.WriteString(s); err != nil {
			return value.MakeErr("join: "+err.Error(), 1), nil
		}
	}
	return value.MakeStr(b.String()), nil
}
