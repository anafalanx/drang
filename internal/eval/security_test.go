package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCapWriterCaps: the capture-buffer cap stores up to limit bytes, always reports a full
// write (so the child's copier never blocks), flags overflow once, and stores nothing past it.
func TestCapWriterCaps(t *testing.T) {
	w := capWriter{limit: 10}
	if n, _ := w.Write([]byte("12345")); n != 5 {
		t.Fatalf("Write returned %d, want 5", n)
	}
	if w.overflowed() {
		t.Fatal("overflow flagged before the limit was crossed")
	}
	if n, _ := w.Write([]byte("67890ABC")); n != 8 { // 8 offered, only 5 fit
		t.Fatalf("Write returned %d, want 8 (always claims a full write)", n)
	}
	if !w.overflowed() {
		t.Error("overflow not flagged after crossing the limit")
	}
	if got := w.String(); got != "1234567890" {
		t.Errorf("buffered %q, want the first 10 bytes %q", got, "1234567890")
	}
	w.Write([]byte("more")) // past the cap: dropped
	if w.String() != "1234567890" {
		t.Error("bytes were stored past the cap")
	}
}

// TestMatchSegsCorrect pins ** semantics (spans zero or more segments) across the memoized
// matcher, including adjacent-** collapse.
func TestMatchSegsCorrect(t *testing.T) {
	cases := []struct {
		ps, ns []string
		want   bool
	}{
		{[]string{"**"}, []string{"a", "b"}, true},
		{[]string{"**"}, nil, true},
		{[]string{"a", "**", "c"}, []string{"a", "b1", "b2", "c"}, true},
		{[]string{"a", "**", "c"}, []string{"a", "c"}, true}, // ** spans zero segments
		{[]string{"a", "**", "c"}, []string{"a", "b", "d"}, false},
		{[]string{"a", "**", "b", "**", "c"}, []string{"a", "x", "b", "y", "z", "c"}, true},
		{[]string{"**", "**", "x"}, []string{"a", "x"}, true}, // adjacent ** collapse
		{[]string{"*.go"}, []string{"main.go"}, true},
		{[]string{"a"}, []string{"a", "b"}, false},
	}
	for i, c := range cases {
		if got := matchSegs(c.ps, c.ns); got != c.want {
			t.Errorf("case %d: matchSegs(%v, %v) = %v, want %v", i, c.ps, c.ns, got, c.want)
		}
	}
}

// TestMatchSegsBoundedNoHang is the regression for the exponential-glob hang: a pattern with
// many non-adjacent ** against a deep path backtracked ~2^k ways before memoization. It must
// now return promptly (a revert would hang the whole test binary).
func TestMatchSegsBoundedNoHang(t *testing.T) {
	var ps []string
	for i := 0; i < 15; i++ {
		ps = append(ps, "a", "**")
	}
	ps = append(ps, "z") // the path has no 'z', so the answer is false — but must be reached fast
	ns := make([]string, 40)
	for i := range ns {
		ns[i] = "a"
	}
	if matchSegs(ps, ns) {
		t.Error("expected no match (the path contains no 'z')")
	}
}

// TestReCacheBounded: with the compile cache full, further distinct patterns still compile
// correctly (just uncached) and the size counter is never pushed past the cap.
func TestReCacheBounded(t *testing.T) {
	saved := reCacheSize.Swap(maxReCache)
	defer reCacheSize.Store(saved)
	re, err := compilePattern(`sec2_(uniq|cap)+\d`)
	if err != nil || re == nil {
		t.Fatalf("compile while cache full failed: re=%v err=%v", re, err)
	}
	if !re.MatchString("sec2_uniq7") {
		t.Error("a regex compiled while the cache was full does not match")
	}
	if got := reCacheSize.Load(); got > maxReCache {
		t.Errorf("cache grew past the cap while full: %d > %d", got, maxReCache)
	}
}

// TestReadFileSizeCap: an over-limit file is a catchable Err, not an OOM; an under-limit file
// reads normally. The limit is lowered for the test.
func TestReadFileSizeCap(t *testing.T) {
	saved := maxReadFileBytes
	maxReadFileBytes = 16
	defer func() { maxReadFileBytes = saved }()

	dir := t.TempDir()
	small := filepath.Join(dir, "small.txt")
	big := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(small, []byte("tiny"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(big, []byte(strings.Repeat("x", 100)), 0o644); err != nil {
		t.Fatal(err)
	}
	sp, bp := filepath.ToSlash(small), filepath.ToSlash(big)

	assertBoth(t, `say(read_file("`+sp+`"))`, "tiny\n")
	assertBoth(t, `say(is_err(read_file("`+bp+`")))`, "true\n")
	if out := run(t, `say(err_msg(read_file("`+bp+`")))`); !strings.Contains(out, "exceeds") {
		t.Errorf("over-limit read_file message = %q, want it to mention the limit", out)
	}
}

// TestSanitizeCSVFieldSet pins the full OWASP dangerous-lead set (= + - @ tab CR LF) — the LF
// case is the one an earlier version missed — and confirms harmless leads (empty, a leading
// SPACE, a mid-string '=') are left untouched.
func TestSanitizeCSVFieldSet(t *testing.T) {
	for _, d := range []string{"=1", "+1", "-1", "@x", "\t=x", "\r=x", "\n=x"} {
		if got := sanitizeCSVField(d); got != "'"+d {
			t.Errorf("sanitizeCSVField(%q) = %q, want it prefixed with a quote", d, got)
		}
	}
	for _, s := range []string{"", "abc", "1.5", " =x", "x=y"} {
		if got := sanitizeCSVField(s); got != s {
			t.Errorf("sanitizeCSVField(%q) = %q, want it unchanged", s, got)
		}
	}
}

// TestToCSVSanitize: {sanitize} prefixes a spreadsheet-formula lead with ' so it is inert;
// without it, the data is written faithfully (a leading '=' or '-' is preserved).
func TestToCSVSanitize(t *testing.T) {
	// Off by default: faithful data (crlf:false so the line ends in one \n; say adds another).
	assertBoth(t, `say(to_csv([["=1+2", "ok"]], {crlf: false}))`, "=1+2,ok\n\n")
	// On: every dangerous lead (= + - @) gets a ' prefix; a safe cell is untouched.
	assertBoth(t, `say(to_csv([["=1+2", "+A1", "-5", "@x", "safe"]], {sanitize: true, crlf: false}))`,
		"'=1+2,'+A1,'-5,'@x,safe\n\n")
}
