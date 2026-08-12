package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/anafalanx/drang/internal/printer"
)

// runFmt implements `drang fmt [flags] [path...]`: it reprints drang source canonically,
// preserving comments. With no paths it filters stdin to stdout. With file/dir paths it
// prints to stdout by default, or rewrites in place with -w. --check / -l / -d report
// rather than write. Output is always re-verified (the printer's drop-guard), so a parse
// error or a dropped comment leaves files untouched and exits non-zero.
func runFmt(args []string) {
	var write, check, list, diff, fix bool
	var paths []string
	options := true
	outputMode := ""
	setOutputMode := func(flag string) {
		if outputMode != "" {
			fmt.Fprintf(os.Stderr, "drang fmt: %s and %s are mutually exclusive\n", outputMode, flag)
			os.Exit(2)
		}
		outputMode = flag
	}
	for _, a := range args {
		if options && a == "--" {
			options = false
			continue
		}
		if !options {
			paths = append(paths, a)
			continue
		}
		switch a {
		case "-w", "--write":
			setOutputMode(a)
			write = true
		case "-c", "--check":
			setOutputMode(a)
			check = true
		case "-l", "--list":
			setOutputMode(a)
			list = true
		case "-d", "--diff":
			setOutputMode(a)
			diff = true
		case "--fix":
			if fix {
				fmt.Fprintln(os.Stderr, "drang fmt: --fix specified more than once")
				os.Exit(2)
			}
			fix = true
		case "-h", "--help":
			fmtHelp()
			os.Exit(0)
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintf(os.Stderr, "drang fmt: unknown flag %q\n", a)
				os.Exit(2)
			}
			paths = append(paths, a)
		}
	}
	if write && len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "drang fmt: -w needs file or directory paths (cannot rewrite stdin)")
		os.Exit(2)
	}

	if len(paths) == 0 {
		fmtStdin(check || list || diff, diff, fix)
		return
	}

	expanded, expandErr := expandFmtPaths(paths)
	if expandErr != nil {
		fmt.Fprintln(os.Stderr, "drang fmt:", expandErr)
		os.Exit(2)
	}
	anyChanged, anyErr := false, false
	for _, f := range expanded {
		src, err := readFileLimited(f, maxSourceBytes, "source file")
		if err != nil {
			fmt.Fprintf(os.Stderr, "drang fmt: %v\n", err)
			anyErr = true
			continue
		}
		out, ferr := formatSource(string(src), fix)
		if ferr != nil {
			fmt.Fprintf(os.Stderr, "drang fmt: %s: %v\n", f, ferr)
			anyErr = true
			continue
		}
		changed := out != string(src)
		switch {
		case write:
			if changed {
				if isReadOnly(f) {
					// Respect a deliberate read-only marking; report it and leave the file.
					fmt.Fprintf(os.Stderr, "drang fmt: %s: read-only, not modified\n", f)
					anyErr = true
				} else if werr := writeFileAtomic(f, out); werr != nil {
					fmt.Fprintf(os.Stderr, "drang fmt: %v\n", werr)
					anyErr = true
				}
			}
		case check:
			if changed {
				anyChanged = true
				fmt.Fprintln(os.Stderr, f)
			}
		case list:
			if changed {
				anyChanged = true
				fmt.Println(f)
			}
		case diff:
			if changed {
				anyChanged = true
				d, derr := unifiedDiff(f, string(src), out)
				if derr != nil {
					fmt.Fprintf(os.Stderr, "drang fmt: %s: %v\n", f, derr)
					anyErr = true
					continue
				}
				os.Stdout.WriteString(d)
			}
		default:
			os.Stdout.WriteString(out)
		}
	}
	switch {
	case anyErr:
		os.Exit(2)
	case (check || list || diff) && anyChanged:
		os.Exit(1)
	}
}

