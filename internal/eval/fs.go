package eval

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"unsafe"

	"github.com/anafalanx/drang/internal/value"
)

// oneString validates a single string argument. Wrong arity is a program abort (a Go error);
// a wrong TYPE is a catchable Err (typeErr, converted by safeBuiltin) — the stdlib convention.
func oneString(name string, args []value.Value) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("%s expects 1 argument, got %d", name, len(args))
	}
	if args[0].Tag() != value.Str {
		return "", typeErrf("%s expects a string, got %s", name, args[0].TypeName())
	}
	return args[0].AsStr(), nil
}

func twoStrings(name string, args []value.Value) (string, string, error) {
	if len(args) != 2 {
		return "", "", fmt.Errorf("%s expects 2 arguments, got %d", name, len(args))
	}
	for i, a := range args {
		if a.Tag() != value.Str {
			return "", "", typeErrf("%s: argument %d must be a string, got %s", name, i+1, a.TypeName())
		}
	}
	return args[0].AsStr(), args[1].AsStr(), nil
}

// filesystemEntryBudget bounds directory enumeration before the Go filepath
// helpers can materialize an arbitrarily wide directory. filepath.Glob and
// filepath.WalkDir both read and sort a whole directory internally; using
// File.ReadDir in bounded batches lets drang reject the next entry at the
// collection ceiling while retaining at most limit+1 DirEntry values.
type filesystemEntryBudget struct {
	limit    int
	used     int
	limitErr error
}

func newFilesystemEntryBudget(limit int, limitErr error) *filesystemEntryBudget {
	if limit < 0 {
		limit = 0
	}
	return &filesystemEntryBudget{limit: limit, limitErr: limitErr}
}

