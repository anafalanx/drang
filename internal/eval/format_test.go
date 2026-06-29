package eval

import (
	"strings"
	"testing"
)

// The printf habit (%-style verbs) must produce a self-documenting arity error, and only
// then — a plain mismatch or prose containing a bare % must not carry the hint.
func TestFormatPrintfHint(t *testing.T) {
	withHint := []string{
		`say(err_msg(format("%d", 5)))`,
		`say(err_msg(format("%5.2f", 3.14)))`,
		`say(err_msg(format("%-10s", "x")))`,
	}
	for _, src := range withHint {
		got := run(t, src)
		if !strings.Contains(got, "not %-style verbs") {
			t.Errorf("%s\n expected the printf hint, got %q", src, got)
		}
	}

	withoutHint := []string{
		`say(err_msg(format("{} {}", 5)))`,     // genuine too-few-args, no %-verbs
		`say(err_msg(format("100% done", 5)))`, // a bare % in prose, not a verb
	}
	for _, src := range withoutHint {
		got := run(t, src)
		if !strings.Contains(got, "placeholder(s) but got") {
			t.Errorf("%s\n expected the base arity error, got %q", src, got)
		}
		if strings.Contains(got, "%-style") {
			t.Errorf("%s\n should NOT carry the printf hint, got %q", src, got)
		}
	}

	// Correct {} / {:spec} usage is unaffected.
	if got := run(t, `say(format("{} {:.2f}", "pi", 3.14159))`); got != "pi 3.14\n" {
		t.Errorf("brace format: got %q, want %q", got, "pi 3.14\n")
	}
}

// The detector is strict: % then optional flags/width/precision then a KNOWN verb letter.
func TestLooksLikePrintf(t *testing.T) {
	yes := []string{"%d", "%s", "%5.2f", "%-10s", "%x", "%v", "got %d here", "%05d", "a %q b"}
	no := []string{"%%", "100% done", "50% off", "no percent", "a {} b", "{:.2f}", "%", "end%"}
	for _, s := range yes {
		if !looksLikePrintf(s) {
			t.Errorf("looksLikePrintf(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if looksLikePrintf(s) {
			t.Errorf("looksLikePrintf(%q) = true, want false", s)
		}
	}
}
