package eval

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/anafalanx/drang/internal/value"
)

// regexObj is a compiled, first-class regex value. It is immutable: the wrapped
// *regexp.Regexp is safe for concurrent use, so the value is shared (never deep
// copied) and carries no per-match state — unlike a stateful JS RegExp.
type regexObj struct {
	re  *regexp.Regexp
	src string // pattern source (Go inline flags already baked in), for Display/Equal
}

func (r *regexObj) TypeName() string { return "regex" }
func (r *regexObj) Len() int         { return 0 }

// Display renders as a qr// literal, choosing a delimiter the pattern does not
// contain so the output round-trips (re-lexing yields an equal regex). Flags show
// in their baked inline form, e.g. qr/foo/i displays as qr/(?i)foo/.
func (r *regexObj) Display() string {
	for _, d := range "/|#!~" {
		if !strings.ContainsRune(r.src, d) {
			return "qr" + string(d) + r.src + string(d)
		}
	}
	return "qr/" + strings.ReplaceAll(r.src, "/", `\/`) + "/" // pattern uses every delimiter (rare)
}

func (r *regexObj) Equal(o value.Obj) bool {
	other, ok := o.(*regexObj)
	return ok && r.src == other.src
}

// DeepCopy shares the value: a compiled regex is immutable and concurrency-safe,
// so copy-on-send (pmap) can hand the same object to every worker.
func (r *regexObj) DeepCopy(map[value.Obj]value.Obj) value.Obj { return r }

// reCache memoizes pattern compilation. Compiled regexes are immutable and safe to
// share, so the same *regexp.Regexp is reused across calls and goroutines — this is
// the "compile once" win for both qr// literals and string-pattern builtins.
var reCache sync.Map // pattern string -> *reCacheEntry

type reCacheEntry struct {
	re  *regexp.Regexp
	err error
}

func compilePattern(pat string) (*regexp.Regexp, error) {
	if v, ok := reCache.Load(pat); ok {
		e := v.(*reCacheEntry)
		return e.re, e.err
	}
	re, err := regexp.Compile(pat)
	reCache.Store(pat, &reCacheEntry{re: re, err: err})
	return re, err
}

// makeRegex compiles pat into a regex Value, or a catchable Err value on failure
// (a bad pattern is data-level error, consistent with drang's first-class errors).
func makeRegex(pat string) value.Value {
	re, err := compilePattern(pat)
	if err != nil {
		return value.MakeErr(fmt.Sprintf("bad regex %q: %v", pat, err), 1)
	}
	return value.MakeObj(value.Regex, &regexObj{re: re, src: pat})
}

// regexArg resolves a pattern argument that may be a string (compiled, cached) or
// an already-compiled regex value. ok=false returns a catchable Err in errv.
func regexArg(name string, v value.Value) (re *regexp.Regexp, errv value.Value, ok bool) {
	switch v.Tag() {
	case value.Regex:
		return v.Obj().(*regexObj).re, value.MakeNil(), true
	case value.Str:
		c, err := compilePattern(v.AsStr())
		if err != nil {
			return nil, value.MakeErr(fmt.Sprintf("%s: bad pattern %q: %v", name, v.AsStr(), err), 1), false
		}
		return c, value.MakeNil(), true
	}
	return nil, value.MakeErr(fmt.Sprintf("%s: pattern must be a string or regex, got %s", name, v.TypeName()), 1), false
}

// reArgs validates the common (subject string, pattern) shape: wrong arity aborts
// (Go error); a non-string subject or bad pattern is a catchable Err in errv.
func reArgs(name string, args []value.Value) (s string, re *regexp.Regexp, errv value.Value, abort error) {
	if len(args) != 2 {
		return "", nil, value.MakeNil(), fmt.Errorf("%s expects 2 arguments (s, pattern), got %d", name, len(args))
	}
	if args[0].Tag() != value.Str {
		return "", nil, value.MakeErr(fmt.Sprintf("%s: first argument must be a string, got %s", name, args[0].TypeName()), 1), nil
	}
	re, ev, ok := regexArg(name, args[1])
	if !ok {
		return "", nil, ev, nil
	}
	return args[0].AsStr(), re, value.MakeNil(), nil
}

