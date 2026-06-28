package printer

import (
	"strings"
	"testing"

	"github.com/anafalanx/drang/internal/ast"
	"github.com/anafalanx/drang/internal/parser"
)

// TestFixMechanism exercises the edition/migration machinery (Walk) with an example
// rename rule, the way a future revision would, and confirms it reaches nodes everywhere
// (top level, inside a function, inside a pipe).
func TestFixMechanism(t *testing.T) {
	src := "say(count([1, 2, 3]))\n" +
		"fn .f($xs) { $xs |> count() }\n"
	p := parser.New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	// Example rule: rename the ident `count` to `tally` (a builtin rename migration).
	rule := func(n ast.Node) {
		if id, ok := n.(*ast.Ident); ok && id.Name == "count" {
			id.Name = "tally"
		}
	}
	Walk(prog, rule)
	out := Program(prog, nil)
	if strings.Contains(out, "count") {
		t.Errorf("rename did not reach every node:\n%s", out)
	}
	if !strings.Contains(out, "tally([1, 2, 3])") || !strings.Contains(out, "|> tally") {
		t.Errorf("expected renamed call and pipe stage:\n%s", out)
	}
	// Idempotent: applying the same rule again is a no-op.
	Walk(prog, rule)
	if again := Program(prog, nil); again != out {
		t.Errorf("rule not idempotent:\n%s\n vs\n%s", out, again)
	}
}

// TestRegexFallback checks the synthesized-regex fallback picks a valid same-char qr
// delimiter (only / and | are valid) or a brace form — never an invalid one like #.
func TestRegexFallback(t *testing.T) {
	cases := []struct{ pattern, want string }{
		{"abc", "qr/abc/"},
		{"a/b", "qr|a/b|"},     // has /, so use |
		{"a|b/c", "qr{a|b/c}"}, // has both / and |, so brace form
	}
	for _, c := range cases {
		if got := regexFallback(c.pattern); got != c.want {
			t.Errorf("regexFallback(%q) = %q, want %q", c.pattern, got, c.want)
		}
	}
}

// TestFixEmptyByDefault confirms FormatFix with no registered rules equals Format (the
// mechanism ships empty).
func TestFixEmptyByDefault(t *testing.T) {
	src := "say(count(1))\n"
	plain, err := Format(src)
	if err != nil {
		t.Fatal(err)
	}
	fixed, err := FormatFix(src)
	if err != nil {
		t.Fatal(err)
	}
	if plain != fixed {
		t.Errorf("FormatFix changed output with no rules:\n plain=%q\n fixed=%q", plain, fixed)
	}
}
