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
	// Example rule: rename the ident `count` to `total` (a builtin rename migration).
	rule := func(n ast.Node) {
		if id, ok := n.(*ast.Ident); ok && id.Name == "count" {
			id.Name = "total"
		}
	}
	Walk(prog, rule)
	out := Program(prog, nil)
	if strings.Contains(out, "count") {
		t.Errorf("rename did not reach every node:\n%s", out)
	}
	if !strings.Contains(out, "total([1, 2, 3])") || !strings.Contains(out, "|> total") {
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

// TestFixNoSpuriousRewrites confirms FormatFix leaves source with no legacy names
// byte-identical to Format (rules must be no-ops when their pattern is absent).
func TestFixNoSpuriousRewrites(t *testing.T) {
	src := "say(count(1))\n$m := {replace: 1}\n"
	plain, err := Format(src)
	if err != nil {
		t.Fatal(err)
	}
	fixed, err := FormatFix(src)
	if err != nil {
		t.Fatal(err)
	}
	if plain != fixed {
		t.Errorf("FormatFix changed output with no legacy names present:\n plain=%q\n fixed=%q", plain, fixed)
	}
}

// fixup runs FormatFix and fails the test on error.
func fixup(t *testing.T, src string) string {
	t.Helper()
	out, err := FormatFix(src)
	if err != nil {
		t.Fatalf("FormatFix(%q): %v", src, err)
	}
	return out
}

// TestFixNamespace08Renames covers the pre-1.0 namespace migration's plain renames in
// every syntactic position: callee, piped callee, and first-class reference.
func TestFixNamespace08Renames(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"callee", `say(index_of("hello", "ll"))`, `say(find_index("hello", "ll"))`},
		{"piped", `"a b" |> each_line(|$l| say($l))`, `"a b" |> stream_lines(|$l| say($l))`},
		{"first-class", `map($paths, abspath)`, `map($paths, abs_path)`},
		{"replace-was-all", `replace($s, "a", "b")`, `replace_all($s, "a", "b")`},
		{"strftime", `strftime(0, "%Y")`, `format_time(0, "%Y")`},
		{"url", `say(url_encode(url_decode($s)))`, `say(to_url(from_url($s)))`},
		{"slash", `slash($p)`, `to_slash($p)`},
		{"sys-gc", `sys_gc("off")`, `drang_gc("off")`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := strings.TrimSpace(fixup(t, c.src+"\n"))
			if got != c.want {
				t.Errorf("fix(%q) = %q, want %q", c.src, got, c.want)
			}
		})
	}
}

// TestFixNamespace08MapKeysProtected: a bare map-literal KEY that happens to spell a
// legacy builtin name is user data and must never be renamed.
func TestFixNamespace08MapKeysProtected(t *testing.T) {
	got := fixup(t, "$m := {replace: 1, tally: 2, slash: gsub($s, qr/x/, \"y\")}\n")
	for _, key := range []string{"replace:", "tally:", "slash:"} {
		if !strings.Contains(got, key) {
			t.Errorf("map key was renamed away, want %q preserved in:\n%s", key, got)
		}
	}
	if !strings.Contains(got, "replace_all($s, qr/x/") {
		t.Errorf("map VALUE should still migrate:\n%s", got)
	}
}

// TestFixNamespace08Gsub: gsub becomes replace_all; a non-qr pattern is wrapped in
// re(...) because old gsub compiled a STRING pattern as a regex, while replace_all
// treats a string needle as a literal.
func TestFixNamespace08Gsub(t *testing.T) {
	cases := []struct{ name, src, wantSub string }{
		{"regex-literal-unwrapped", `gsub($s, qr/\d/, "#")`, `replace_all($s, qr/\d/, "#")`},
		{"string-pattern-wrapped", `gsub($s, "a+", "#")`, `replace_all($s, re("a+"), "#")`},
		{"expr-pattern-wrapped", `gsub($s, $pat, "#")`, `replace_all($s, re($pat), "#")`},
		{"piped-string-wrapped", `$s |> gsub("a+", "#")`, `$s |> replace_all(re("a+"), "#")`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := fixup(t, c.src+"\n")
			if !strings.Contains(got, c.wantSub) {
				t.Errorf("fix(%q) = %q, want it to contain %q", c.src, got, c.wantSub)
			}
		})
	}
	// Idempotent: fixing already-fixed output changes nothing.
	once := fixup(t, `gsub($s, "a+", "#")`+"\n")
	if twice := fixup(t, once); twice != once {
		t.Errorf("gsub rule not idempotent:\n once=%q\n twice=%q", once, twice)
	}
}

