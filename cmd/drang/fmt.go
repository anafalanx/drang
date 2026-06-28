package main

import (
	"fmt"
	"io"
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
	var write, check, list, diff bool
	var paths []string
	for _, a := range args {
		switch a {
		case "-w", "--write":
			write = true
		case "-c", "--check":
			check = true
		case "-l", "--list":
			list = true
		case "-d", "--diff":
			diff = true
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
	if write && check {
		fmt.Fprintln(os.Stderr, "drang fmt: -w and --check are mutually exclusive")
		os.Exit(2)
	}
	if write && len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "drang fmt: -w needs file or directory paths (cannot rewrite stdin)")
		os.Exit(2)
	}

	if len(paths) == 0 {
		fmtStdin(check || list || diff, diff)
		return
	}

	anyChanged, anyErr := false, false
	for _, f := range expandFmtPaths(paths) {
		src, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "drang fmt: %v\n", err)
			anyErr = true
			continue
		}
		out, ferr := printer.Format(string(src))
		if ferr != nil {
			fmt.Fprintf(os.Stderr, "drang fmt: %s: %v\n", f, ferr)
			anyErr = true
			continue
		}
		changed := out != string(src)
		switch {
		case write:
			if changed {
				if werr := writeFileAtomic(f, out); werr != nil {
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
				os.Stdout.WriteString(unifiedDiff(f, string(src), out))
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
func fmtStdin(report, diff bool) {
	src, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "drang fmt:", err)
		os.Exit(2)
	}
	out, ferr := printer.Format(string(src))
	if ferr != nil {
		fmt.Fprintln(os.Stderr, "drang fmt: <stdin>:", ferr)
		os.Exit(1)
	}
	if report {
		changed := out != string(src)
		if diff && changed {
			os.Stdout.WriteString(unifiedDiff("<stdin>", string(src), out))
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
func expandFmtPaths(paths []string) []string {
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
		filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
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
		})
	}
	return out
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
	cerr := tmp.Close()
	if werr != nil {
		os.Remove(name)
		return werr
	}
	if cerr != nil {
		os.Remove(name)
		return cerr
	}
	if fi, e := os.Stat(path); e == nil {
		os.Chmod(name, fi.Mode())
	}
	if rerr := os.Rename(name, path); rerr != nil {
		os.Remove(name)
		return rerr
	}
	return nil
}

// unifiedDiff returns a line-based diff of a vs b (every line annotated: "  " context,
// "-" removed, "+" added), using an LCS to align them. Empty when a == b.
func unifiedDiff(name, a, b string) string {
	at, bt := splitLines(a), splitLines(b)
	n, m := len(at), len(bt)
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
	var d strings.Builder
	fmt.Fprintf(&d, "--- %s (original)\n+++ %s (formatted)\n", name, name)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case at[i] == bt[j]:
			d.WriteString("  " + at[i] + "\n")
			i, j = i+1, j+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			d.WriteString("-" + at[i] + "\n")
			i++
		default:
			d.WriteString("+" + bt[j] + "\n")
			j++
		}
	}
	for ; i < n; i++ {
		d.WriteString("-" + at[i] + "\n")
	}
	for ; j < m; j++ {
		d.WriteString("+" + bt[j] + "\n")
	}
	return d.String()
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
  -h, --help     print this help

Paths may be files or directories (directories are searched for *.dr files).
With paths and no flags, the formatted source is written to stdout. Output is
re-verified before writing: a parse error or a dropped comment aborts that file.
`)
}