// readDirBounded reads one directory in bounded batches and returns its entries
// in the same lexical name order as os.ReadDir. The budget is shared across a
// complete glob/walk/copy operation, so neither a single wide directory nor a
// broad tree can move an unbounded amount of directory metadata into memory.
func readDirBounded(name string, budget *filesystemEntryBudget) ([]fs.DirEntry, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	remaining := budget.limit - budget.used
	capacity := remaining
	if capacity > 256 {
		capacity = 256
	}
	if capacity < 0 {
		capacity = 0
	}
	entries := make([]fs.DirEntry, 0, capacity)
	for {
		remaining = budget.limit - budget.used
		want := 256
		if remaining < want {
			want = remaining + 1 // one extra entry is the bounded overflow signal
		}
		if want < 1 {
			want = 1
		}
		batch, readErr := f.ReadDir(want)
		if len(batch) > remaining {
			return nil, budget.limitErr
		}
		budget.used += len(batch)
		entries = append(entries, batch...)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
			return entries, readErr
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

// walkDirBounded is filepath.WalkDir with bounded directory reads. It keeps the
// public WalkDir callback/SkipDir behavior and lexical depth-first order, but
// treats a budget breach as an operation error rather than offering it to the
// callback (whose normal read-error policy might otherwise skip it).
func walkDirBounded(root string, budget *filesystemEntryBudget, fn fs.WalkDirFunc) error {
	d, err := os.Lstat(root)
	if err != nil {
		err = fn(root, nil, err)
	} else {
		err = walkDirNodeBounded(root, fs.FileInfoToDirEntry(d), budget, fn)
	}
	if err == filepath.SkipDir || err == filepath.SkipAll {
		return nil
	}
	return err
}

func walkDirNodeBounded(name string, d fs.DirEntry, budget *filesystemEntryBudget, fn fs.WalkDirFunc) error {
	if err := fn(name, d, nil); err != nil || !d.IsDir() {
		if err == filepath.SkipDir && d.IsDir() {
			return nil
		}
		return err
	}

	entries, err := readDirBounded(name, budget)
	if err != nil {
		if err == budget.limitErr {
			return err
		}
		// Match WalkDir: report an unreadable directory to the callback a
		// second time and let it choose whether to stop or skip the subtree.
		if callbackErr := fn(name, d, err); callbackErr != nil {
			if callbackErr == filepath.SkipDir {
				return nil
			}
			return callbackErr
		}
	}

	for _, child := range entries {
		childName := filepath.Join(name, child.Name())
		if err := walkDirNodeBounded(childName, child, budget, fn); err != nil {
			if err == filepath.SkipDir {
				break
			}
			return err
		}
	}
	return nil
}

// --- path helpers: pure string transforms (never touch the disk); a non-string arg is a catchable Err ---

// builtinJoin renders an array's elements and joins them with a separator:
// join(array, sep?) → string. This is the universal meaning of join that a
// Perl/Python/Ruby user reaches for. It is array-only by design; to assemble
// filesystem path segments use path_join (the type-dispatched polymorphism that
// used to fold both meanings into join was retired pre-1.0).
func builtinJoin(args []value.Value) (value.Value, error) {
	if len(args) == 0 || args[0].Tag() != value.Arr {
		got := "no arguments"
		if len(args) > 0 {
			got = args[0].TypeName()
		}
		return value.MakeNil(), typeErrf("join: first argument must be an array, got %s (to join path segments use path_join)", got)
	}
	return joinStrings(args)
}

// builtinPathJoin joins its string arguments into one OS-native path:
// path_join(seg, ...) → string, e.g. path_join($root, "out.txt"). A non-string
// argument is a catchable Err. (join renders+joins an array; path_join is the path
// sibling — the two were one polymorphic builtin before the pre-1.0 split.)
func builtinPathJoin(args []value.Value) (value.Value, error) {
	parts := make([]string, len(args))
	total := int64(0)
	havePart := false
	for i, a := range args {
		if a.Tag() != value.Str {
			return value.MakeNil(), typeErrf("path_join: argument %d must be a string, got %s", i+1, a.TypeName())
		}
		parts[i] = a.AsStr()
		if parts[i] == "" {
			continue // filepath.Join ignores empty elements
		}
		if havePart {
			if total >= maxStringBytes {
				return value.MakeErr(fmt.Sprintf("path_join: result exceeds the %d-byte string limit", maxStringBytes), 1), nil
			}
			total++ // worst-case native separator inserted between adjacent parts
		}
		if !sizeFits(total, len(parts[i])) {
			return value.MakeErr(fmt.Sprintf("path_join: result exceeds the %d-byte string limit", maxStringBytes), 1), nil
		}
		total += int64(len(parts[i]))
		havePart = true
	}
	out := filepath.Join(parts...)
	if int64(len(out)) > maxStringBytes {
		return value.MakeErr(fmt.Sprintf("path_join: result exceeds the %d-byte string limit", maxStringBytes), 1), nil
	}
	return value.MakeStr(out), nil
}

func builtinDirname(args []value.Value) (value.Value, error) {
	p, err := oneString("dirname", args)
	if err != nil {
		return value.MakeNil(), err
	}
	return value.MakeStr(filepath.Dir(p)), nil
}

func builtinBasename(args []value.Value) (value.Value, error) {
	p, err := oneString("basename", args)
	if err != nil {
		return value.MakeNil(), err
	}
	return value.MakeStr(filepath.Base(p)), nil
}

func builtinExt(args []value.Value) (value.Value, error) {
	p, err := oneString("ext", args)
	if err != nil {
		return value.MakeNil(), err
	}
	return value.MakeStr(filepath.Ext(p)), nil
}

func builtinStem(args []value.Value) (value.Value, error) {
	p, err := oneString("stem", args)
	if err != nil {
		return value.MakeNil(), err
	}
	b := filepath.Base(p)
	return value.MakeStr(strings.TrimSuffix(b, filepath.Ext(b))), nil
}

// builtinAbsPath resolves a path to absolute against the CWD. (Numeric absolute
// value is the abs builtin; this was renamed from abs to free that name. Spelled
// abs_path — composed underscore form, like its predicate sibling is_abs and
// Perl's Cwd::abs_path — not Python's glued abspath.)
func builtinAbsPath(args []value.Value) (value.Value, error) {
	p, err := oneString("abs_path", args)
	if err != nil {
		return value.MakeNil(), err
	}
	a, e := filepath.Abs(p)
	if e != nil {
		return value.MakeErr("abs_path "+p+": "+e.Error(), 1), nil
	}
	if int64(len(a)) > maxStringBytes {
		return value.MakeErr(fmt.Sprintf("abs_path: result exceeds the %d-byte string limit", maxStringBytes), 1), nil
	}
	return value.MakeStr(a), nil
}

// builtinToSlash converts a path's backslashes to forward slashes (filepath.ToSlash) —
// named for the conversion, joining the to_json/to_hex family.
func builtinToSlash(args []value.Value) (value.Value, error) {
	p, err := oneString("to_slash", args)
	if err != nil {
		return value.MakeNil(), err
	}
	return value.MakeStr(filepath.ToSlash(p)), nil
}

func builtinIsAbs(args []value.Value) (value.Value, error) {
	p, err := oneString("is_abs", args)
	if err != nil {
		return value.MakeNil(), err
	}
	return value.MakeBool(filepath.IsAbs(p)), nil
}

func builtinClean(args []value.Value) (value.Value, error) {
	p, err := oneString("clean", args)
	if err != nil {
		return value.MakeNil(), err
	}
	return value.MakeStr(filepath.Clean(p)), nil
}

// builtinRel returns target relative to base. Uncomparable paths (e.g. different
// Windows volumes) are a catchable Err.
func builtinRel(args []value.Value) (value.Value, error) {
	base, target, err := twoStrings("rel", args)
	if err != nil {
		return value.MakeNil(), err
	}
	r, e := filepath.Rel(base, target)
	if e != nil {
		return value.MakeErr("rel "+base+" -> "+target+": "+e.Error(), 1), nil
	}
	if int64(len(r)) > maxStringBytes {
		return value.MakeErr(fmt.Sprintf("rel: result exceeds the %d-byte string limit", maxStringBytes), 1), nil
	}
	return value.MakeStr(r), nil
}

// builtinIsWithin reports whether target is inside base (or equal to it). The is_
// prefix marks it a guard (always a bool, never a fallible op): uncomparable paths
// or any "../"-escaping relative path are simply not within.
func builtinIsWithin(args []value.Value) (value.Value, error) {
	base, target, err := twoStrings("is_within", args)
	if err != nil {
		return value.MakeNil(), err
	}
	r, e := filepath.Rel(base, target)
	if e != nil {
		return value.MakeBool(false), nil
	}
	inside := r == "." || (r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)))
	return value.MakeBool(inside), nil
}

// builtinPathListSep returns the OS PATH-list separator (";" on Windows, ":" on
// Unix), for splitting/joining $ENV["PATH"]-style lists.
func builtinPathListSep(args []value.Value) (value.Value, error) {
	if len(args) != 0 {
		return value.MakeNil(), fmt.Errorf("path_list_sep expects no arguments, got %d", len(args))
	}
	return value.MakeStr(string(os.PathListSeparator)), nil
}

// --- stat guards: always a bool, never an Err, so they drop into if/unless ---