// fmtStdin formats stdin. In report mode (check/list/diff) it writes a diff and/or exits
// non-zero when the input is not already formatted; otherwise it writes the formatted
// source to stdout.
// formatSource formats src, applying migration rewrites first when fix is set.
func formatSource(src string, fix bool) (string, error) {
	if fix {
		return printer.FormatFix(src)
	}
	return printer.Format(src)
}

func fmtStdin(report, diff, fix bool) {
	src, err := readAllLimited(os.Stdin, maxSourceBytes, "stdin source")
	if err != nil {
		fmt.Fprintln(os.Stderr, "drang fmt:", err)
		os.Exit(2)
	}
	out, ferr := formatSource(string(src), fix)
	if ferr != nil {
		fmt.Fprintln(os.Stderr, "drang fmt: <stdin>:", ferr)
		os.Exit(1)
	}
	if report {
		changed := out != string(src)
		if diff && changed {
			d, derr := unifiedDiff("<stdin>", string(src), out)
			if derr != nil {
				fmt.Fprintln(os.Stderr, "drang fmt: <stdin>:", derr)
				os.Exit(2)
			}
			os.Stdout.WriteString(d)
		}
		if changed {
			os.Exit(1)
		}
		return
	}
	os.Stdout.WriteString(out)
}

// expandFmtPaths turns the given paths into a flat list of files: a directory is walked
// for *.dr files (skipping .git and dot-directories); a file is taken as-is. Unreadable
// paths are passed through so the caller reports the error.
func expandFmtPaths(paths []string) ([]string, error) {
	var out []string
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			out = append(out, p)
			continue
		}
		if !fi.IsDir() {
			out = append(out, p)
			continue
		}
		if err := filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path != p && (d.Name() == ".git" || strings.HasPrefix(d.Name(), ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, ".dr") {
				out = append(out, path)
			}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("walk %s: %w", p, err)
		}
	}
	return out, nil
}

// writeFileAtomic writes content to a temp file in the same directory, preserves the
// original file mode, and renames it over path (atomic on a single filesystem).
func writeFileAtomic(path, content string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".drang-fmt-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.WriteString(content)
	serr := tmp.Sync()
	cerr := tmp.Close()
	if werr != nil {
		os.Remove(name)
		return werr
	}
	if serr != nil {
		os.Remove(name)
		return serr
	}
	if cerr != nil {
		os.Remove(name)
		return cerr
	}
	if fi, e := os.Stat(path); e == nil {
		if err := os.Chmod(name, fi.Mode()); err != nil {
			os.Remove(name)
			return err
		}
	}
	if rerr := os.Rename(name, path); rerr != nil {
		os.Remove(name)
		return rerr
	}
	return nil
}

// isReadOnly reports whether path exists and has its owner-write bit cleared (the
// read-only marking, including the Windows read-only attribute). drang fmt -w respects
// that marking rather than overriding it.
func isReadOnly(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().Perm()&0o200 == 0
}

// Ordinary diffs use an LCS to align lines. Its matrix is quadratic, so large
// inputs fall back to a linear prefix/suffix alignment rather than letting `fmt
// -d` turn a bounded source file into a multi-gigabyte allocation.
var (
	maxDiffLCSCells    int64 = 4_000_000
	maxDiffLines       int64 = 1_000_000
	maxDiffOutputBytes int64 = 64 << 20
)

// unifiedDiff returns a line-based diff of a vs b (every line annotated: "  " context,
// "-" removed, "+" added). Empty when a == b.
func unifiedDiff(name, a, b string) (string, error) {
	na, nb := diffLineCount(a), diffLineCount(b)
	if na > maxDiffLines || nb > maxDiffLines {
		return "", fmt.Errorf("diff exceeds the %d-line limit", maxDiffLines)
	}
	at, bt := splitLines(a), splitLines(b)
	n, m := len(at), len(bt)
	if int64(n+1)*int64(m+1) > maxDiffLCSCells {
		return linearUnifiedDiff(name, at, bt)
	}
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if at[i] == bt[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	d := newDiffBuilder()
	if err := d.header(name); err != nil {
		return "", err
	}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case at[i] == bt[j]:
			if err := d.line("  ", at[i]); err != nil {
				return "", err
			}
			i, j = i+1, j+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			if err := d.line("-", at[i]); err != nil {
				return "", err
			}
			i++
		default:
			if err := d.line("+", bt[j]); err != nil {
				return "", err
			}
			j++
		}
	}
	for ; i < n; i++ {
		if err := d.line("-", at[i]); err != nil {
			return "", err
		}
	}
	for ; j < m; j++ {
		if err := d.line("+", bt[j]); err != nil {
			return "", err
		}
	}
	return d.String(), nil
}

