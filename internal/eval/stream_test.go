package eval

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anafalanx/drang/internal/ast"
	"github.com/anafalanx/drang/internal/parser"
)

// streamOut parses src, runs it in stream mode over input, and returns the -p output.
func streamOut(t *testing.T, src, input string, opts StreamOpts) string {
	t.Helper()
	p := parser.New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse errors in %q: %v", src, errs)
	}
	var out bytes.Buffer
	opts.Stdin = strings.NewReader(input)
	opts.Stdout = &out
	if err := RunStream(prog, nil, opts); err != nil {
		t.Fatalf("RunStream(%q): %v", src, err)
	}
	return out.String()
}

func TestStreamAutoPrint(t *testing.T) {
	got := streamOut(t, `$_ = upper($_)`, "ab\ncd\n", StreamOpts{AutoPrint: true})
	if got != "AB\nCD\n" {
		t.Errorf("got %q, want %q", got, "AB\nCD\n")
	}
}

func TestStreamPlainCopy(t *testing.T) {
	// -p with an empty body is cat.
	got := streamOut(t, ``, "one\ntwo\n", StreamOpts{AutoPrint: true})
	if got != "one\ntwo\n" {
		t.Errorf("got %q, want %q", got, "one\ntwo\n")
	}
}

func TestStreamAutoSplit(t *testing.T) {
	got := streamOut(t, `$_ = $f[1]`, "a b c\nd e f\n", StreamOpts{AutoPrint: true, AutoSplit: true})
	if got != "b\ne\n" {
		t.Errorf("got %q, want %q", got, "b\ne\n")
	}
}

func TestStreamLineNumber(t *testing.T) {
	got := streamOut(t, `$_ = "${nr}:${_}"`, "x\ny\nz\n", StreamOpts{AutoPrint: true})
	if got != "1:x\n2:y\n3:z\n" {
		t.Errorf("got %q, want %q", got, "1:x\n2:y\n3:z\n")
	}
}

func TestStreamBeginPersistsAcrossLines(t *testing.T) {
	// BEGIN seeds an accumulator that survives every line (awk-style global).
	got := streamOut(t, `BEGIN{ $n := 0 } $n = $n + 1; $_ = "${n}"`, "a\nb\nc\n", StreamOpts{AutoPrint: true})
	if got != "1\n2\n3\n" {
		t.Errorf("got %q, want %q", got, "1\n2\n3\n")
	}
}

func TestStreamEndRunsAfterLoop(t *testing.T) {
	// END runs once after the loop with the accumulated value. Observe it via a file
	// write (the path comes in through $ARGV, which RunStream seeds).
	out := filepath.Join(t.TempDir(), "n.txt")
	src := `BEGIN{ $n := 0 } $n = $n + 1; END{ write_file($ARGV[0], "${n}") }`
	p := parser.New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	if err := RunStream(prog, []string{out}, StreamOpts{Stdin: strings.NewReader("a\nb\nc\n")}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "3" {
		t.Errorf("END wrote %q, want %q", string(b), "3")
	}
}

func TestStreamFilesAndFilename(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(a, []byte("1\n2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := parser.New(`$_ = "${file}|${nr}|${_}"`)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	var out bytes.Buffer
	opts := StreamOpts{AutoPrint: true, Files: []string{a, b}, Stdout: &out}
	if err := RunStream(prog, []string{a, b}, opts); err != nil {
		t.Fatal(err)
	}
	// $nr is global across files; $file tracks the current one.
	want := a + "|1|1\n" + a + "|2|2\n" + b + "|3|3\n"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

func TestStreamMissingFileErrors(t *testing.T) {
	p := parser.New(`$_ = $_`)
	prog := p.ParseProgram()
	err := RunStream(prog, nil, StreamOpts{Files: []string{filepath.Join(t.TempDir(), "nope.txt")}})
	if err == nil || !strings.Contains(err.Error(), "cannot open") {
		t.Errorf("want a cannot-open error, got %v", err)
	}
}

func TestParseSpecialBlock(t *testing.T) {
	p := parser.New(`BEGIN { say(1) }  END { say(2) }`)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	if len(prog.Stmts) != 2 {
		t.Fatalf("got %d statements, want 2", len(prog.Stmts))
	}
	sb, ok := prog.Stmts[0].(*ast.SpecialBlock)
	if !ok || sb.Name != "BEGIN" {
		t.Errorf("stmt[0] = %T %v, want BEGIN SpecialBlock", prog.Stmts[0], prog.Stmts[0])
	}
	if sb, ok := prog.Stmts[1].(*ast.SpecialBlock); !ok || sb.Name != "END" {
		t.Errorf("stmt[1] = %T %v, want END SpecialBlock", prog.Stmts[1], prog.Stmts[1])
	}
}

func TestStreamSpecialBlockOutsideStreamErrors(t *testing.T) {
	// Through the production path (compile → fall back to tree-walker → clean error).
	p := parser.New(`BEGIN { say(1) }`)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	err := RunProgramWithArgs(prog, NewEnv(), nil)
	if err == nil || !strings.Contains(err.Error(), "one-liner") {
		t.Errorf("want a one-liner-mode error, got %v", err)
	}
}

func TestParseMapLiteralStillNeedsSeparator(t *testing.T) {
	// The }-terminates-statement leniency is scoped to block-form statements; a map
	// literal value that merely ends in } must still be followed by a separator.
	p := parser.New(`$a := {"k": 1} say("x")`)
	p.ParseProgram()
	if len(p.Errors()) == 0 {
		t.Error("missing separator after a map-literal value should still be a parse error")
	}
}

func TestParseBlockFormTerminatesAtBrace(t *testing.T) {
	// A genuine block-form statement on one line does terminate at its closing }.
	for _, src := range []string{
		`if true { say(1) } say(2)`,
		`BEGIN { say(1) } say(2)`,
		`fn .f() { 1 } .f()`,
	} {
		p := parser.New(src)
		p.ParseProgram()
		if errs := p.Errors(); len(errs) != 0 {
			t.Errorf("%q: a block-form } should terminate the statement, got %v", src, errs)
		}
	}
}

func TestStreamFrozenInjectedVarErrors(t *testing.T) {
	// Freezing an injected stream var in BEGIN must error, not silently corrupt the
	// loop (the per-line define would otherwise be a discarded no-op).
	p := parser.New(`BEGIN{ $nr ::= 999 }`)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	if err := RunStream(prog, nil, StreamOpts{Stdin: strings.NewReader("a\nb\n")}); err == nil {
		t.Error("freezing an injected stream var should error, not silently no-op")
	}
}
