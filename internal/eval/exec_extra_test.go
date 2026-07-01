package eval

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/anafalanx/drang/internal/value"
)

// TestExecArg0 verifies the {arg0} option changes the presented argv[0] without
// changing the actually-launched binary (cmd.Path).
func TestExecArg0(t *testing.T) {
	cmd := exec.Command("the-real-exe", "a", "b")
	applyExecOpts(cmd, execOpts{arg0: "spoofed", hasArg0: true})
	if cmd.Args[0] != "spoofed" {
		t.Errorf("cmd.Args[0] = %q, want spoofed", cmd.Args[0])
	}
	if len(cmd.Args) != 3 || cmd.Args[1] != "a" || cmd.Args[2] != "b" {
		t.Errorf("arg0 disturbed the other args: %v", cmd.Args)
	}
	if cmd.Path == "spoofed" {
		t.Errorf("arg0 must not change the launched binary (cmd.Path = %q)", cmd.Path)
	}
}

// TestCaptureAll: capture_all always returns a {out,err,code,ok} record; a
// non-zero exit is data, never a thrown Err.
func TestCaptureAll(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"ok", `$r := capture_all(["cmd", "/c", "echo hi"]); say($r.out, $r.code, $r.ok)`, "hi 0 true\n"},
		{"fail-code", `$r := capture_all(["cmd", "/c", "exit 3"]); say($r.code, $r.ok)`, "3 false\n"},
		{"no-abort-on-fail", `say(is_err(capture_all(["cmd", "/c", "exit 1"])))`, "false\n"},
		{"bad-start", `say(capture_all(["this-program-does-not-exist-xyz"]).code)`, "127\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := run(t, c.src); got != c.want {
				t.Errorf("%s\n got %q\nwant %q", c.src, got, c.want)
			}
		})
	}
}

func TestExecEnvOptions(t *testing.T) {
	t.Setenv("DRANG_PARENT_ONLY", "parent")

	exact := value.MakeMap()
	exact.Obj().(*value.OrderedMap).Set(str("DRANG_CHILD_ONLY"), str("child"))
	exactOpts := value.MakeMap().Obj().(*value.OrderedMap)
	exactOpts.Set(str("env_exact"), exact)
	o, err := execOptions("run", exactOpts)
	if err != nil {
		t.Fatal(err)
	}
	if !o.hasEnv || !o.resolveWithEnv {
		t.Fatalf("exact env should set env and env-based resolution: %#v", o)
	}
	if len(o.env) != 1 || o.env[0] != "DRANG_CHILD_ONLY=child" {
		t.Fatalf("exact env = %#v, want only DRANG_CHILD_ONLY=child", o.env)
	}

	overlay := value.MakeMap()
	overlay.Obj().(*value.OrderedMap).Set(str("drang_parent_only"), str("new"))
	overlayOpts := value.MakeMap().Obj().(*value.OrderedMap)
	overlayOpts.Set(str("env_add"), overlay)
	o, err = execOptions("run", overlayOpts)
	if err != nil {
		t.Fatal(err)
	}
	count, val := 0, ""
	for _, e := range o.env {
		if i := strings.IndexByte(e, '='); i >= 0 && strings.EqualFold(e[:i], "DRANG_PARENT_ONLY") {
			count++
			val = e[i+1:]
		}
	}
	if count != 1 || val != "new" {
		t.Fatalf("env_add replaced %d entries with %q, want one replacement of new in %#v", count, val, o.env)
	}
	if _, ok := envLookupFold(o.env, "PATH"); !ok {
		t.Fatalf("env_add should inherit PATH; env=%#v", o.env)
	}
}

func TestExecEnvAndEnvAddConflict(t *testing.T) {
	exact := value.MakeMap()
	overlay := value.MakeMap()
	opts := value.MakeMap().Obj().(*value.OrderedMap)
	opts.Set(str("env_exact"), exact)
	opts.Set(str("env_add"), overlay)
	if _, err := execOptions("run", opts); err == nil {
		t.Fatal("env_exact and env_add together should be rejected")
	}
}

// The conventional name `env` is caught with a teaching error pointing to env_exact/env_add.
func TestExecEnvRenameHint(t *testing.T) {
	opts := value.MakeMap().Obj().(*value.OrderedMap)
	opts.Set(str("env"), value.MakeMap())
	_, err := execOptions("run", opts)
	if err == nil {
		t.Fatal("the bare 'env' option should be rejected")
	}
	if !strings.Contains(err.Error(), "env_exact") || !strings.Contains(err.Error(), "env_add") {
		t.Fatalf("'env' rejection should point to env_exact and env_add, got %v", err)
	}
}

func TestCaptureExactEnvDoesNotInheritParent(t *testing.T) {
	t.Setenv("DRANG_PARENT_ONLY", "parent")
	src := `$e := {
	PATH: env("PATH"),
	SystemRoot: env("SystemRoot"),
	DRANG_CHILD_ONLY: "child",
}
say(capture("cmd", "/c", "if defined DRANG_PARENT_ONLY (echo inherited:%DRANG_PARENT_ONLY%) else (echo exact:%DRANG_CHILD_ONLY%)", {env_exact: $e}))`
	if got := run(t, src); got != "exact:child\n" {
		t.Fatalf("exact env leaked or failed:\n got %q\nwant %q", got, "exact:child\n")
	}
}

func TestCaptureEnvAddInheritsParent(t *testing.T) {
	t.Setenv("DRANG_PARENT_ONLY", "parent")
	src := `say(capture("cmd", "/c", "echo %DRANG_PARENT_ONLY%:%DRANG_CHILD_ONLY%", {env_add: {DRANG_CHILD_ONLY: "child"}}))`
	if got := run(t, src); got != "parent:child\n" {
		t.Fatalf("env_add did not inherit/overlay:\n got %q\nwant %q", got, "parent:child\n")
	}
}
