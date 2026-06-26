package eval

import (
	"fmt"
	"regexp"

	"github.com/anafalanx/lang3/internal/value"
)

// compileRe compiles a pattern (Go's RE2 syntax). On failure it returns a nil
// regexp and a catchable Err value for the caller to hand back to the script.
func compileRe(name, pat string) (*regexp.Regexp, value.Value) {
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil, value.MakeErr(fmt.Sprintf("%s: bad pattern %q: %v", name, pat, err), 1)
	}
	return re, value.MakeNil()
}

// builtinMatches reports whether re matches anywhere in s.
func builtinMatches(args []value.Value) (value.Value, error) {
	s, p, err := twoStrings("matches", args)
	if err != nil {
		return value.MakeNil(), err
	}
	re, errv := compileRe("matches", p)
	if re == nil {
		return errv, nil
	}
	return value.MakeBool(re.MatchString(s)), nil
}

// builtinMatch returns the first match as [full, group1, group2, ...], or nil if
// there is no match.
func builtinMatch(args []value.Value) (value.Value, error) {
	s, p, err := twoStrings("match", args)
	if err != nil {
		return value.MakeNil(), err
	}
	re, errv := compileRe("match", p)
	if re == nil {
		return errv, nil
	}
	m := re.FindStringSubmatch(s)
	if m == nil {
		return value.MakeNil(), nil
	}
	out := make([]value.Value, len(m))
	for i, g := range m {
		out[i] = value.MakeStr(g)
	}
	return value.MakeArray(out), nil
}

// builtinFindAll returns every (full) match of re in s, in order.
func builtinFindAll(args []value.Value) (value.Value, error) {
	s, p, err := twoStrings("find_all", args)
	if err != nil {
		return value.MakeNil(), err
	}
	re, errv := compileRe("find_all", p)
	if re == nil {
		return errv, nil
	}
	all := re.FindAllString(s, -1)
	out := make([]value.Value, len(all))
	for i, g := range all {
		out[i] = value.MakeStr(g)
	}
	return value.MakeArray(out), nil
}

// builtinGsub replaces every match of re in s with repl, where repl may use
// $1 / ${name} backreferences (Go's ReplaceAllString syntax).
func builtinGsub(args []value.Value) (value.Value, error) {
	if len(args) != 3 {
		return value.MakeNil(), fmt.Errorf("gsub expects 3 arguments (s, re, repl), got %d", len(args))
	}
	for i, a := range args {
		if a.Tag() != value.Str {
			return value.MakeNil(), fmt.Errorf("gsub: argument %d must be a string, got %s", i+1, a.TypeName())
		}
	}
	re, errv := compileRe("gsub", args[1].AsStr())
	if re == nil {
		return errv, nil
	}
	return value.MakeStr(re.ReplaceAllString(args[0].AsStr(), args[2].AsStr())), nil
}
