package eval

import (
	"bytes"
	"testing"

	"github.com/anafalanx/drang/internal/parser"
	"github.com/anafalanx/drang/internal/value"
)

func TestWarnGoesToStderr(t *testing.T) {
	p := parser.New(`say("out"); warn("err", 42)`)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	var outBuf, errBuf bytes.Buffer
	oldOut, oldErr := stdout, stderr
	stdout, stderr = &outBuf, &errBuf
	defer func() { stdout, stderr = oldOut, oldErr }()
	if err := RunProgram(prog, NewEnv()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if outBuf.String() != "out\n" {
		t.Errorf("stdout = %q, want %q", outBuf.String(), "out\n")
	}
	if errBuf.String() != "err 42\n" {
		t.Errorf("stderr = %q, want %q", errBuf.String(), "err 42\n")
	}
}

// TestExit checks that exit()/die() unwind to the top with the right code, past
// every construct that handles other signals (functions, loops, //).
func TestExit(t *testing.T) {
	// die() writes to stderr; capture it so the test stays quiet.
	oldErr := stderr
	stderr = &bytes.Buffer{}
	defer func() { stderr = oldErr }()

	cases := []struct {
		name, src string
		code      int
	}{
		{"bare", `exit()`, 0},
		{"code", `exit(2)`, 2},
		{"past-function", `fn f() { exit(3) }; f(); say("unreached")`, 3},
		{"past-loop", `for $i in 1..9 { exit(4) }`, 4},
		{"not-recovered-by-defor", `exit(5) // 99`, 5},
		{"die-is-1", `die("boom")`, 1},
		{"clamp-high", `exit(300)`, 255},
		{"clamp-negative", `exit(-1)`, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := parser.New(c.src)
			prog := p.ParseProgram()
			if errs := p.Errors(); len(errs) > 0 {
				t.Fatalf("parse %q: %v", c.src, errs)
			}
			err := RunProgramVM(prog, NewEnv())
			code, ok := ExitRequested(err)
			if !ok {
				t.Fatalf("%q: want an exit, got err=%v", c.src, err)
			}
			if code != c.code {
				t.Errorf("%q: exit code = %d, want %d", c.src, code, c.code)
			}
		})
	}
}

// TestExitInPmap guards that an exit() inside a pmap worker unwinds to the top
// rather than being downgraded to pmap's result value. Here exit is the SOLE
// failure, so nothing pre-empts it and the outcome is deterministic. (The
// competing-failure race — exit vs another worker's Err value — is inherent to
// fail-loud parallel execution and can't be asserted deterministically; the fix
// guarantees the exit wins whenever its callback runs.)
func TestExitInPmap(t *testing.T) {
	p := parser.New(`pmap([1, 2, 3, 4], |$x| { if $x == 3 { exit(9) }; $x })`)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	if code, ok := ExitRequested(RunProgramVM(prog, NewEnv())); !ok || code != 9 {
		t.Errorf("exit in pmap: code=%d ok=%v, want 9 true", code, ok)
	}
}

// TestParseArgs locks in the flat-map parsing; to_json gives a deterministic,
// order-preserving snapshot of the result.
func TestParseArgs(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"flags", `say(to_json(parse_args(["--verbose", "--debug"])))`, "{\"verbose\":true,\"debug\":true,\"_\":[]}\n"},
		{"eq-value", `say(to_json(parse_args(["--out=r.txt"])))`, "{\"out\":\"r.txt\",\"_\":[]}\n"},
		{"space-value-with-opts", `say(to_json(parse_args(["--out", "r.txt"], ["out"])))`, "{\"out\":\"r.txt\",\"_\":[]}\n"},
		{"space-value-without-opts", `say(to_json(parse_args(["--out", "r.txt"])))`, "{\"out\":true,\"_\":[\"r.txt\"]}\n"},
		{"short", `say(to_json(parse_args(["-v", "-n=2"])))`, "{\"v\":true,\"n\":\"2\",\"_\":[]}\n"},
		{"terminator", `say(to_json(parse_args(["a", "--", "--b", "-c"])))`, "{\"_\":[\"a\",\"--b\",\"-c\"]}\n"},
		{"lone-dash", `say(to_json(parse_args(["-"])))`, "{\"_\":[\"-\"]}\n"},
		{"empty", `say(to_json(parse_args([])))`, "{\"_\":[]}\n"},
		{"mixed", `say(to_json(parse_args(["--verbose", "--out=r", "in1", "in2"])))`, "{\"verbose\":true,\"out\":\"r\",\"_\":[\"in1\",\"in2\"]}\n"},
		{"value-opt-at-end", `say(to_json(parse_args(["--out"], ["out"])))`, "{\"out\":\"\",\"_\":[]}\n"},

		// ergonomic access of the flat result
		{"field-access", `say(parse_args(["--verbose"]).verbose)`, "true\n"},
		{"positional-index", `say(parse_args(["a", "b"])["_"][1])`, "b\n"},

		// "_" is reserved for positionals: a literal --_ is preserved, never dropped
		{"reserved-underscore-eq", `say(to_json(parse_args(["--_=hello", "pos"])))`, "{\"_\":[\"--_=hello\",\"pos\"]}\n"},
		{"reserved-underscore-flag", `say(to_json(parse_args(["--_"])))`, "{\"_\":[\"--_\"]}\n"},
		// a value-option never swallows the -- terminator, but does take a lone - or another option
		{"value-opt-stops-at-terminator", `say(to_json(parse_args(["--out", "--", "p"], ["out"])))`, "{\"out\":\"\",\"_\":[\"p\"]}\n"},
		{"value-opt-takes-lone-dash", `say(to_json(parse_args(["--out", "-"], ["out"])))`, "{\"out\":\"-\",\"_\":[]}\n"},
		{"value-opt-permissive-next-option", `say(to_json(parse_args(["--out", "--verbose"], ["out"])))`, "{\"out\":\"--verbose\",\"_\":[]}\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := run(t, c.src); got != c.want {
				t.Errorf("%s\n got %q\nwant %q", c.src, got, c.want)
			}
		})
	}
}

// TestParseArgsRejectsNonString verifies argv elements must be strings (so a
// negative number can't be coerced into a phantom flag).
func TestParseArgsRejectsNonString(t *testing.T) {
	argv := value.MakeArray([]value.Value{value.MakeInt(-5)})
	if _, err := builtinParseArgs([]value.Value{argv}); err == nil {
		t.Error("parse_args with a non-string argv element should abort")
	}
}

// TestExitWalker confirms the same unwinding on the tree-walker (the fallback).
func TestExitWalker(t *testing.T) {
	oldErr := stderr
	stderr = &bytes.Buffer{}
	defer func() { stderr = oldErr }()
	p := parser.New(`fn f() { exit(7) }; f()`)
	prog := p.ParseProgram()
	if code, ok := ExitRequested(RunProgram(prog, NewEnv())); !ok || code != 7 {
		t.Errorf("walker exit: code=%d ok=%v", code, ok)
	}
}
