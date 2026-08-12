package eval

import (
	"container/list"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

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

// The process-wide cache contains immutable compiled regexes, but is bounded by
// both entry count and source bytes. LRU eviction prevents an early one-off flood
// from pinning the cache forever. Invalid patterns are never retained.
const (
	maxRegexPatternBytes          = 1 << 20
	maxReCache                    = 4096
	maxReCacheBytes               = 16 << 20
	regexEntryOverhead            = 256
	maxRegexDiagnosticBytes int64 = 4 << 10
)

type reCacheEntry struct {
	pat  string
	re   *regexp.Regexp
	cost int
}

var reCache = struct {
	sync.Mutex
	items map[string]*list.Element
	lru   list.List // front is most recently used; values are *reCacheEntry
	bytes int
}{items: make(map[string]*list.Element)}

func compilePattern(pat string) (*regexp.Regexp, error) {
	if len(pat) > maxRegexPatternBytes {
		return nil, fmt.Errorf("pattern exceeds the %d-byte limit", maxRegexPatternBytes)
	}
	reCache.Lock()
	if el := reCache.items[pat]; el != nil {
		reCache.lru.MoveToFront(el)
		re := el.Value.(*reCacheEntry).re
		reCache.Unlock()
		return re, nil
	}
	reCache.Unlock()

	re, err := regexp.Compile(pat)
	if err != nil {
		return nil, err
	}

	cost := len(pat) + regexEntryOverhead
	reCache.Lock()
	defer reCache.Unlock()
	if el := reCache.items[pat]; el != nil { // another goroutine won the compile race
		reCache.lru.MoveToFront(el)
		return el.Value.(*reCacheEntry).re, nil
	}
	for len(reCache.items) >= maxReCache || reCache.bytes+cost > maxReCacheBytes {
		old := reCache.lru.Back()
		if old == nil {
			break
		}
		e := old.Value.(*reCacheEntry)
		delete(reCache.items, e.pat)
		reCache.bytes -= e.cost
		reCache.lru.Remove(old)
	}
	e := &reCacheEntry{pat: pat, re: re, cost: cost}
	reCache.items[pat] = reCache.lru.PushFront(e)
	reCache.bytes += cost
	return re, nil
}

// makeRegex compiles pat into a regex Value, or a catchable Err value on failure
// (a bad pattern is data-level error, consistent with drang's first-class errors).
func makeRegex(pat string) value.Value {
	re, err := compilePattern(pat)
	if err != nil {
		return value.MakeErr(fmt.Sprintf("bad regex %s: %v", boundedRegexPatternQuote(pat, maxRegexDiagnosticBytes), err), 1)
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
			return nil, value.MakeErr(fmt.Sprintf("%s: bad pattern %s: %v", name, boundedRegexPatternQuote(v.AsStr(), maxRegexDiagnosticBytes), err), 1), false
		}
		return c, value.MakeNil(), true
	}
	return nil, value.MakeErr(fmt.Sprintf("%s: pattern must be a string or regex, got %s", name, v.TypeName()), 1), false
}

// boundedRegexPatternQuote is an exact strconv.Quote for ordinary diagnostics,
// but stops after limit bytes and adds a truncation marker. It quotes one decoded
// rune at a time, so a long control-byte pattern cannot first allocate a 4x-sized
// escaped copy before the diagnostic is truncated.
func boundedRegexPatternQuote(pat string, limit int64) string {
	b := newLimitedStringBuilder(limit)
	if err := b.WriteByte('"'); err != nil {
		return truncatedDiagnostic(b.String(), limit)
	}
	for len(pat) > 0 {
		_, width := utf8.DecodeRuneInString(pat)
		if width <= 0 {
			width = 1
		}
		q := strconv.Quote(pat[:width])
		if _, err := b.WriteString(q[1 : len(q)-1]); err != nil {
			return truncatedDiagnostic(b.String(), limit)
		}
		pat = pat[width:]
	}
	if err := b.WriteByte('"'); err != nil {
		return truncatedDiagnostic(b.String(), limit)
	}
	return b.String()
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
	if int64(len(args[0].AsStr())) > maxStringBytes {
		return "", nil, value.MakeErr(fmt.Sprintf("%s: input exceeds the %d-byte string limit", name, maxStringBytes), 1), nil
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
	all := re.FindAllString(s, maxCollectionItems+1)
	if len(all) > maxCollectionItems {
		return value.MakeErr(fmt.Sprintf("match_all: result exceeds the %d-element collection limit", maxCollectionItems), 1), nil
	}
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
		if !literalReplacementFits(s, args[1].AsStr(), repl, true) {
			return value.MakeErr(fmt.Sprintf("replace_all: result exceeds the %d-byte string limit", maxStringBytes), 1), nil
		}
		return value.MakeStr(strings.ReplaceAll(s, args[1].AsStr(), repl)), nil
	}
	out, rerr := regexReplaceLimited(re, s, repl, true)
	if rerr != nil {
		code := int64(1)
		if _, ok := rerr.(regexWorkLimitError); ok {
			code = 137
		}
		return value.MakeErr("replace_all: "+rerr.Error(), code), nil
	}
	return value.MakeStr(out), nil
}

