package eval

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/anafalanx/drang/internal/ast"
	"github.com/anafalanx/drang/internal/value"
)

// StreamOpts configures one-liner stream mode (the awk/perl -n/-p loop).
type StreamOpts struct {
	AutoPrint    bool      // -p: print $_ after each line (with a newline)
	AutoSplit    bool      // -a: split each line on whitespace into $f
	InPlace      bool      // -i: write the -p output back to each input file instead of stdout
	BackupSuffix string    // -i<suffix>: save the original to <file><suffix> before overwriting (e.g. ".bak")
	Files        []string  // input files; empty means read Stdin
	Stdin        io.Reader // input when Files is empty (defaults to os.Stdin)
	Stdout       io.Writer // where -p writes (defaults to os.Stdout)
	ModuleDir    string    // base directory for relative use paths
	ScriptPath   string    // running script path for file-derived defaults such as store()
}

// RunStream runs prog once per input line in awk/perl -n/-p style. BEGIN { } and
// END { } blocks are hoisted out of the per-line loop and run once, before and
// after; the remaining statements run once per line against a persistent top scope,
// so accumulators declared in BEGIN survive across lines (awk-style globals). Each
// iteration injects $_ (the line, with its trailing newline stripped), $nr (the
// 1-based line number across all input), $file (the current filename), and, with
// AutoSplit, $f (the whitespace-split fields, 0-indexed).
//
// The per-line body is tree-walked (the reference backend), which keeps the
// implementation simple and correct; compiling it once and running it on the VM per
// line is a possible future optimization.
func RunStream(prog *ast.Program, argv []string, opts StreamOpts) error {
	realOut := opts.Stdout
	if realOut == nil {
		realOut = os.Stdout
	}
	out := realOut // runLine reads this each call; -i retargets it per file
	env := NewEnv()
	env.SetModuleDir(opts.ModuleDir)
	env.SetScriptPath(opts.ScriptPath)
	seedArgv(env, argv)
	ctx := env.executionContext()
	owned := ctx.beginRun()
	defer ctx.endRun(owned)
	if err := RunPrelude(env); err != nil {
		return err
	}

	// Hoist BEGIN/END out of the per-line body.
	var begin, body, end []ast.Stmt
	for _, s := range prog.Stmts {
		if sb, ok := s.(*ast.SpecialBlock); ok {
			if sb.Name == "END" {
				end = append(end, sb.Body.Stmts...)
			} else {
				begin = append(begin, sb.Body.Stmts...)
			}
			continue
		}
		body = append(body, s)
	}

	if err := evalStmts(begin, env); err != nil {
		return err
	}

	nr := int64(0)
	runLine := func(line, fname string) error {
		nr++
		// Each input line is its own "run" for the runaway-recursion budget: an
		// awk-style job over millions of lines that legitimately catches a deep
		// overflow per line must never accumulate into a spurious storm abort.
		env.resetOverflowBudget()
		// Inject the per-line variables. A define error means the user froze one of
		// these in BEGIN (e.g. $nr ::= ...); surface it rather than silently running
		// the loop on stale values.
		inject := []struct {
			name string
			v    value.Value
		}{
			{"_", value.MakeStr(line)},
			{"nr", value.MakeInt(nr)},
			{"file", value.MakeStr(fname)},
		}
		if opts.AutoSplit {
			if countFieldsOver(line, maxCollectionItems) {
				return fmt.Errorf("auto-split result exceeds the %d-element collection limit", maxCollectionItems)
			}
			fields := strings.Fields(line)
			fv := make([]value.Value, len(fields))
			for i, f := range fields {
				fv[i] = value.MakeStr(f)
			}
			inject = append(inject, struct {
				name string
				v    value.Value
			}{"f", value.MakeArray(fv)})
		}
		for _, kv := range inject {
			if err := env.define(kv.name, kv.v, false); err != nil {
				return err
			}
		}
		if err := evalStmts(body, env); err != nil {
			return err
		}
		if opts.AutoPrint {
			cur, _ := env.get("_")
			text, err := streamText(cur)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintln(out, text); err != nil {
				return fmt.Errorf("writing stream output: %w", err)
			}
		}
		return nil
	}

	scan := func(r io.Reader, fname string) error {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // tolerate long lines
		for sc.Scan() {
			if err := runLine(sc.Text(), fname); err != nil {
				return err
			}
		}
		if err := sc.Err(); err != nil {
			return fmt.Errorf("reading %s: %v", fname, err)
		}
		return nil
	}

	stdin := opts.Stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	if len(opts.Files) == 0 {
		if err := scan(stdin, "<stdin>"); err != nil {
			return err
		}
	} else {
		for _, fn := range opts.Files {
			if fn == "-" { // "-" means stdin, the awk/perl convention
				if opts.InPlace {
					return fmt.Errorf("-i cannot edit stdin (\"-\") in place")
				}
				if err := scan(stdin, "<stdin>"); err != nil {
					return err
				}
				continue
			}
			f, err := os.Open(fn)
			if err != nil {
				return fmt.Errorf("cannot open %s: %v", fn, err)
			}
			if opts.InPlace {
				tmp, err := os.CreateTemp(filepath.Dir(fn), ".drang-stream-*")
				if err != nil {
					f.Close()
					return fmt.Errorf("cannot create temporary output for %s: %w", fn, err)
				}
				tmpName := tmp.Name()
				committed := false
				defer func() {
					if !committed {
						os.Remove(tmpName)
					}
				}()
				out = tmp // stream transformed output instead of retaining the whole file
				err = scan(f, fn)
				closeInputErr := f.Close()
				out = realOut // restore before the next file / END
				if err != nil {
					tmp.Close()
					return err
				}
				if closeInputErr != nil {
					tmp.Close()
					return fmt.Errorf("closing %s: %w", fn, closeInputErr)
				}
				if err := tmp.Sync(); err != nil {
					tmp.Close()
					return fmt.Errorf("syncing temporary output for %s: %w", fn, err)
				}
				if err := tmp.Close(); err != nil {
					return fmt.Errorf("closing temporary output for %s: %w", fn, err)
				}
				if err := commitInPlaceTemp(fn, opts.BackupSuffix, tmpName); err != nil {
					return err
				}
				committed = true
			} else {
				err = scan(f, fn)
				f.Close()
				if err != nil {
					return err
				}
			}
		}
	}

	return evalStmts(end, env)
}