// TestFixNamespace08Tally: tally($xs) becomes count_by($xs, |$e| $e) — the identity
// key — in both plain and piped form.
func TestFixNamespace08Tally(t *testing.T) {
	got := fixup(t, "say(tally($xs))\n$h := $xs |> tally()\n")
	if !strings.Contains(got, "count_by($xs, |$e| $e)") {
		t.Errorf("plain tally not rewritten:\n%s", got)
	}
	if !strings.Contains(got, "|> count_by(|$e| $e)") {
		t.Errorf("piped tally not rewritten:\n%s", got)
	}
	if twice := fixup(t, got); twice != got {
		t.Errorf("tally rule not idempotent:\n once=%q\n twice=%q", got, twice)
	}
}

// TestFixNamespace08Interp: a renamed builtin inside an interpolating string must
// migrate too — the rule clears Interp.Raw so the printer re-renders from Parts
// instead of reprinting the stale verbatim source (which would silently discard the
// migration and leave a call to a deleted builtin).
func TestFixNamespace08Interp(t *testing.T) {
	cases := []struct{ name, src, wantSub, mustNotContain string }{
		{"rename-in-interp", `say($"i=${index_of($s, $t)}")`, `${find_index($s, $t)}`, "index_of"},
		{"gsub-wrap-in-interp", `say($"r=${gsub($s, $p, $r)}")`, `${replace_all($s, re($p), $r)}`, "gsub"},
		{"qq-form", `say($qq{u=${url_encode($u)}})`, `${to_url($u)}`, "url_encode"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := fixup(t, c.src+"\n")
			if !strings.Contains(got, c.wantSub) {
				t.Errorf("fix(%q) = %q, want it to contain %q", c.src, got, c.wantSub)
			}
			if strings.Contains(got, c.mustNotContain) {
				t.Errorf("fix(%q) = %q — legacy name %q survived inside the interpolation", c.src, got, c.mustNotContain)
			}
			if twice := fixup(t, got); twice != got {
				t.Errorf("interp rewrite not idempotent:\n once=%q\n twice=%q", got, twice)
			}
		})
	}
	// An interpolation with NO legacy names keeps its verbatim Raw (fidelity: no
	// spurious re-rendering).
	src := `say($"n=${find_index($s, $t)}")` + "\n"
	if got := fixup(t, src); got != src {
		t.Errorf("clean interpolation was rewritten:\n in=%q\nout=%q", src, got)
	}
}

// TestFixNamespace08FirstClassLeftAlone: bare FIRST-CLASS references to gsub and tally
// are deliberately NOT renamed — a name swap cannot preserve their semantics (gsub's
// string-pattern-as-regex; tally's arity), so the rule leaves them to fail loudly at
// runtime rather than silently corrupting behavior. A wrong-arity gsub/tally call
// (already an error in 0.7) is likewise left untouched.
func TestFixNamespace08FirstClassLeftAlone(t *testing.T) {
	cases := []struct{ name, src, mustKeep string }{
		{"first-class-gsub-decl", `$f := gsub`, "gsub"},
		{"first-class-gsub-arg", `map($fs, gsub)`, "gsub"},
		{"first-class-tally", `$t := tally`, "tally"},
		{"two-arg-tally-call", `say(tally($xs, $f))`, "tally($xs, $f)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := fixup(t, c.src+"\n")
			if !strings.Contains(got, c.mustKeep) {
				t.Errorf("fix(%q) = %q, want %q left untouched (loud runtime failure beats a silent semantic flip)", c.src, got, c.mustKeep)
			}
			if strings.Contains(got, "replace_all") || strings.Contains(got, "count_by") {
				t.Errorf("fix(%q) = %q — a non-migratable reference was renamed", c.src, got)
			}
		})
	}
}