func diffLineCount(s string) int64 {
	if s == "" {
		return 0
	}
	n := int64(strings.Count(s, "\n"))
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

type diffBuilder struct {
	strings.Builder
}

func newDiffBuilder() *diffBuilder { return &diffBuilder{} }

func (d *diffBuilder) write(s string) error {
	if int64(len(s)) > maxDiffOutputBytes-int64(d.Len()) {
		return fmt.Errorf("diff output exceeds the %d MiB limit", maxDiffOutputBytes>>20)
	}
	d.WriteString(s)
	return nil
}

func (d *diffBuilder) header(name string) error {
	if err := d.write("--- "); err != nil {
		return err
	}
	if err := d.write(name); err != nil {
		return err
	}
	if err := d.write(" (original)\n+++ "); err != nil {
		return err
	}
	if err := d.write(name); err != nil {
		return err
	}
	return d.write(" (formatted)\n")
}

func (d *diffBuilder) line(prefix, line string) error {
	need := int64(len(prefix)) + int64(len(line)) + 1
	if need > maxDiffOutputBytes-int64(d.Len()) {
		return fmt.Errorf("diff output exceeds the %d MiB limit", maxDiffOutputBytes>>20)
	}
	d.WriteString(prefix)
	d.WriteString(line)
	d.WriteByte('\n')
	return nil
}

// linearUnifiedDiff preserves the common prefix and suffix and reports the
// changed middle as one replacement. It may be less minimal than the LCS form,
// but it is exact, deterministic, and O(n+m) in both time and memory.
func linearUnifiedDiff(name string, at, bt []string) (string, error) {
	prefix := 0
	for prefix < len(at) && prefix < len(bt) && at[prefix] == bt[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(at)-prefix && suffix < len(bt)-prefix &&
		at[len(at)-1-suffix] == bt[len(bt)-1-suffix] {
		suffix++
	}

	d := newDiffBuilder()
	if err := d.header(name); err != nil {
		return "", err
	}
	for _, line := range at[:prefix] {
		if err := d.line("  ", line); err != nil {
			return "", err
		}
	}
	for _, line := range at[prefix : len(at)-suffix] {
		if err := d.line("-", line); err != nil {
			return "", err
		}
	}
	for _, line := range bt[prefix : len(bt)-suffix] {
		if err := d.line("+", line); err != nil {
			return "", err
		}
	}
	for _, line := range at[len(at)-suffix:] {
		if err := d.line("  ", line); err != nil {
			return "", err
		}
	}
	return d.String(), nil
}

func splitLines(s string) []string {
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func fmtHelp() {
	fmt.Print(`usage: drang fmt [flags] [path...]

Reformats drang source canonically (comments preserved). With no paths it reads
stdin and writes the formatted source to stdout.

Flags:
  -w, --write    rewrite each file in place (atomically); requires paths
  -c, --check    list unformatted files to stderr and exit non-zero (CI gate)
  -l, --list     list files that would change to stdout
  -d, --diff     print a diff of the changes
      --fix      also apply migration rewrites (drang's edition mechanism)
  -h, --help     print this help

Paths may be files or directories (directories are searched for *.dr files).
With paths and no flags, the formatted source is written to stdout. Output is
re-verified before writing: a parse error or a dropped comment aborts that file.
`)
}