// commitInPlaceTemp atomically promotes an already-synced same-directory temp
// file. A requested backup is copied through its own temp file, so neither the
// transformed file nor the backup is retained in memory or left partial.
func commitInPlaceTemp(fn, suffix, tmpName string) error {
	if suffix != "" {
		if err := copyFileAtomic(fn, fn+suffix); err != nil {
			return fmt.Errorf("cannot write backup %s: %v", fn+suffix, err)
		}
	}
	if fi, err := os.Stat(fn); err == nil {
		if err := os.Chmod(tmpName, fi.Mode()); err != nil {
			return fmt.Errorf("cannot preserve mode of %s: %w", fn, err)
		}
	}
	if err := os.Rename(tmpName, fn); err != nil {
		return fmt.Errorf("cannot write %s: %v", fn, err)
	}
	return nil
}

func copyFileAtomic(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".drang-backup-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		tmp.Close()
		if !committed {
			os.Remove(tmpName)
		}
	}()
	if _, err := io.Copy(tmp, in); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if fi, err := os.Stat(src); err == nil {
		if err := os.Chmod(tmpName, fi.Mode()); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return err
	}
	committed = true
	return nil
}

// evalStmts tree-walks a statement list against env (the persistent stream scope).
func evalStmts(stmts []ast.Stmt, env *Env) error {
	for _, s := range stmts {
		if _, err := evalStmt(s, env); err != nil {
			return err
		}
	}
	return nil
}

// streamText renders $_ for -p output: a string prints verbatim, anything else via
// its Display form (so a body that sets $_ to a number still prints sensibly).
func streamText(v value.Value) (string, error) {
	s, ok := displayWithin(v, maxStringBytes)
	if !ok {
		return "", fmt.Errorf("stream output exceeds the %d-byte string limit", maxStringBytes)
	}
	return s, nil
}
