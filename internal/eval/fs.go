package eval

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

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

// --- path helpers: pure string transforms (never touch the disk); a non-string arg is a catchable Err ---

// builtinJoin is polymorphic: join(array, sep?) joins the array's elements into
// a string (the universal meaning of join), while join(str, str, ...) joins path
// segments. The first-argument type disambiguates (a path part is never an array).
func builtinJoin(args []value.Value) (value.Value, error) {
	if len(args) >= 1 && args[0].Tag() == value.Arr {
		return joinStrings(args)
	}
	parts := make([]string, len(args))
	for i, a := range args {
		if a.Tag() != value.Str {
			return value.MakeNil(), typeErrf("join: argument %d must be a string, got %s", i+1, a.TypeName())
		}
		parts[i] = a.AsStr()
	}
	return value.MakeStr(filepath.Join(parts...)), nil
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

// builtinAbspath resolves a path to absolute against the CWD. (Numeric absolute
// value is the abs builtin; this was renamed from abs to free that name.)
func builtinAbspath(args []value.Value) (value.Value, error) {
	p, err := oneString("abspath", args)
	if err != nil {
		return value.MakeNil(), err
	}
	a, e := filepath.Abs(p)
	if e != nil {
		return value.MakeErr("abspath "+p+": "+e.Error(), 1), nil
	}
	return value.MakeStr(a), nil
}

func builtinSlash(args []value.Value) (value.Value, error) {
	p, err := oneString("slash", args)
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
	return value.MakeStr(r), nil
}

// builtinWithin reports whether target is inside base (or equal to it). It is a
// guard (always a bool): uncomparable paths or any "../"-escaping relative path
// are simply not within.
func builtinWithin(args []value.Value) (value.Value, error) {
	base, target, err := twoStrings("within", args)
	if err != nil {
		return value.MakeNil(), err
	}
	r, e := filepath.Rel(base, target)
	if e != nil {
		return value.MakeBool(false), nil
	}
	within := r == "." || (r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)))
	return value.MakeBool(within), nil
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
	return value.MakeFloat(float64(fi.ModTime().UnixNano()) / 1e9), nil
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
	if !strings.Contains(pattern, "**") {
		m, err := filepath.Glob(filepath.FromSlash(pattern))
		if err != nil {
			return nil, err
		}
		sort.Strings(m)
		return m, nil
	}
	return doublestarGlob(pattern)
}

func doublestarGlob(pattern string) ([]string, error) {
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
	_ = filepath.WalkDir(rootPath, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if p == rootPath {
			return nil // never yield the walk root itself ("." or the bare base dir)
		}
		if matchSegs(segs, strings.Split(filepath.ToSlash(p), "/")) {
			matches = append(matches, p)
		}
		return nil
	})
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

// matchSegs matches path segments where ** spans zero or more segments.
func matchSegs(ps, ns []string) bool {
	for len(ps) > 0 {
		if ps[0] == "**" {
			if len(ps) == 1 {
				return true
			}
			for i := 0; i <= len(ns); i++ {
				if matchSegs(ps[1:], ns[i:]) {
					return true
				}
			}
			return false
		}
		if len(ns) == 0 {
			return false
		}
		if ok, _ := path.Match(ps[0], ns[0]); !ok {
			return false
		}
		ps, ns = ps[1:], ns[1:]
	}
	return len(ns) == 0
}

// builtinReadDir lists a directory as an array of {name, path, is_dir} records
// (sorted by name, as os.ReadDir guarantees). A missing/unreadable dir is a
// catchable Err. More structured than glob(join(dir, "*")).
func builtinReadDir(args []value.Value) (value.Value, error) {
	p, err := oneString("read_dir", args)
	if err != nil {
		return value.MakeNil(), err
	}
	entries, e := os.ReadDir(p)
	if e != nil {
		return value.MakeErr("read_dir "+p+": "+e.Error(), 1), nil
	}
	out := make([]value.Value, len(entries))
	for i, de := range entries {
		m := value.MakeMap()
		om := m.Obj().(*value.OrderedMap)
		om.Set(value.MakeStr("name"), value.MakeStr(de.Name()))
		om.Set(value.MakeStr("path"), value.MakeStr(filepath.Join(p, de.Name())))
		om.Set(value.MakeStr("is_dir"), value.MakeBool(de.IsDir()))
		out[i] = m
	}
	return value.MakeArray(out), nil
}

// --- file IO ---

func builtinReadFile(args []value.Value) (value.Value, error) {
	p, err := oneString("read_file", args)
	if err != nil {
		return value.MakeNil(), err
	}
	b, e := os.ReadFile(p)
	if e != nil {
		return value.MakeErr("read_file "+p+": "+e.Error(), 1), nil
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
	content := args[1].Display() // any value renders via its display; a string carries raw bytes
	appendMode := false
	if len(args) == 3 {
		if args[2].Tag() != value.Map {
			return value.MakeErr("write_file: opts must be a map, got "+args[2].TypeName(), 1), nil
		}
		m := args[2].Obj().(*value.OrderedMap)
		for _, k := range m.Keys() {
			if k.Display() != "append" {
				return value.MakeErr("write_file: unknown option "+k.Display(), 1), nil
			}
		}
		if v, ok := m.Get(value.MakeStr("append")); ok {
			appendMode = v.Truthy()
		}
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
	if fi.IsDir() {
		return copyTree(src, dst)
	}
	return copyFile(src, dst, fi.Mode())
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
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(p, target, info.Mode())
	})
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
