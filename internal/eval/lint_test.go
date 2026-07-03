package eval

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestDuplicateTopFns(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{"none", "fn .a() { 1 }\nfn .b() { 2 }", nil},
		{"one-dup", "fn .a() { 1 }\nfn .a() { 2 }", []string{".a"}},
		{"triple-reported-once", "fn .a() {1}\nfn .a() {2}\nfn .a() {3}", []string{".a"}},
		{"two-distinct-dups-in-order", "fn .a(){1}\nfn .b(){1}\nfn .a(){2}\nfn .b(){2}", []string{".a", ".b"}},
		// Only the top level counts: a fn defined in each branch is a deliberate
		// conditional definition, not a duplicate.
		{"conditional-branches-not-counted", "if true { fn .h(){1} } else { fn .h(){2} }", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DuplicateTopFns(mustParse(t, c.src))
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("DuplicateTopFns(%q) = %v, want %v", c.src, got, c.want)
			}
		})
	}
}

func TestWarnDuplicateTopFns(t *testing.T) {
	// A duplicate prints exactly one warning line, naming both the origin and the function.
	var buf bytes.Buffer
	WarnDuplicateTopFns(mustParse(t, "fn .a(){1}\nfn .a(){2}"), "prog.dr", &buf)
	got := buf.String()
	for _, want := range []string{"prog.dr", ".a", "defined more than once", "last definition wins"} {
		if !strings.Contains(got, want) {
			t.Errorf("warning missing %q; got %q", want, got)
		}
	}
	if n := strings.Count(got, "\n"); n != 1 {
		t.Errorf("want exactly one warning line, got %d in %q", n, got)
	}
	// No duplicate → no output at all.
	buf.Reset()
	WarnDuplicateTopFns(mustParse(t, "fn .a(){1}\nfn .b(){2}"), "prog.dr", &buf)
	if buf.Len() != 0 {
		t.Errorf("expected no warning, got %q", buf.String())
	}
}
