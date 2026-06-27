// Command drang is the drang interpreter CLI.
//
// Usage: drang [--run|--ast|--tokens] (-e '<source>' | <file.dr>) [args...]
// Leading flags are consumed up to the first non-flag token (the program);
// everything after the program is exposed to the script as $ARGV. By default it
// runs the program; --ast prints the parsed AST and --tokens the token stream.
// --version and --help print and exit. `drang build <file.dr> [-o out]` compiles
// a script into a standalone executable (the drang binary with the source
// appended); such an executable runs its embedded program directly.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/anafalanx/drang/internal/eval"
	"github.com/anafalanx/drang/internal/lexer"
	"github.com/anafalanx/drang/internal/parser"
	"github.com/anafalanx/drang/internal/token"
	"github.com/anafalanx/drang/internal/value"
)

// version is the release string. Declared as a var so a build can stamp it via
// -ldflags "-X main.version=...".
var version = "0.2"

func main() {
	// A standalone executable (made by `drang build`) carries its program appended
	// to this binary; run it directly, with every argument going to the script as
	// $ARGV. A plain drang binary has no such payload and continues to the CLI.
	if src, name, found, err := embeddedProgram(); found {
		if err != nil {
			fmt.Fprintln(os.Stderr, "drang: corrupt standalone payload:", err)
			os.Exit(1)
		}
		origin := name // errors name the original script (zdr.dr:line:col)
		if origin == "" {
			origin = standaloneOrigin()
		}
		runProgram(string(src), origin, os.Args[1:], "")
		return
	}
	// `drang build <script.dr> [-o out]` compiles a script into a standalone exe.
	if len(os.Args) > 1 && os.Args[1] == "build" {
		buildStandalone(os.Args[2:])
		return
	}

	mode := "run"
	args := expandOneLinerCluster(os.Args[1:])
	var streamN, streamP, autoSplit bool

	// Consume leading mode flags up to the first non-flag (the program token).
	i := 0
loop:
	for i < len(args) {
		switch args[i] {
		case "--tokens":
			mode = "tokens"
		case "--ast":
			mode = "ast"
		case "--run":
			mode = "run"
		case "-n":
			streamN = true
		case "-p":
			streamP = true
		case "-a":
			autoSplit = true
		case "--repl":
			repl()
			os.Exit(0)
		case "--version", "-V":
			fmt.Println("drang", version)
			os.Exit(0)
		case "--help", "-h":
			help()
			os.Exit(0)
		default:
			break loop
		}
		i++
	}

	rest := args[i:]
	var src, origin string
	var argv []string
	var baseDir string // base directory for relative `use` paths: a file's dir, else cwd
	switch {
	case len(rest) >= 1 && rest[0] == "-e":
		if len(rest) < 2 {
			usage()
		}
		src, origin = rest[1], "<-e>"
		argv = rest[2:]
	case len(rest) >= 1:
		b, err := os.ReadFile(rest[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, "drang:", err)
			os.Exit(1)
		}
		src, origin = string(b), rest[0]
		argv = rest[1:]
		baseDir = filepath.Dir(rest[0])
	default:
		// One-liner mode needs an explicit program: reading stdin as the program would
		// leave nothing to stream over (stdin is the input), so reject it clearly.
		if streamN || streamP {
			fmt.Fprintln(os.Stderr, "drang: -n/-p requires a program (-e '<source>' or a <file.dr>)")
			os.Exit(2)
		}
		// No program given. An interactive terminal gets the REPL (this is also what
		// double-clicking the executable does); piped/redirected stdin is read and run
		// as the program, so `cat foo.dr | drang` works.
		if interactive() {
			if mode != "run" {
				usage()
			}
			repl()
			return
		}
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, "drang:", err)
			os.Exit(1)
		}
		src, origin = string(b), "<stdin>"
	}

	if streamN || streamP {
		runStream(src, origin, argv, streamP, autoSplit)
		return
	}

	switch mode {
	case "tokens":
		dumpTokens(src, origin)
	case "ast":
		dumpAST(src, origin)
	default:
		runProgram(src, origin, argv, baseDir)
	}
}