func builtinExists(args []value.Value) (value.Value, error) {
	p, err := oneString("exists", args)
	if err != nil {
		return value.MakeNil(), err
	}
	_, statErr := os.Stat(p)
	return value.MakeBool(statErr == nil), nil
}

func builtinIsDir(args []value.Value) (value.Value, error) {
	p, err := oneString("is_dir", args)
	if err != nil {
		return value.MakeNil(), err
	}
	fi, statErr := os.Stat(p)
	return value.MakeBool(statErr == nil && fi.IsDir()), nil
}

func builtinIsFile(args []value.Value) (value.Value, error) {
	p, err := oneString("is_file", args)
	if err != nil {
		return value.MakeNil(), err
	}
	fi, statErr := os.Stat(p)
	return value.MakeBool(statErr == nil && fi.Mode().IsRegular()), nil
}

// builtinIsSymlink uses Lstat, so it reports on the link itself rather than its target
// (unlike exists/is_dir/is_file, which follow symlinks via Stat).
func builtinIsSymlink(args []value.Value) (value.Value, error) {
	p, err := oneString("is_symlink", args)
	if err != nil {
		return value.MakeNil(), err
	}
	fi, lerr := os.Lstat(p)
	return value.MakeBool(lerr == nil && fi.Mode()&os.ModeSymlink != 0), nil
}

// --- fallible filesystem ops: catchable Err (code 1) on real failure ---

func builtinMkdir(args []value.Value) (value.Value, error) {
	p, err := oneString("mkdir", args)
	if err != nil {
		return value.MakeNil(), err
	}
	if e := os.MkdirAll(p, 0o755); e != nil {
		return value.MakeErr("mkdir "+p+": "+e.Error(), 1), nil
	}
	return value.MakeStr(p), nil
}

// builtinMtime returns a file's modification time as float Unix seconds (sub-second
// precision, the same unit as now()), or a catchable Err if the file is missing.
func builtinMtime(args []value.Value) (value.Value, error) {
	p, err := oneString("mtime", args)
	if err != nil {
		return value.MakeNil(), err
	}
	fi, e := os.Stat(p)
	if e != nil {
		return value.MakeErr("mtime "+p+": "+e.Error(), 1), nil
	}
	return value.MakeFloat(epochSeconds(fi.ModTime())), nil
}

func builtinNewer(args []value.Value) (value.Value, error) {
	if len(args) != 2 {
		return value.MakeNil(), fmt.Errorf("newer expects 2 arguments (a, b), got %d", len(args))
	}
	if args[0].Tag() != value.Str || args[1].Tag() != value.Str {
		return value.MakeNil(), typeErrf("newer expects two string paths")
	}
	fa, ea := os.Stat(args[0].AsStr())
	if ea != nil {
		return value.MakeErr("newer: "+ea.Error(), 1), nil
	}
	fb, eb := os.Stat(args[1].AsStr())
	if eb != nil {
		return value.MakeErr("newer: "+eb.Error(), 1), nil
	}
	return value.MakeBool(fa.ModTime().After(fb.ModTime())), nil
}

// builtinStale reports whether target needs rebuilding: true if target is
// missing or older than any source. A missing source is a real error (Err).
func builtinStale(args []value.Value) (value.Value, error) {
	if len(args) != 2 {
		return value.MakeNil(), fmt.Errorf("stale expects 2 arguments (target, sources), got %d", len(args))
	}
	if args[0].Tag() != value.Str {
		return value.MakeNil(), typeErrf("stale: target must be a string")
	}
	target := args[0].AsStr()
	sources, err := stringList("stale", args[1])
	if err != nil {
		return value.MakeNil(), err
	}
	tfi, terr := os.Stat(target)
	if terr != nil {
		return value.MakeBool(true), nil // target missing -> rebuild
	}
	tmod := tfi.ModTime()
	for _, s := range sources {
		sfi, serr := os.Stat(s)
		if serr != nil {
			return value.MakeErr("stale: source "+s+": "+serr.Error(), 1), nil
		}
		if sfi.ModTime().After(tmod) {
			return value.MakeBool(true), nil
		}
	}
	return value.MakeBool(false), nil
}

// stringList accepts a single string (one element) or an array of strings.
func stringList(name string, v value.Value) ([]string, error) {
	switch v.Tag() {
	case value.Str:
		return []string{v.AsStr()}, nil
	case value.Arr:
		elems := v.Obj().(*value.Array).Elems
		out := make([]string, len(elems))
		for i, e := range elems {
			if e.Tag() != value.Str {
				return nil, typeErrf("%s: expected an array of strings", name)
			}
			out[i] = e.AsStr()
		}
		return out, nil
	}
	return nil, typeErrf("%s: expected a string or array of strings, got %s", name, v.TypeName())
}

func builtinGlob(args []value.Value) (value.Value, error) {
	pat, err := oneString("glob", args)
	if err != nil {
		return value.MakeNil(), err
	}
	matches, gerr := globMatch(pat)
	if gerr != nil {
		return value.MakeErr("glob "+pat+": "+gerr.Error(), 1), nil
	}
	out := make([]value.Value, len(matches))
	for i, m := range matches {
		out[i] = value.MakeStr(m)
	}
	return value.MakeArray(out), nil
}

