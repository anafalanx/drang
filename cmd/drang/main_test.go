package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestREPL drives the REPL loop over a scripted session and checks that state
// persists across lines, multi-line submissions work, results are echoed, and a
// parse error does not kill the loop.
func TestREPL(t *testing.T) {
	script := strings.Join([]string{
		`1 + 2`,          // expression -> 3
		`$x := 10`,       // declaration persists, echoes 10
		`$x * 5`,         // reads persisted $x -> 50
		`fn .sq($n) {`,   // multi-line: continues on "...>"
		`$n * $n`,        //
		`}`,              // function persists
		`.sq(9)`,         // -> 81 (runs the defined function)
		`$"v=$x"`,        // interpolation + persisted var -> v=10
		`@@@`,            // garbage -> parse error; loop must recover
		`100 + 1`,        // -> 101 proves recovery
		`fn .noop() { }`, // declaration -> nil, must NOT echo a value line
		`exit`,
	}, "\n") + "\n"

	var out strings.Builder
	replLoop(strings.NewReader(script), &out)
	got := out.String()

	for _, want := range []string{"50", "81", "v=10", "101"} {
		if !strings.Contains(got, want) {
			t.Errorf("REPL output missing %q\n--- full output ---\n%s", want, got)
		}
	}
	if strings.Contains(got, "error") && !strings.Contains(got, "101") {
		t.Errorf("REPL did not recover after a parse error\n%s", got)
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestREPLReportsInputFailure(t *testing.T) {
	var out strings.Builder
	replLoop(failingReader{err: errors.New("device failed")}, &out)
	if got := out.String(); !strings.Contains(got, "error reading input: device failed") {
		t.Fatalf("REPL silently swallowed input failure:\n%s", got)
	}
}

func TestTestRunExitCodePrecedence(t *testing.T) {
	tests := []struct {
		name          string
		fileErr       bool
		fail          int
		exitRequested bool
		exitCode      int
		want          int
	}{
		{name: "success", want: 0},
		{name: "failed example", fail: 1, want: 1},
		{name: "exit zero does not mask failure", fail: 1, exitRequested: true, want: 1},
		{name: "nonzero exit beats failure", fail: 1, exitRequested: true, exitCode: 7, want: 7},
		{name: "infrastructure error beats exit", fileErr: true, exitRequested: true, exitCode: 7, want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := testRunExitCode(tt.fileErr, tt.fail, tt.exitRequested, tt.exitCode); got != tt.want {
				t.Fatalf("exit code = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestTestRunKeepsFirstNonzeroExplicitExit(t *testing.T) {
	code := 0
	for _, next := range []int{0, 7, 3, 0} {
		code = firstNonzeroTestExit(code, next)
	}
	if code != 7 {
		t.Fatalf("aggregate explicit exit = %d, want first nonzero code 7", code)
	}
}

func TestRunTestsExplicitExitIntegration(t *testing.T) {
	if paths := os.Getenv("DRANG_TEST_RUN_PATHS"); paths != "" {
		runTests(filepath.SplitList(paths))
		return
	}
	dir := t.TempDir()
	writeTest := func(name, src string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	exitZeroFailure := writeTest("exit-zero-failure.dr", "example false\nexit(0)")
	exitSeven := writeTest("exit-seven.dr", "example true\nexit(7)")
	exitThree := writeTest("exit-three.dr", "example true\nexit(3)")
	tests := []struct {
		name  string
		paths []string
		code  int
		want  string
	}{
		{name: "exit zero preserves failed assertion", paths: []string{exitZeroFailure}, code: 1, want: "0 passed, 1 failed"},
		{name: "nonzero exit determines status", paths: []string{exitSeven}, code: 7, want: "1 passed, 0 failed"},
		{name: "first nonzero across files", paths: []string{exitSeven, exitThree}, code: 7, want: "total: 2 passed, 0 failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestRunTestsExplicitExitIntegration$")
			cmd.Env = append(os.Environ(), "DRANG_TEST_RUN_PATHS="+strings.Join(tt.paths, string(os.PathListSeparator)))
			out, err := cmd.CombinedOutput()
			if got := cmd.ProcessState.ExitCode(); got != tt.code {
				t.Fatalf("exit code = %d, want %d (err %v)\n%s", got, tt.code, err, out)
			}
			if !strings.Contains(string(out), tt.want) {
				t.Fatalf("output missing %q:\n%s", tt.want, out)
			}
		})
	}
}

func TestSourceLineScansNewlineHeavyInput(t *testing.T) {
	src := strings.Repeat("\n", 4096) + "needle\r\r\nlast"
	if got := sourceLine(src, 4097); got != "needle" {
		t.Fatalf("source line 4097 = %q, want needle", got)
	}
	if got := sourceLine(src, 4098); got != "last" {
		t.Fatalf("source line 4098 = %q, want last", got)
	}
	if got := sourceLine(src, 4099); got != "" {
		t.Fatalf("out-of-range source line = %q, want empty", got)
	}
}

var _ io.Reader = failingReader{}