// builtinReplaceFirst replaces only the FIRST occurrence of needle in s.
func builtinReplaceFirst(args []value.Value) (value.Value, error) {
	s, repl, re, err := replaceArgs("replace_first", args)
	if err != nil {
		return value.MakeNil(), err
	}
	if re == nil {
		if !literalReplacementFits(s, args[1].AsStr(), repl, false) {
			return value.MakeErr(fmt.Sprintf("replace_first: result exceeds the %d-byte string limit", maxStringBytes), 1), nil
		}
		return value.MakeStr(strings.Replace(s, args[1].AsStr(), repl, 1)), nil
	}
	out, rerr := regexReplaceLimited(re, s, repl, false)
	if rerr != nil {
		code := int64(1)
		if _, ok := rerr.(regexWorkLimitError); ok {
			code = 137
		}
		return value.MakeErr("replace_first: "+rerr.Error(), code), nil
	}
	return value.MakeStr(out), nil
}

func literalReplacementFits(s, old, repl string, all bool) bool {
	count := strings.Count(s, old)
	if !all && count > 1 {
		count = 1
	}
	total := int64(len(s))
	delta := int64(len(repl)) - int64(len(old))
	if count == 0 {
		return total <= maxStringBytes
	}
	if delta > 0 {
		if total > maxStringBytes || int64(count) > (maxStringBytes-total)/delta {
			return false
		}
		total += int64(count) * delta
	} else if delta < 0 {
		total -= int64(count) * -delta
	}
	return total <= maxStringBytes
}

type regexWorkLimitError struct{}

func (regexWorkLimitError) Error() string {
	return fmt.Sprintf("work exceeds the %d-item collection limit", maxCollectionItems)
}

type regexExpansionPart struct {
	literal    string
	group      int
	candidates []int
	capture    bool
}

func regexReplaceLimited(re *regexp.Regexp, s, repl string, all bool) (string, error) {
	// Avoid parsing or indexing a replacement that will never be used. This
	// lightweight precheck does not materialize submatch vectors.
	if re.FindStringIndex(s) == nil {
		if int64(len(s)) > maxStringBytes {
			return "", fmt.Errorf("result exceeds the %d-byte string limit", maxStringBytes)
		}
		return s, nil
	}

	groups := 1 + re.NumSubexp()
	if groups > maxCollectionItems {
		return "", regexWorkLimitError{}
	}
	plan, expansionWork, parseWork, err := compileRegexExpansion(re, repl, maxCollectionItems)
	if err != nil {
		return "", err
	}
	if parseWork > maxCollectionItems {
		return "", regexWorkLimitError{}
	}
	perMatch := groups
	if expansionWork > maxCollectionItems-perMatch {
		return "", regexWorkLimitError{}
	}
	perMatch += expansionWork
	allowed := (maxCollectionItems - parseWork) / perMatch
	if allowed < 1 {
		return "", regexWorkLimitError{}
	}

	var matches [][]int
	if all {
		matches = re.FindAllStringSubmatchIndex(s, allowed+1)
		if len(matches) > allowed {
			return "", regexWorkLimitError{}
		}
	} else {
		matches = [][]int{re.FindStringSubmatchIndex(s)}
	}
	b := newLimitedStringBuilder(maxStringBytes)
	last := 0
	for _, match := range matches {
		if _, err := b.WriteString(s[last:match[0]]); err != nil {
			return "", err
		}
		if err := appendRegexExpansion(b, plan, s, match); err != nil {
			return "", err
		}
		last = match[1]
	}
	if _, err := b.WriteString(s[last:]); err != nil {
		return "", err
	}
	return b.String(), nil
}