// globMatch returns sorted matches for a pattern. No match is an empty list (not
// an error). A `**` segment matches across directories via a WalkDir fallback.
func globMatch(pattern string) ([]string, error) {
	hasDoublestar := strings.Contains(pattern, "**")
	if hasDoublestar && globSegmentCount(pattern) > 256 {
		return nil, fmt.Errorf("glob pattern exceeds the 256-segment complexity limit")
	}
	limitErr := fmt.Errorf("glob scan exceeds the %d-entry collection limit", maxCollectionItems)
	budget := newFilesystemEntryBudget(maxCollectionItems, limitErr)
	var (
		matches []string
		err     error
	)
	if hasDoublestar {
		matches, err = doublestarGlobBounded(pattern, budget)
	} else {
		matches, err = filepathGlobBounded(filepath.FromSlash(pattern), budget, 0)
	}
	if err != nil {
		return nil, err
	}
	if len(matches) > maxCollectionItems {
		return nil, fmt.Errorf("glob result exceeds the %d-element collection limit", maxCollectionItems)
	}
	sort.Strings(matches)
	return matches, nil
}

// globSegmentCount counts without splitting, so a hostile separator-only
// pattern is rejected before allocating a slice proportional to its length.
func globSegmentCount(pattern string) int {
	segments := 1
	for i := 0; i < len(pattern); i++ {
		if os.IsPathSeparator(pattern[i]) {
			segments++
			if segments > 256 {
				return segments
			}
		}
	}
	return segments
}

