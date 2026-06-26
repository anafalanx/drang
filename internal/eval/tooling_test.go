package eval

import (
	"bytes"
	"testing"

	"github.com/anafalanx/drang/internal/parser"
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