// compileRegexExpansion parses the replacement once. Named captures are indexed
// once as well; duplicate names retain their source order so each match still
// selects the first participating capture, matching regexp expansion semantics.
// parseWork and expansionWork are conservative operation counts used to reject
// match×template or match×duplicate-name explosions before expansion begins.
func compileRegexExpansion(re *regexp.Regexp, template string, limit int) ([]regexExpansionPart, int, int, error) {
	names := re.SubexpNames()
	byName := make(map[string][]int)
	for i, name := range names {
		if name != "" {
			byName[name] = append(byName[name], i)
		}
	}
	parts := make([]regexExpansionPart, 0)
	expansionWork := 0
	add := func(part regexExpansionPart, work int) error {
		if len(parts) >= limit || work < 0 || work > limit-expansionWork {
			return regexWorkLimitError{}
		}
		parts = append(parts, part)
		expansionWork += work
		return nil
	}
	for len(template) > 0 {
		before, after, ok := strings.Cut(template, "$")
		if !ok {
			if template != "" {
				if err := add(regexExpansionPart{literal: template}, 1); err != nil {
					return nil, 0, 0, err
				}
			}
			break
		}
		if before != "" {
			if err := add(regexExpansionPart{literal: before}, 1); err != nil {
				return nil, 0, 0, err
			}
		}
		template = after
		if template != "" && template[0] == '$' {
			if err := add(regexExpansionPart{literal: "$"}, 1); err != nil {
				return nil, 0, 0, err
			}
			template = template[1:]
			continue
		}
		name, num, rest, ok := extractRegexTemplate(template)
		if !ok {
			if err := add(regexExpansionPart{literal: "$"}, 1); err != nil {
				return nil, 0, 0, err
			}
			continue
		}
		template = rest
		part := regexExpansionPart{capture: true, group: num}
		work := 1
		if num < 0 {
			part.candidates = byName[name]
			if len(part.candidates) > limit-work {
				return nil, 0, 0, regexWorkLimitError{}
			}
			work += len(part.candidates)
		}
		if err := add(part, work); err != nil {
			return nil, 0, 0, err
		}
	}
	return parts, expansionWork, len(parts), nil
}

func appendRegexExpansion(b *limitedStringBuilder, plan []regexExpansionPart, src string, match []int) error {
	for _, part := range plan {
		if !part.capture {
			if _, err := b.WriteString(part.literal); err != nil {
				return err
			}
			continue
		}
		group := part.group
		if group < 0 {
			for _, candidate := range part.candidates {
				if 2*candidate+1 < len(match) && match[2*candidate] >= 0 {
					group = candidate
					break
				}
			}
		}
		if group >= 0 && 2*group+1 < len(match) && match[2*group] >= 0 {
			if _, err := b.WriteString(src[match[2*group]:match[2*group+1]]); err != nil {
				return err
			}
		}
	}
	return nil
}

// extractRegexTemplate mirrors regexp's replacement-name grammar.
func extractRegexTemplate(s string) (name string, num int, rest string, ok bool) {
	if s == "" {
		return "", 0, "", false
	}
	brace := false
	if s[0] == '{' {
		brace = true
		s = s[1:]
	}
	i := 0
	for i < len(s) {
		r, width := utf8.DecodeRuneInString(s[i:])
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			break
		}
		i += width
	}
	if i == 0 {
		return "", 0, "", false
	}
	name = s[:i]
	if brace {
		if i >= len(s) || s[i] != '}' {
			return "", 0, "", false
		}
		i++
	}
	num = 0
	for j := 0; j < len(name); j++ {
		if name[j] < '0' || name[j] > '9' || num >= 1e8 {
			num = -1
			break
		}
		num = num*10 + int(name[j]-'0')
	}
	if name[0] == '0' && len(name) > 1 {
		num = -1
	}
	return name, num, s[i:], true
}