func globHasMeta(name string) bool {
	magic := `*?[`
	if runtime.GOOS != "windows" {
		magic = `*?[\`
	}
	return strings.ContainsAny(name, magic)
}

// filepathGlobBounded follows filepath.Glob's recursive component semantics,
// including Windows drive-relative and UNC cleaning, but enumerates each
// directory through readDirBounded instead of Readdirnames(-1).
func filepathGlobBounded(pattern string, budget *filesystemEntryBudget, depth int) ([]string, error) {
	// Match filepath.Glob's recursion guard (CVE-2022-30632) without imposing
	// the stricter doublestar segment ceiling on a long but literal path.
	const pathSeparatorsLimit = 10_000
	if depth == pathSeparatorsLimit {
		return nil, filepath.ErrBadPattern
	}
	if _, err := filepath.Match(pattern, ""); err != nil {
		return nil, err
	}
	if !globHasMeta(pattern) {
		if _, err := os.Lstat(pattern); err != nil {
			return nil, nil
		}
		return []string{pattern}, nil
	}

	dir, file := filepath.Split(pattern)
	volumeLen, dir := cleanGlobDir(dir)
	if !globHasMeta(dir[volumeLen:]) {
		return globDirectoryBounded(dir, file, nil, budget)
	}
	if dir == pattern {
		return nil, filepath.ErrBadPattern
	}
	dirs, err := filepathGlobBounded(dir, budget, depth+1)
	if err != nil {
		return nil, err
	}
	var matches []string
	for _, candidateDir := range dirs {
		matches, err = globDirectoryBounded(candidateDir, file, matches, budget)
		if err != nil {
			return nil, err
		}
	}
	return matches, nil
}

func cleanGlobDir(dir string) (volumeLen int, cleaned string) {
	if runtime.GOOS != "windows" {
		switch dir {
		case "":
			return 0, "."
		case string(filepath.Separator):
			return 0, dir
		default:
			return 0, dir[:len(dir)-1]
		}
	}

	volumeLen = len(filepath.VolumeName(dir))
	switch {
	case dir == "":
		return 0, "."
	case volumeLen+1 == len(dir) && os.IsPathSeparator(dir[len(dir)-1]):
		return volumeLen + 1, dir
	case volumeLen == len(dir) && len(dir) == 2:
		return volumeLen, dir + "."
	default:
		if volumeLen >= len(dir) {
			volumeLen = len(dir) - 1
		}
		return volumeLen, dir[:len(dir)-1]
	}
}

func globDirectoryBounded(dir, pattern string, matches []string, budget *filesystemEntryBudget) ([]string, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return matches, nil // filepath.Glob ignores inaccessible/non-directory candidates
	}
	entries, err := readDirBounded(dir, budget)
	if err != nil {
		if err == budget.limitErr {
			return nil, err
		}
		// filepath.Glob ignores a Readdirnames error but still considers any
		// partial names returned before it.
	}
	for _, entry := range entries {
		matched, matchErr := filepath.Match(pattern, entry.Name())
		if matchErr != nil {
			return matches, matchErr
		}
		if matched {
			matches = append(matches, filepath.Join(dir, entry.Name()))
		}
	}
	return matches, nil
}

func doublestarGlobBounded(pattern string, budget *filesystemEntryBudget) ([]string, error) {
	pat := filepath.ToSlash(pattern)
	segs := strings.Split(pat, "/")
	// Validate wildcard segments so a malformed pattern is an Err here too,
	// rather than silently empty (matching the non-** filepath.Glob path).
	for _, s := range segs {
		if s == "**" {
			continue
		}
		if _, err := path.Match(s, ""); err != nil {
			return nil, err
		}
	}
	root := globBase(pat)
	if root == "" {
		root = "."
	}
	rootPath := filepath.FromSlash(root)
	var matches []string
	walkErr := walkDirBounded(rootPath, budget, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // missing/unreadable roots and entries are no matches
		}
		if p == rootPath {
			return nil // never yield the walk root itself ("." or the bare base dir)
		}
		if matchSegs(segs, strings.Split(filepath.ToSlash(p), "/")) {
			matches = append(matches, p)
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Strings(matches)
	return matches, nil
}

// globBase is the leading wildcard-free prefix of a forward-slash pattern.
func globBase(pat string) string {
	var base []string
	for _, s := range strings.Split(pat, "/") {
		if strings.ContainsAny(s, "*?[") {
			break
		}
		base = append(base, s)
	}
	return strings.Join(base, "/")
}

// matchSegs reports whether path segments ns match pattern segments ps, where a `**`
// pattern segment spans zero or more path segments. It memoizes on the (pi, ni) position
// pair, so several `**` segments cost O(len(ps)*len(ns)) instead of exponential
// backtracking — a pattern like a/**/b/**/c/**/d matched against a deep tree would
// otherwise hang. Adjacent `**` are collapsed first (a/**/**/b is the same set as
// a/**/b), which also caps the work a pattern padded with many `**` can demand.
func matchSegs(ps, ns []string) bool {
	ps = collapseDoublestars(ps)
	np, nn := len(ps), len(ns)
	if np > 256 || nn > 4096 || np+1 > maxCollectionItems/(nn+1) {
		return false
	}
	memo := make([]uint8, (np+1)*(nn+1)) // per (pi, ni): 0 unknown, 1 no, 2 yes
	var rec func(pi, ni int) bool
	rec = func(pi, ni int) bool {
		idx := pi*(nn+1) + ni
		if memo[idx] != 0 {
			return memo[idx] == 2
		}
		var res bool
		switch {
		case pi == np:
			res = ni == nn // pattern exhausted: match iff the path is too
		case ps[pi] == "**":
			if pi == np-1 {
				res = true // a trailing ** absorbs the rest, including zero segments
			} else {
				for i := ni; i <= nn; i++ { // ** consumes zero or more path segments
					if rec(pi+1, i) {
						res = true
						break
					}
				}
			}
		case ni == nn:
			res = false // a non-** segment remains but the path is spent
		default:
			if ok, _ := path.Match(ps[pi], ns[ni]); ok {
				res = rec(pi+1, ni+1)
			}
		}
		if res {
			memo[idx] = 2
		} else {
			memo[idx] = 1
		}
		return res
	}
	return rec(0, 0)
}

// collapseDoublestars drops redundant adjacent ** segments (they match the same set as a
// single **), so a pattern padded with many ** cannot inflate the match work.
func collapseDoublestars(segs []string) []string {
	var out []string
	for _, s := range segs {
		if s == "**" && len(out) > 0 && out[len(out)-1] == "**" {
			continue
		}
		out = append(out, s)
	}
	return out
}

// builtinReadDir lists a directory as an array of {name, path, is_dir} records
// (sorted by name, as os.ReadDir guarantees). A missing/unreadable dir is a
// catchable Err. More structured than glob(join(dir, "*")).
func builtinReadDir(args []value.Value) (value.Value, error) {
	p, err := oneString("read_dir", args)
	if err != nil {
		return value.MakeNil(), err
	}
	limitErr := fmt.Errorf("read_dir: result exceeds the %d-element collection limit", maxCollectionItems)
	entries, e := readDirBounded(p, newFilesystemEntryBudget(maxCollectionItems, limitErr))
	if e != nil {
		if e == limitErr {
			return value.MakeErr(e.Error(), 1), nil
		}
		return value.MakeErr("read_dir "+p+": "+e.Error(), 1), nil
	}
	out := make([]value.Value, len(entries))
	for i, de := range entries {
		m := value.MakeMap()
		om := m.Obj().(*value.OrderedMap)
		om.Set(value.MakeStr("name"), value.MakeStr(de.Name()))
		om.Set(value.MakeStr("path"), value.MakeStr(filepath.Join(p, de.Name())))
		om.Set(value.MakeStr("is_dir"), value.MakeBool(de.IsDir()))
		om.Set(value.MakeStr("is_symlink"), value.MakeBool(de.Type()&os.ModeSymlink != 0))
		out[i] = m
	}
	return value.MakeArray(out), nil
}

// builtinReadlink returns the target a symlink points to (os.Readlink), without
// following it. A non-symlink or missing path is a catchable Err.
func builtinReadlink(args []value.Value) (value.Value, error) {
	p, err := oneString("readlink", args)
	if err != nil {
		return value.MakeNil(), err
	}
	t, e := os.Readlink(p)
	if e != nil {
		return value.MakeErr("readlink "+p+": "+e.Error(), 1), nil
	}
	return value.MakeStr(t), nil
}

// builtinWalk recursively lists everything under dir as an array of
// {name, path, is_dir, is_symlink, size, mtime} records, depth-first in lexical order.
// The root itself is not included. Symlinks are reported (is_symlink) but never
// followed, so a symlink cycle cannot loop the walk. Unreadable entries are skipped;
// only an unreadable or non-directory root is a catchable Err. Broader than read_dir
// (one level) or glob (pattern match); compose with filter/map for the collection you want.
func builtinWalk(args []value.Value) (value.Value, error) {
	p, err := oneString("walk", args)
	if err != nil {
		return value.MakeNil(), err
	}
	if fi, e := os.Stat(p); e != nil || !fi.IsDir() {
		return value.MakeErr("walk "+p+": not a readable directory", 1), nil
	}
	out := []value.Value{}
	errWalkLimit := fmt.Errorf("walk result exceeds the %d-element collection limit", maxCollectionItems)
	budget := newFilesystemEntryBudget(maxCollectionItems, errWalkLimit)
	walkErr := walkDirBounded(p, budget, func(path string, d os.DirEntry, werr error) error {
		if werr != nil {
			if path == p {
				return werr
			}
			if d != nil && d.IsDir() {
				return filepath.SkipDir // unreadable subdir: skip its subtree, keep going
			}
			return nil
		}
		if path == p {
			return nil // exclude the root itself
		}
		var sz int64
		var mt float64
		if info, ierr := d.Info(); ierr == nil {
			sz = info.Size()
			mt = epochSeconds(info.ModTime())
		}
		m := value.MakeMap()
		om := m.Obj().(*value.OrderedMap)
		om.Set(value.MakeStr("name"), value.MakeStr(d.Name()))
		om.Set(value.MakeStr("path"), value.MakeStr(path))
		om.Set(value.MakeStr("is_dir"), value.MakeBool(d.IsDir()))
		om.Set(value.MakeStr("is_symlink"), value.MakeBool(d.Type()&os.ModeSymlink != 0))
		om.Set(value.MakeStr("size"), value.MakeInt(sz))
		om.Set(value.MakeStr("mtime"), value.MakeFloat(mt))
		out = append(out, m)
		return nil
	})
	if walkErr != nil {
		return value.MakeErr("walk "+p+": "+walkErr.Error(), 1), nil
	}
	return value.MakeArray(out), nil
}

// --- file IO ---

// maxReadFileBytes backstops read_file, which loads the whole file into one string. Without
// a bound an unbounded source — a multi-gigabyte file, or a named pipe / device that never
// reaches EOF — could exhaust memory; past this the read is a catchable Err, not an OOM. The
// 64 MiB matches the interpreter's other whole-value limits; larger data should use the
// streaming one-liner/process paths. A var, not a const, only so a test can lower it.
var maxReadFileBytes int64 = 64 << 20

func builtinReadFile(args []value.Value) (value.Value, error) {
	p, err := oneString("read_file", args)
	if err != nil {
		return value.MakeNil(), err
	}
	f, e := os.Open(p)
	if e != nil {
		return value.MakeErr("read_file "+p+": "+e.Error(), 1), nil
	}
	defer f.Close()
	// Read at most maxReadFileBytes+1 so an over-limit file is detected without buffering it
	// all: the +1 byte is the "too big" signal (LimitReader bounds memory regardless of the
	// stat'd size, which lies for pipes/devices).
	b, e := io.ReadAll(io.LimitReader(f, maxReadFileBytes+1))
	if e != nil {
		return value.MakeErr("read_file "+p+": "+e.Error(), 1), nil
	}
	if int64(len(b)) > maxReadFileBytes {
		return value.MakeErr(fmt.Sprintf("read_file %s: file exceeds the %d-byte limit", p, maxReadFileBytes), 1), nil
	}
	return value.MakeStr(string(b)), nil
}

func builtinWriteFile(args []value.Value) (value.Value, error) {
	if len(args) < 2 || len(args) > 3 {
		return value.MakeNil(), fmt.Errorf("write_file expects 2 or 3 arguments (path, content, opts?), got %d", len(args))
	}
	if args[0].Tag() != value.Str {
		return value.MakeNil(), typeErrf("write_file: path must be a string")
	}
	p := args[0].AsStr()
	appendMode := false
	if len(args) == 3 {
		if args[2].Tag() != value.Map {
			return value.MakeErr("write_file: opts must be a map, got "+args[2].TypeName(), 1), nil
		}
		m := args[2].Obj().(*value.OrderedMap)
		for _, k := range m.Keys() {
			if k.Tag() != value.Str || k.AsStr() != "append" {
				return value.MakeErr("write_file: unknown option "+k.Display(), 1), nil
			}
		}
		if v, ok := m.Get(value.MakeStr("append")); ok {
			if v.Tag() != value.Bool {
				return value.MakeErr("write_file: append must be a bool, got "+v.TypeName(), 1), nil
			}
			appendMode = v.AsBool()
		}
	}
	content, ok := displayWithin(args[1], maxStringBytes) // strings stay raw; other values use Display form
	if !ok {
		return value.MakeErr(fmt.Sprintf("write_file: content exceeds the %d-byte string limit", maxStringBytes), 1), nil
	}
	if appendMode {
		f, e := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if e != nil {
			return value.MakeErr("write_file "+p+": "+e.Error(), 1), nil
		}
		_, we := f.WriteString(content)
		ce := f.Close()
		if we != nil {
			return value.MakeErr("write_file "+p+": "+we.Error(), 1), nil
		}
		if ce != nil {
			return value.MakeErr("write_file "+p+": "+ce.Error(), 1), nil
		}
		return value.MakeStr(p), nil
	}
	if e := os.WriteFile(p, []byte(content), 0o644); e != nil {
		return value.MakeErr("write_file "+p+": "+e.Error(), 1), nil
	}
	return value.MakeStr(p), nil
}

// builtinTempFile creates a fresh, uniquely-named empty file in the system temp dir and
// returns its path. An optional prefix names it; the caller removes it (rm) when done.
func builtinTempFile(args []value.Value) (value.Value, error) {
	prefix, err := tempPrefix("tempfile", args)
	if err != nil {
		return value.MakeNil(), err
	}
	f, e := os.CreateTemp("", prefix+"-*")
	if e != nil {
		return value.MakeErr("tempfile: "+e.Error(), 1), nil
	}
	name := f.Name()
	f.Close()
	return value.MakeStr(name), nil
}

// builtinTempDir creates a fresh, uniquely-named directory in the system temp dir and
// returns its path; the caller removes it (rm) when done.
func builtinTempDir(args []value.Value) (value.Value, error) {
	prefix, err := tempPrefix("tempdir", args)
	if err != nil {
		return value.MakeNil(), err
	}
	p, e := os.MkdirTemp("", prefix+"-*")
	if e != nil {
		return value.MakeErr("tempdir: "+e.Error(), 1), nil
	}
	return value.MakeStr(p), nil
}

// tempPrefix resolves the optional prefix argument (default "drang") for tempfile/tempdir.
func tempPrefix(name string, args []value.Value) (string, error) {
	if len(args) > 1 {
		return "", fmt.Errorf("%s expects 0 or 1 arguments (prefix?), got %d", name, len(args))
	}
	if len(args) == 1 {
		if args[0].Tag() != value.Str {
			return "", typeErrf("%s: prefix must be a string", name)
		}
		return args[0].AsStr(), nil
	}
	return "drang", nil
}

// --- atomic-swap family: rename, rm (recursive force delete), copy, size ---

func builtinRename(args []value.Value) (value.Value, error) {
	src, dst, err := twoStrings("rename", args)
	if err != nil {
		return value.MakeNil(), err
	}
	if e := os.Rename(src, dst); e != nil {
		return value.MakeErr("rename "+src+" -> "+dst+": "+e.Error(), 1), nil
	}
	return value.MakeStr(dst), nil
}

// builtinRm removes a file or directory tree (recursive, idempotent). Named rm
// because delete is the map-key remover.
func builtinRm(args []value.Value) (value.Value, error) {
	p, err := oneString("rm", args)
	if err != nil {
		return value.MakeNil(), err
	}
	if e := os.RemoveAll(p); e != nil {
		return value.MakeErr("rm "+p+": "+e.Error(), 1), nil
	}
	return value.MakeStr(p), nil
}

func builtinCopy(args []value.Value) (value.Value, error) {
	src, dst, err := twoStrings("copy", args)
	if err != nil {
		return value.MakeNil(), err
	}
	if e := copyPath(src, dst); e != nil {
		return value.MakeErr("copy "+src+" -> "+dst+": "+e.Error(), 1), nil
	}
	return value.MakeStr(dst), nil
}

func copyPath(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := validateCopyTarget(src, dst, fi); err != nil {
		return err
	}
	if fi.IsDir() {
		return copyTree(src, dst)
	}
	return copyFile(src, dst, fi.Mode())
}

// validateCopyTarget rejects aliases of the source and directory descendants
// before copyFile can truncate anything or copyTree can manufacture its own
// destination while walking. Handle-based canonicalization resolves Windows
// symlinks and junctions; canonicalProspectivePath additionally resolves the
// nearest existing target ancestor so `alias-to-src/new-dir` is recognized
// even though new-dir does not exist yet. os.SameFile covers hard links and any
// other filesystem identity alias not visible from path spelling alone.
func validateCopyTarget(src, dst string, srcInfo os.FileInfo) error {
	if dstLinkInfo, err := os.Lstat(dst); err == nil {
		// A staged rename over a symlink would replace the link object, while
		// the historical direct open followed it. Refuse that ambiguous target
		// rather than unexpectedly writing outside dst's lexical location or
		// silently changing which filesystem object is replaced.
		if err := rejectCopyDestinationRedirect(dst, dstLinkInfo); err != nil {
			return err
		}
		dstInfo, err := os.Stat(dst)
		if err != nil {
			return err
		}
		if os.SameFile(srcInfo, dstInfo) {
			return fmt.Errorf("source and destination are the same filesystem object")
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	srcCanonical, err := canonicalExistingPath(src)
	if err != nil {
		return err
	}
	dstCanonical, err := canonicalProspectivePath(dst)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(srcCanonical, dstCanonical)
	if err == nil && rel == "." {
		return fmt.Errorf("source and destination resolve to the same path")
	}
	if srcInfo.IsDir() && err == nil && pathIsSameOrWithin(rel) {
		return fmt.Errorf("destination is inside the source directory")
	}
	return nil
}

func canonicalExistingPath(name string) (string, error) {
	abs, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	resolved, err := finalWindowsPath(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

var getFinalPathNameByHandleW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetFinalPathNameByHandleW")

// finalWindowsPath resolves every reparse-point component through an opened
// handle. filepath.EvalSymlinks only recognizes entries whose FileMode has
// ModeSymlink; current Go deliberately exposes Windows junctions as generic
// reparse points instead, so EvalSymlinks alone can leave a junction alias
// unresolved.
func finalWindowsPath(abs string) (string, error) {
	name := abs
	if !strings.HasPrefix(name, `\\?\`) && !strings.HasPrefix(name, `\??\`) {
		if strings.HasPrefix(name, `\\`) {
			name = `\\?\UNC\` + name[2:]
		} else {
			name = `\\?\` + name
		}
	}
	namep, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return "", err
	}
	h, err := syscall.CreateFile(
		namep,
		0,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return "", err
	}
	defer syscall.CloseHandle(h)

	buf := make([]uint16, 512)
	for {
		n, _, callErr := getFinalPathNameByHandleW.Call(
			uintptr(h),
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(len(buf)),
			0, // FILE_NAME_NORMALIZED | VOLUME_NAME_DOS
		)
		if n == 0 {
			if callErr != syscall.Errno(0) {
				return "", callErr
			}
			return "", fmt.Errorf("GetFinalPathNameByHandleW failed for %s", abs)
		}
		if n >= uintptr(len(buf)) {
			// Windows paths are limited to 32,767 UTF-16 code units. Bound the
			// retry even if an unexpected provider reports a corrupt size.
			if n > 32_767 {
				return "", fmt.Errorf("resolved path is too long: %s", abs)
			}
			buf = make([]uint16, int(n)+1)
			continue
		}
		resolved := syscall.UTF16ToString(buf[:n])
		switch {
		case strings.HasPrefix(resolved, `\\?\UNC\`):
			resolved = `\\` + resolved[len(`\\?\UNC\`):]
		case strings.HasPrefix(resolved, `\\?\`):
			resolved = resolved[len(`\\?\`):]
		}
		return resolved, nil
	}
}

// canonicalProspectivePath resolves as much of a possibly nonexistent path as
// the filesystem currently knows, then reattaches its missing tail. Walking to
// the nearest existing ancestor is what makes a destination beneath a junction
// or symlink comparable to the canonical source directory.
func canonicalProspectivePath(name string) (string, error) {
	current, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	current = filepath.Clean(current)
	var missing []string
	for {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := canonicalExistingPath(current)
			if err != nil {
				return "", err
			}
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		} else if !os.IsNotExist(err) {
			return "", err
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("cannot resolve destination path %s", name)
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func pathIsSameOrWithin(rel string) bool {
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	// Stage in the destination directory. Opening dst with O_TRUNC first would
	// destroy an existing good file if the source read or destination write then
	// failed; a same-directory rename makes the successful replacement atomic.
	out, err := os.CreateTemp(filepath.Dir(dst), ".drang-copy-*")
	if err != nil {
		return err
	}
	tmpName := out.Name()
	committed := false
	defer func() {
		_ = out.Close()
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := copyFileData(out, in); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return err
	}
	committed = true
	return nil
}

// A variable only so a focused test can inject a mid-copy I/O error and prove
// that staged copying preserves the previous destination. Production never
// reassigns it.
var copyFileData = io.Copy

func copyTree(src, dst string) error {
	// copyPath classifies the source with Stat, so an explicitly supplied
	// directory symlink or junction is a directory source. Resolve that root
	// once, then retain the walker's normal non-following behavior for links
	// encountered inside the tree.
	srcRoot, err := canonicalExistingPath(src)
	if err != nil {
		return err
	}

	// Work below the resolved destination root. This preserves an explicitly
	// supplied path through an ancestor alias, while ensuring that child checks
	// and writes use one stable lexical tree rather than repeatedly traversing
	// that alias.
	dstRoot, err := canonicalProspectivePath(dst)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dstRoot, 0o755); err != nil {
		return err
	}
	if err := ensureCopyTreeDirectory(dstRoot, "."); err != nil {
		return err
	}

	limitErr := fmt.Errorf("copy traversal exceeds the %d-entry collection limit", maxCollectionItems)
	budget := newFilesystemEntryBudget(maxCollectionItems, limitErr)
	return walkDirBounded(srcRoot, budget, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcRoot, p)
		if err != nil {
			return err
		}
		if filepath.IsAbs(rel) || !pathIsSameOrWithin(rel) {
			return fmt.Errorf("source traversal escaped its root: %s", p)
		}
		if rel == "." {
			if !d.IsDir() {
				return fmt.Errorf("source directory changed during copy: %s", src)
			}
			return nil
		}
		if d.IsDir() {
			return ensureCopyTreeDirectory(dstRoot, rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if isWindowsRedirect(info) {
			followed, err := os.Stat(p)
			if err != nil {
				return err
			}
			if followed.IsDir() {
				return fmt.Errorf("source tree contains a directory symlink or junction: %s", p)
			}
		}
		if err := ensureCopyTreeDirectory(dstRoot, filepath.Dir(rel)); err != nil {
			return err
		}
		target := filepath.Join(dstRoot, rel)
		if targetInfo, err := os.Lstat(target); err == nil {
			if err := rejectCopyDestinationRedirect(target, targetInfo); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		return copyFile(p, target, info.Mode())
	})
}

// ensureCopyTreeDirectory validates every already-existing component from the
// destination root to rel before creating or using the directory. In
// particular, MkdirAll must not be used for merge children: it follows a
// pre-existing symlink or Windows junction and could redirect a later file
// copy outside dst (or back into src).
func ensureCopyTreeDirectory(dstRoot, rel string) error {
	cleanRel := filepath.Clean(rel)
	if filepath.IsAbs(cleanRel) || !pathIsSameOrWithin(cleanRel) {
		return fmt.Errorf("invalid destination-relative path %s", rel)
	}

	current := dstRoot
	components := []string(nil)
	if cleanRel != "." {
		components = strings.Split(cleanRel, string(filepath.Separator))
	}
	for i := -1; i < len(components); i++ {
		if i >= 0 {
			component := components[i]
			if component == "" || component == "." || component == ".." {
				return fmt.Errorf("invalid destination path component %q", component)
			}
			current = filepath.Join(current, component)
		}

		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, 0o755); err != nil && !os.IsExist(err) {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		if err := rejectCopyDestinationRedirect(current, info); err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("destination path component is not a directory: %s", current)
		}
	}
	return nil
}

// rejectCopyDestinationRedirect rejects all Windows reparse points, not only
// entries that Go exposes as ModeSymlink. Name-surrogate reparse points include
// directory junctions and mount points; following any of them during a merge
// would move the mutation outside the destination tree. Rejecting the broader
// attribute is intentionally conservative for other reparse-backed entries.
func rejectCopyDestinationRedirect(name string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("destination is a symlink or junction: %s", name)
	}
	if isWindowsRedirect(info) {
		return fmt.Errorf("destination is a reparse point: %s", name)
	}
	return nil
}

func isWindowsRedirect(info os.FileInfo) bool {
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

func builtinSize(args []value.Value) (value.Value, error) {
	p, err := oneString("size", args)
	if err != nil {
		return value.MakeNil(), err
	}
	fi, e := os.Stat(p)
	if e != nil {
		return value.MakeErr("size "+p+": "+e.Error(), 1), nil
	}
	return value.MakeInt(fi.Size()), nil
}
