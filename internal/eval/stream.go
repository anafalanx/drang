package eval

import (
	"bufio"
	"fmt"
	"io"
	"os"
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
	seedArgv(env, argv)
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
			fmt.Fprintln(out, streamText(cur))
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
				var buf strings.Builder
				out = &buf // capture this file's -p output
				err = scan(f, fn)
				f.Close()
				out = realOut // restore before the next file / END
				if err != nil {
					return err
				}
				if err := commitInPlace(fn, opts.BackupSuffix, buf.String()); err != nil {
					return err
				}
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

// commitInPlace writes the transformed content back to fn (atomic temp + rename),
// first copying the original to fn+suffix when suffix is non-empty. Used by -i.
func commitInPlace(fn, suffix, content string) error {
	if suffix != "" {
		orig, err := os.ReadFile(fn)
		if err != nil {
			return fmt.Errorf("cannot read %s for backup: %v", fn, err)
		}
		if err := atomicWriteFile(fn+suffix, orig); err != nil {
			return fmt.Errorf("cannot write backup %s: %v", fn+suffix, err)
		}
	}
	if err := atomicWriteFile(fn, []byte(content)); err != nil {
		return fmt.Errorf("cannot write %s: %v", fn, err)
	}
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
func streamText(v value.Value) string {
	if v.Tag() == value.Str {
		return v.AsStr()
	}
	return v.Display()
}
