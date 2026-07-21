package eval

import (
	"bytes"
	"strings"
	"testing"
)

// validate is a boundary shape checker: value-or-Err, every mismatch reported with
// its path. These run on both backends via assertBoth (parity by construction).

// runBackendPrelude mirrors runBackend but loads the prelude first, for tests that
// exercise the prelude combinators (one_of, lit) against both backends.
func runBackendPrelude(t *testing.T, src string, vm bool) (string, error) {
	t.Helper()
	prog := mustParseProg(t, src)
	var buf bytes.Buffer
	old := stdout
	oldVM := vmEnabled
	stdout = &buf
	defer func() { stdout = old; vmEnabled = oldVM }()
	vmEnabled = vm
	env := NewEnv()
	if err := RunPrelude(env); err != nil {
		t.Fatalf("prelude: %v", err)
	}
	var err error
	if vm {
		err = RunProgramVM(prog, env)
	} else {
		err = RunProgram(prog, env)
	}
	return buf.String(), err
}

func assertBothPrelude(t *testing.T, src, want string) {
	t.Helper()
	for _, vm := range []bool{false, true} {
		got, err := runBackendPrelude(t, src, vm)
		if err != nil {
			t.Fatalf("vm=%v error for %q: %v", vm, src, err)
		}
		if got != want {
			t.Errorf("vm=%v: got %q, want %q\nsrc: %s", vm, got, want, src)
		}
	}
}

func TestValidatePassThrough(t *testing.T) {
	// Success returns the SAME value (pass-through), so validate composes inline.
	assertBoth(t, `$m := {host: "x", port: 8080}
$r := validate($m, {host: str, port: int})
say($r.host)
say($r == $m)`, "x\ntrue\n")
	// A non-map root validates too, and passes through.
	assertBoth(t, `say(validate(5, |$v| $v > 3))`, "5\n")
}

func TestValidateTypeTokens(t *testing.T) {
	assertBoth(t, `say(err_msg(validate({port: "80"}, {port: int})))`,
		"validate: port: want int, got str \"80\"\n")
	assertBoth(t, `say(is_err(validate({n: 1.5}, {n: float})))`, "false\n")
	// Exact tags — no coercion, like the rest of the language.
	assertBoth(t, `say(is_err(validate({n: 1}, {n: float})))`, "true\n")
	assertBoth(t, `say(is_err(validate({b: true}, {b: bool})))`, "false\n")
	// The root value itself reads as "value" in the report.
	assertBoth(t, `say(err_msg(validate("nope", {a: int})))`,
		"validate: value: want map, got str \"nope\"\n")
}

func TestValidateMissingAndExtra(t *testing.T) {
	assertBoth(t, `say(err_msg(validate({}, {port: int})))`,
		"validate: port: missing (want int)\n")
	// Strict by default: an undeclared key is a mismatch (the write-typo catcher).
	assertBoth(t, `say(err_msg(validate({port: 1, extra: 2}, {port: int})))`,
		"validate: extra: unexpected key\n")
	// Every mismatch is reported in one Err, not just the first.
	assertBoth(t, `$e := err_msg(validate({a: "x"}, {a: int, b: str}))
say(contains($e, "want int") and contains($e, "b: missing"))`, "true\n")
}

func TestValidateOptionalKeys(t *testing.T) {
	assertBoth(t, `say(is_err(validate({host: "h"}, {host: str, "port?": int})))`, "false\n")
	assertBoth(t, `say(is_err(validate({host: "h", port: 1}, {host: str, "port?": int})))`, "false\n")
	assertBoth(t, `say(is_err(validate({host: "h", port: "x"}, {host: str, "port?": int})))`, "true\n")
	// Present-as-nil counts as absent — the language's own absence stance.
	assertBoth(t, `$src := {}
say(is_err(validate({host: "h", port: $src.nope}, {host: str, "port?": int})))`, "false\n")
	assertBoth(t, `$src := {}
say(is_err(validate({stray: $src.nope}, {})))`, "false\n")
}

func TestValidateWildcard(t *testing.T) {
	// "..." holds the term extra keys must match; its presence makes the map open.
	assertBoth(t, `say(is_err(validate({a: 1, b: 2}, {"...": int})))`, "false\n")
	assertBoth(t, `say(is_err(validate({a: 1, b: "x"}, {"...": int})))`, "true\n")
	assertBoth(t, `say(is_err(validate({m: "GET", x: 9, y: []}, {m: str, "...": true})))`, "false\n")
}

func TestValidateArraysAndNesting(t *testing.T) {
	assertBoth(t, `say(is_err(validate({tags: ["a", "b"]}, {tags: [str]})))`, "false\n")
	assertBoth(t, `say(err_msg(validate({tags: ["a", 3]}, {tags: [str]})))`,
		"validate: tags[1]: want str, got int 3\n")
	assertBoth(t, `say(is_err(validate({xs: [1, "mixed"]}, {xs: []})))`, "false\n")
	assertBoth(t, `say(is_err(validate({tags: "ab"}, {tags: [str]})))`, "true\n")
	assertBoth(t, `say(err_msg(validate({child: {cmd: 7}}, {child: {cmd: str}})))`,
		"validate: child.cmd: want str, got int 7\n")
}

func TestValidatePredicatesAndCombinators(t *testing.T) {
	assertBoth(t, `say(is_err(validate({port: 8080}, {port: |$v| $v > 1024})))`, "false\n")
	// A predicate that fails with fail(...) surfaces its own message at the path.
	assertBoth(t, `$check := |$v| {
  if $v > 1024 { return true }
  fail("port must be above 1024")
}
$e := err_msg(validate({port: 80}, {port: $check}))
say(contains($e, "port: port must be above 1024"))`, "true\n")
	// Prelude combinators: alternatives and exact literals.
	assertBothPrelude(t, `say(is_err(validate({r: true}, {r: one_of([int, bool])})))`, "false\n")
	assertBothPrelude(t, `$e := err_msg(validate({r: "x"}, {r: one_of([int, bool])}))
say(contains($e, "none of the alternatives"))`, "true\n")
	assertBothPrelude(t, `say(is_err(validate({m: "GET"}, {m: lit("GET")})))`, "false\n")
	assertBothPrelude(t, `say(is_err(validate({m: "POST"}, {m: lit("GET")})))`, "true\n")
}

func TestValidateInvalidShapeAborts(t *testing.T) {
	// A malformed shape is a programming mistake: an ABORT, never a catchable Err —
	// so a typo'd shape cannot be silently absorbed by `//`.
	for _, vm := range []bool{false, true} {
		_, err := runBackend(t, `validate({a: 1}, {a: "int"}) // "swallowed?"`, vm)
		if err == nil || !strings.Contains(err.Error(), "lit(") {
			t.Errorf("vm=%v: a string shape term must abort with the lit() hint, got %v", vm, err)
		}
		_, err = runBackend(t, `validate({a: [1]}, {a: [int, str]})`, vm)
		if err == nil || !strings.Contains(err.Error(), "one element term") {
			t.Errorf("vm=%v: a two-term array shape must abort, got %v", vm, err)
		}
	}
}

func TestEulerBuiltin(t *testing.T) {
	// e() is Euler's number, the sibling of pi(); exp(1) == e().
	assertBoth(t, `say(e() == exp(1))`, "true\n")
	assertBoth(t, `say(floor(e() * 100))`, "271\n")
}