// expandOneLinerCluster splits a leading combined one-liner flag (e.g. -ne, -ane)
// into separate flags (-n -e, -a -n -e) so the normal flag loop handles them. Only
// the first argument is considered, and only when it is a cluster of the one-liner
// letters n/p/a/e; a trailing e becomes -e (which then consumes the next arg as the
// program source, exactly as a standalone -e would).
func expandOneLinerCluster(args []string) []string {
	if len(args) == 0 {
		return args
	}
	a := args[0]
	if len(a) <= 2 || a[0] != '-' || a[1] == '-' {
		return args // a single flag (-n), a long --flag, or a bare token
	}
	body := a[1:]
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case 'n', 'p', 'a':
		case 'e':
			if i != len(body)-1 {
				return args // -e must be last: it consumes the NEXT arg as the source
			}
		default:
			return args // not purely one-liner letters; leave untouched
		}
	}
	expanded := make([]string, 0, len(body)+len(args)-1)
	for i := 0; i < len(body); i++ {
		expanded = append(expanded, "-"+body[i:i+1])
	}
	return append(expanded, args[1:]...)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: drang [--run|--ast|--tokens] (-e '<source>' | <file.dr>) [args...]")
	fmt.Fprintln(os.Stderr, "       drang -n|-p [-a] (-e '<source>' | <file.dr>) [files...]   (one-liner mode)")
	fmt.Fprintln(os.Stderr, "       drang build <file.dr> [-o <output>] [--runtime <drang-binary>]")
	fmt.Fprintln(os.Stderr, "try 'drang --help' for more information")
	os.Exit(2)
}

// help prints full usage to stdout (for an explicit --help, which exits 0).
func help() {
	fmt.Printf(`drang %s — a small, parallel, Perl-inspired scripting language

Usage:
  drang [options] <file.dr> [args...]
  drang [options] -e '<source>' [args...]
  drang build <file.dr> [-o <output>] [--runtime <drang-binary>]

Commands:
  build          compile a script into a standalone executable (a drang binary with
                 the source appended); the result runs its embedded program.
                 --runtime <path> uses a target-OS/arch drang binary as the base,
                 for cross-platform builds (e.g. a Linux standalone from Windows)

Options:
  -e <source>    run the given source string instead of a file
  -n             one-liner mode: run the program once per input line ($_ = the line)
  -p             like -n, but print $_ after each line (the sed/filter mode)
  -a             autosplit each line on whitespace into the $f array (0-indexed)
  --run          run the program (default)
  --repl         start the interactive REPL (also the default with no program
                 on an interactive terminal)
  --ast          print the parsed AST instead of running
  --tokens       print the token stream instead of running
  --version, -V  print the version and exit
  --help, -h     print this help and exit

With no program on an interactive terminal, drang starts the REPL; with piped
input it runs stdin as the program. Arguments after the program are exposed to
the script as $ARGV; the process environment is available as the $ENV map.

In one-liner mode (-n/-p), the args after the program are input files (stdin if
none), and short flags combine: drang -ne '...', -pe, -ane. Each line sets $_ (the
line), $nr (1-based line number), and $file (the current filename); -a also sets $f.
BEGIN { } and END { } blocks run once, before and after the loop.
`, version)
}

func dumpTokens(src, origin string) {
	fmt.Printf("# tokens of %s\n", origin)
	l := lexer.New(src)
	for {
		t := l.Next()
		fmt.Printf("%-9s %q\tline %d\n", t.Kind, t.Lit, t.Line)
		if t.Kind == token.EOF {
			break
		}
	}
}

func dumpAST(src, origin string) {
	p := parser.New(src)
	prog := p.ParseProgram()
	if reportParseErrors(p, origin) {
		os.Exit(1)
	}
	fmt.Printf("# ast of %s\n", origin)
	fmt.Println(prog.String())
}

func runProgram(src, origin string, argv []string, baseDir string) {
	p := parser.New(src)
	prog := p.ParseProgram()
	if reportParseErrors(p, origin) {
		os.Exit(1)
	}
	env := eval.NewEnv()
	env.SetModuleDir(baseDir)
	if err := eval.RunProgramWithArgs(prog, env, argv); err != nil {
		if code, ok := eval.ExitRequested(err); ok {
			os.Exit(code) // explicit exit()/die(): no error report
		}
		reportRuntimeError(src, origin, err)
		os.Exit(eval.ExitCode(err))
	}
}

// runStream parses src and runs it in one-liner stream mode (-n/-p): once per input
// line, with the post-program args (argv) used as input files (stdin if none).
func runStream(src, origin string, argv []string, autoPrint, autoSplit bool) {
	p := parser.New(src)
	prog := p.ParseProgram()
	if reportParseErrors(p, origin) {
		os.Exit(1)
	}
	opts := eval.StreamOpts{AutoPrint: autoPrint, AutoSplit: autoSplit, Files: argv}
	if err := eval.RunStream(prog, argv, opts); err != nil {
		if code, ok := eval.ExitRequested(err); ok {
			os.Exit(code) // explicit exit()/die(): no error report
		}
		reportRuntimeError(src, origin, err)
		os.Exit(eval.ExitCode(err))
	}
}