// builtinRe compiles a string pattern into a reusable regex value: re(pattern).
// An already-compiled regex passes through. Used for dynamic (interpolated)
// patterns; qr/.../ is the literal form.
func builtinRe(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("re expects 1 argument (pattern), got %d", len(args))
	}
	switch args[0].Tag() {
	case value.Regex:
		return args[0], nil
	case value.Str:
		return makeRegex(args[0].AsStr()), nil
	}
	return value.MakeErr(fmt.Sprintf("re: pattern must be a string, got %s", args[0].TypeName()), 1), nil
}

// builtinMatches reports whether the pattern matches anywhere in s.
func builtinMatches(args []value.Value) (value.Value, error) {
	s, re, errv, abort := reArgs("matches", args)
	if abort != nil {
		return value.MakeNil(), abort
	}
	if re == nil {
		return errv, nil
	}
	return value.MakeBool(re.MatchString(s)), nil
}

// builtinMatch returns the first match as [full, group1, group2, ...], or nil if
// there is no match.
func builtinMatch(args []value.Value) (value.Value, error) {
	s, re, errv, abort := reArgs("match", args)
	if abort != nil {
		return value.MakeNil(), abort
	}
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

// builtinMatchAll returns every (full) match of the pattern in s, in order. It is
// the exhaustive sibling of match (which returns the first match with its groups);
// the shared "match" stem keeps matches/match/match_all one coherent family.
func builtinMatchAll(args []value.Value) (value.Value, error) {
	s, re, errv, abort := reArgs("match_all", args)
	if abort != nil {
		return value.MakeNil(), abort
	}
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

// replaceArgs validates the shared (s, needle, repl) shape of replace_first/replace_all.
// The needle picks the mode: a plain string is a LITERAL needle (repl is literal too),
// a qr// regex value matches as a pattern (repl may use $1 / ${name} backreferences,
// Go's ReplaceAllString syntax). This bare-string-means-literal dispatch matches Ruby's
// String#gsub and is why these builtins never compile a string as a pattern — write
// re(pat) or qr// when a pattern is meant.
func replaceArgs(name string, args []value.Value) (s, repl string, re *regexp.Regexp, err error) {
	if len(args) != 3 {
		return "", "", nil, fmt.Errorf("%s expects 3 arguments (s, needle, repl), got %d", name, len(args))
	}
	if args[0].Tag() != value.Str {
		return "", "", nil, typeErrf("%s: first argument must be a string, got %s", name, args[0].TypeName())
	}
	if args[2].Tag() != value.Str {
		return "", "", nil, typeErrf("%s: replacement must be a string, got %s", name, args[2].TypeName())
	}
	switch args[1].Tag() {
	case value.Str:
		return args[0].AsStr(), args[2].AsStr(), nil, nil // literal needle
	case value.Regex:
		return args[0].AsStr(), args[2].AsStr(), args[1].Obj().(*regexObj).re, nil
	default:
		return "", "", nil, typeErrf("%s: needle must be a string (literal) or a regex, got %s", name, args[1].TypeName())
	}
}

// builtinReplaceAll replaces EVERY occurrence of needle in s (the _all marks
// exhaustiveness, mirroring match/match_all; replace_first is the single-shot form).
func builtinReplaceAll(args []value.Value) (value.Value, error) {
	s, repl, re, err := replaceArgs("replace_all", args)
	if err != nil {
		return value.MakeNil(), err
	}
	if re == nil {
		return value.MakeStr(strings.ReplaceAll(s, args[1].AsStr(), repl)), nil
	}
	return value.MakeStr(re.ReplaceAllString(s, repl)), nil
}

// builtinReplaceFirst replaces only the FIRST occurrence of needle in s.
func builtinReplaceFirst(args []value.Value) (value.Value, error) {
	s, repl, re, err := replaceArgs("replace_first", args)
	if err != nil {
		return value.MakeNil(), err
	}
	if re == nil {
		return value.MakeStr(strings.Replace(s, args[1].AsStr(), repl, 1)), nil
	}
	loc := re.FindStringSubmatchIndex(s)
	if loc == nil {
		return value.MakeStr(s), nil
	}
	out := make([]byte, 0, len(s))
	out = append(out, s[:loc[0]]...)
	out = re.ExpandString(out, repl, s, loc) // $1 / ${name} backreferences
	out = append(out, s[loc[1]:]...)
	return value.MakeStr(string(out)), nil
}
