package eval

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/anafalanx/drang/internal/ast"
	"github.com/anafalanx/drang/internal/value"
)

// RunExamples runs prog (executing its top-level so all definitions exist), then
// checks it two ways: a file-level golden-output check (when goldenPath is set or
// update is true — the program's captured stdout is compared to, or written to, that
// file), and the top-level `example` assertions. It writes one block per failure to w.
// baseDir resolves relative `use` paths; origin labels the source. It returns
// (passed, failed, loadErr); loadErr is non-nil only if the program itself failed to
// run (or a golden file could not be read/written), not for a test failure.
func RunExamples(prog *ast.Program, baseDir, origin, goldenPath string, update bool, w io.Writer) (pass, fail int, loadErr error) {
	env := NewEnv()
	env.SetModuleDir(baseDir)

	// For a golden check (or --update) capture the program's stdout; otherwise let it
	// pass through. Restore the real stdout before the example checks run.
	capture := goldenPath != "" || update
	var buf bytes.Buffer
	if capture {
		// Swap under outMu (via swapStdout) so capture can't race a say from a
		// still-running spawned task; the restore also fences any in-flight write.
		old := swapStdout(&buf)
		loadErr = RunProgramWithArgs(prog, env, nil)
		swapStdout(old)
	} else {
		loadErr = RunProgramWithArgs(prog, env, nil)
	}
	if loadErr != nil {
		// A top-level exit()/die() ends setup early but must not silently mask the
		// tests; tolerate it and check whatever was defined. Other errors are fatal.
		if _, isExit := ExitRequested(loadErr); !isExit {
			return 0, 0, loadErr
		}
		loadErr = nil
	}

	switch {
	case update && goldenPath != "":
		if err := os.WriteFile(goldenPath, buf.Bytes(), 0o644); err != nil {
			return pass, fail, err
		}
		fmt.Fprintf(w, "  updated %s\n", goldenPath)
	case goldenPath != "":
		expected, err := os.ReadFile(goldenPath)
		if err != nil {
			return pass, fail, err
		}
		if d := goldenDiff(string(expected), buf.String()); d == "" {
			pass++
		} else {
			fail++
			fmt.Fprintf(w, "  FAIL %s — stdout differs from %s\n%s", origin, goldenPath, d)
		}
	}

	for _, st := range prog.Stmts {
		ex, ok := st.(*ast.ExampleStmt)
		if !ok {
			continue
		}
		if good, detail := checkExample(ex, env); good {
			pass++
		} else {
			fail++
			line, _ := ex.Loc()
			fmt.Fprintf(w, "  FAIL %s:%d  %s\n        %s\n", origin, line, ex.String(), detail)
		}
	}
	return pass, fail, loadErr
}

// goldenDiff returns "" if expected == actual, else a compact diff: it trims the
// common prefix/suffix of lines and shows only the differing middle as -expected /
// +actual (truncated if very large).
func goldenDiff(expected, actual string) string {
	if expected == actual {
		return ""
	}
	exp := strings.Split(expected, "\n")
	act := strings.Split(actual, "\n")
	p := 0
	for p < len(exp) && p < len(act) && exp[p] == act[p] {
		p++
	}
	s := 0
	for s < len(exp)-p && s < len(act)-p && exp[len(exp)-1-s] == act[len(act)-1-s] {
		s++
	}
	var b strings.Builder
	fmt.Fprintf(&b, "        @@ first difference at line %d @@\n", p+1)
	const maxPerSide = 20 // each side gets its own budget, so neither is hidden
	expLines, actLines := exp[p:len(exp)-s], act[p:len(act)-s]
	for i, ln := range expLines {
		if i >= maxPerSide {
			fmt.Fprintf(&b, "        … (%d more expected lines)\n", len(expLines)-i)
			break
		}
		fmt.Fprintf(&b, "        - %s\n", ln)
	}
	for i, ln := range actLines {
		if i >= maxPerSide {
			fmt.Fprintf(&b, "        … (%d more actual lines)\n", len(actLines)-i)
			break
		}
		fmt.Fprintf(&b, "        + %s\n", ln)
	}
	return b.String()
}

// checkExample evaluates one assertion, returning whether it passed and, if not, a
// short explanation. The example expressions run here (in the check phase), not during
// the program run, where they are no-ops.
func checkExample(ex *ast.ExampleStmt, env *Env) (bool, string) {
	got, err := evalExpr(ex.Subject, env)
	// exit()/die() are non-catchable aborts, not failures — an example must not
	// swallow one (which would otherwise read as a passing `fails`).
	if err != nil {
		if _, isExit := ExitRequested(err); isExit {
			return false, "the example called exit()/die() (a non-catchable abort)"
		}
	}
	if ex.Fails {
		if err != nil || got.IsErr() {
			return true, ""
		}
		return false, fmt.Sprintf("expected an error, but got %s", describe(got))
	}
	if err != nil {
		return false, "unexpected error: " + err.Error()
	}
	if got.IsErr() {
		return false, "unexpected error: " + got.ErrMsg()
	}
	if ex.Want == nil { // truthy form
		if got.Truthy() {
			return true, ""
		}
		return false, fmt.Sprintf("expected a truthy value, got %s", describe(got))
	}
	want, werr := evalExpr(ex.Want, env)
	if werr != nil {
		if _, isExit := ExitRequested(werr); isExit {
			return false, "the example called exit()/die() (a non-catchable abort)"
		}
		return false, "error evaluating the expected value: " + werr.Error()
	}
	if value.Equal(got, want) {
		return true, ""
	}
	return false, fmt.Sprintf("expected %s, got %s", describe(want), describe(got))
}

// describe renders a value for a failure message, quoting strings so a type mismatch
// (e.g. "5" vs 5) and empty/whitespace values are visible.
func describe(v value.Value) string {
	if v.Tag() == value.Str {
		return strconv.Quote(v.AsStr())
	}
	return v.Display()
}
