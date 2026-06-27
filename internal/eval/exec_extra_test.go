package eval

import (
	"os/exec"
	"runtime"
	"testing"
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
	if runtime.GOOS != "windows" {
		t.Skip("capture_all end-to-end test uses cmd.exe")
	}
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
