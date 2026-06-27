package eval

import (
	"fmt"
	"io"
	"strconv"

	"github.com/anafalanx/drang/internal/ast"
	"github.com/anafalanx/drang/internal/value"
)

// RunExamples runs prog (executing its top-level so all definitions exist), then
// evaluates each top-level `example` assertion against the resulting env, writing one
// block per failure to w. baseDir resolves the program's relative `use` paths; origin
// labels the source in the report. It returns (passed, failed, loadErr); loadErr is
// non-nil only if the program itself failed to run — not for an assertion failure.
func RunExamples(prog *ast.Program, baseDir, origin string, w io.Writer) (pass, fail int, loadErr error) {
	env := NewEnv()
	env.SetModuleDir(baseDir)
	if err := RunProgramWithArgs(prog, env, nil); err != nil {
		// A top-level exit()/die() ends setup early but must not silently mask the
		// examples; check them against whatever was defined (a missing later
		// definition just makes its example fail).
		if _, isExit := ExitRequested(err); !isExit {
			return 0, 0, err
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
	return pass, fail, nil
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
