package eval

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/anafalanx/drang/internal/ast"
	"github.com/anafalanx/drang/internal/value"
)

var maxGoldenOutputBytes int64 = 64 << 20

type boundedOutputBuffer struct {
	bytes.Buffer
	limit int64
}

func (b *boundedOutputBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - int64(b.Len())
	if remaining <= 0 {
		return 0, fmt.Errorf("test output exceeds the %d-byte limit", b.limit)
	}
	if int64(len(p)) <= remaining {
		return b.Buffer.Write(p)
	}
	n, _ := b.Buffer.Write(p[:remaining])
	return n, fmt.Errorf("test output exceeds the %d-byte limit", b.limit)
}

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
	env.SetScriptPath(origin)
	var requestedExit error

	// For a golden check (or --update) capture the program's stdout; otherwise let it
	// pass through. Restore the real stdout before the example checks run.
	capture := goldenPath != "" || update
	buf := boundedOutputBuffer{limit: maxGoldenOutputBytes}
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
		// tests; retain it for the CLI while checking whatever was defined. Other
		// errors are fatal.
		if _, isExit := ExitRequested(loadErr); !isExit {
			return 0, 0, loadErr
		}
		requestedExit = loadErr
		loadErr = nil
	}

	switch {
	case update && goldenPath != "":
		if err := os.WriteFile(goldenPath, buf.Bytes(), 0o644); err != nil {
			return pass, fail, err
		}
		if _, err := fmt.Fprintf(w, "  updated %s\n", goldenPath); err != nil {
			return pass, fail, err
		}
	case goldenPath != "":
		expected, err := readFileBounded(goldenPath, maxGoldenOutputBytes, "golden output")
		if err != nil {
			return pass, fail, err
		}
		if d := goldenDiff(string(expected), buf.String()); d == "" {
			pass++
		} else {
			fail++
			if _, err := fmt.Fprintf(w, "  FAIL %s — stdout differs from %s\n%s", origin, goldenPath, d); err != nil {
				return pass, fail, err
			}
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
			if _, err := fmt.Fprintf(w, "  FAIL %s:%d  %s\n        %s\n", origin, line, ex.String(), detail); err != nil {
				return pass, fail, err
			}
		}
	}
	return pass, fail, requestedExit
}

// goldenDiff returns "" if expected == actual, else a compact diff: it trims the
// common prefix/suffix of lines and shows only the differing middle as -expected /
// +actual (truncated if very large).
func goldenDiff(expected, actual string) string {
	return goldenDiffLimit(expected, actual, 20)
}

type textLineCursor struct {
	src  string
	pos  int
	done bool
}

func (c *textLineCursor) next() (string, bool) {
	if c.done {
		return "", false
	}
	if relEnd := strings.IndexByte(c.src[c.pos:], '\n'); relEnd >= 0 {
		end := c.pos + relEnd
		line := c.src[c.pos:end]
		c.pos = end + 1
		return line, true
	}
	line := c.src[c.pos:]
	c.pos = len(c.src)
	c.done = true
	return line, true
}

func (c textLineCursor) remaining() int {
	if c.done {
		return 0
	}
	return strings.Count(c.src[c.pos:], "\n") + 1
}

func previousTextLine(src string, lower, end int) (line string, nextEnd int, ok bool) {
	if end < lower {
		return "", end, false
	}
	if relStart := strings.LastIndexByte(src[lower:end], '\n'); relStart >= 0 {
		start := lower + relStart + 1
		return src[start:end], start - 1, true
	}
	return src[lower:end], lower - 1, true
}

func goldenDiffLimit(expected, actual string, maxPerSide int) string {
	if expected == actual {
		return ""
	}
	exp := textLineCursor{src: expected}
	act := textLineCursor{src: actual}
	prefix := 0
	for !exp.done && !act.done {
		expBefore, actBefore := exp, act
		expLine, _ := exp.next()
		actLine, _ := act.next()
		if expLine != actLine {
			exp, act = expBefore, actBefore
			break
		}
		prefix++
	}

	expRemaining, actRemaining := exp.remaining(), act.remaining()
	expEnd, actEnd := len(expected), len(actual)
	suffix := 0
	for suffix < expRemaining && suffix < actRemaining {
		expLine, nextExpEnd, _ := previousTextLine(expected, exp.pos, expEnd)
		actLine, nextActEnd, _ := previousTextLine(actual, act.pos, actEnd)
		if expLine != actLine {
			break
		}
		suffix++
		expEnd, actEnd = nextExpEnd, nextActEnd
	}
	expDifferent := expRemaining - suffix
	actDifferent := actRemaining - suffix

	var b strings.Builder
	fmt.Fprintf(&b, "        @@ first difference at line %d @@\n", prefix+1)
	for i := 0; i < expDifferent && i < maxPerSide; i++ {
		line, _ := exp.next()
		fmt.Fprintf(&b, "        - %s\n", line)
	}
	if expDifferent > maxPerSide {
		fmt.Fprintf(&b, "        … (%d more expected lines)\n", expDifferent-maxPerSide)
	}
	for i := 0; i < actDifferent && i < maxPerSide; i++ {
		line, _ := act.next()
		fmt.Fprintf(&b, "        + %s\n", line)
	}
	if actDifferent > maxPerSide {
		fmt.Fprintf(&b, "        … (%d more actual lines)\n", actDifferent-maxPerSide)
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
	var (
		s  string
		ok bool
	)
	if v.Tag() == value.Str {
		s, ok = quotedWithin(v.AsStr(), maxStringBytes)
	} else {
		s, ok = displayWithin(v, maxStringBytes)
	}
	if ok {
		return s
	}
	return truncatedDiagnostic(s, maxStringBytes)
}