// reportRuntimeError prints a runtime error, with the offending source line and a
// caret under the column when the error carries a position.
func reportRuntimeError(src, origin string, err error) {
	fmt.Fprintln(os.Stderr, "drang:", err)
	line, col, ok := eval.ErrorPos(err)
	if !ok {
		return
	}
	fmt.Fprintf(os.Stderr, "  at %s:%d:%d\n", origin, line, col)
	if s := sourceLine(src, line); s != "" {
		fmt.Fprintf(os.Stderr, "    %s\n", s)
		if col >= 1 {
			fmt.Fprintf(os.Stderr, "    %s^\n", strings.Repeat(" ", col-1))
		}
	}
}

func sourceLine(src string, line int) string {
	if line < 1 {
		return ""
	}
	lines := strings.Split(src, "\n")
	if line > len(lines) {
		return ""
	}
	return strings.TrimRight(lines[line-1], "\r")
}

// interactive reports whether stdin is a terminal (vs a pipe or file), which is
// how we tell an interactive session (-> REPL) from `cat foo.dr | drang` (-> run).
func interactive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// repl runs the interactive read-eval-print loop on the standard streams.
func repl() { replLoop(os.Stdin, os.Stdout) }

// replLoop is the REPL over explicit streams (so it is testable). State (variables,
// functions) persists across submissions in one env. A submission that is still
// open (its only parse errors are unexpected-EOF) continues on a "...>" prompt; a
// real parse or runtime error is reported and the buffer is reset. Non-nil results
// are echoed. Prompts, results, and errors all go to out, so an interactive session
// reads as one stream.
func replLoop(in io.Reader, out io.Writer) {
	fmt.Fprintf(out, "drang %s — type 'exit' (or Ctrl+D / Ctrl+Z) to quit\n", version)
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // tolerate long lines
	env := eval.NewREPLEnv()
	var buf strings.Builder
	continued := false
	for {
		if continued {
			fmt.Fprint(out, "  ...> ")
		} else {
			fmt.Fprint(out, "drang> ")
		}
		if !sc.Scan() {
			fmt.Fprintln(out)
			return
		}
		line := sc.Text()
		if !continued {
			switch strings.TrimSpace(line) {
			case "exit", "quit":
				return
			case "":
				continue
			}
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
		src := buf.String()

		p := parser.New(src)
		prog := p.ParseProgram()
		if errs := p.Errors(); len(errs) > 0 {
			if incompleteParse(errs) {
				continued = true // keep reading: the submission is unfinished
				continue
			}
			for _, e := range errs {
				fmt.Fprintln(out, e)
			}
			buf.Reset()
			continued = false
			continue
		}
		buf.Reset()
		continued = false

		v, err := eval.EvalREPL(prog, env)
		if err != nil {
			if code, ok := eval.ExitRequested(err); ok {
				os.Exit(code) // exit()/die() from the REPL ends the session
			}
			replError(out, src, err)
			continue
		}
		if v.Tag() != value.Nil {
			fmt.Fprintln(out, v.Display())
		}
	}
}

// replError prints a REPL runtime error with the offending line and a caret.
func replError(out io.Writer, src string, err error) {
	fmt.Fprintln(out, "error:", err)
	if line, col, ok := eval.ErrorPos(err); ok && col >= 1 {
		if s := sourceLine(src, line); s != "" {
			fmt.Fprintf(out, "  %s\n  %s^\n", s, strings.Repeat(" ", col-1))
		}
	}
}

// incompleteParse reports whether every parse error is an unexpected end of input
// (an unclosed brace/paren or a dangling operator), meaning the REPL should keep
// reading more lines rather than report a syntax error.
func incompleteParse(errs []string) bool {
	for _, e := range errs {
		if !strings.Contains(e, "EOF") {
			return false
		}
	}
	return len(errs) > 0
}

func reportParseErrors(p *parser.Parser, origin string) bool {
	errs := p.Errors()
	if len(errs) == 0 {
		return false
	}
	fmt.Fprintf(os.Stderr, "# parse errors in %s\n", origin)
	for _, e := range errs {
		fmt.Fprintln(os.Stderr, e)
	}
	return true
}
