package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProcStatus: status(proc) polls without blocking and reports the ok/code shape after exit.
func TestProcStatus(t *testing.T) {
	assertBoth(t, `$p := start("cmd","/c","exit 0")
await($p)
$s := status($p)
say($s.running ~ " " ~ str($s.ok) ~ " " ~ str($s.code))`, "false true 0\n")
}

// TestKilledVsExited: a kill()'d process reports "was killed" (137), distinct from a natural exit.
func TestKilledVsExited(t *testing.T) {
	out := run(t, `$p := start("cmd","/c","ping -n 30 127.0.0.1 >nul")
kill($p)
$r := await($p)
say(err_code($r) ~ " " ~ err_msg($r))`)
	if !strings.Contains(out, "137") || !strings.Contains(out, "was killed") {
		t.Errorf("kill should surface code 137 'was killed', got %q", out)
	}
	assertBoth(t, `$p := start("cmd","/c","exit 3"); say(err_code(await($p)))`, "3\n") // natural exit keeps its code
}

// TestMergeStderr: {merge_stderr} interleaves the child's stderr into its stdout.
func TestMergeStderr(t *testing.T) {
	out := run(t, `say(capture("cmd","/c","echo OUT& echo ERRLINE 1>&2",{merge_stderr: true}))`)
	if !strings.Contains(out, "OUT") || !strings.Contains(out, "ERRLINE") {
		t.Errorf("merge_stderr should include both streams, got %q", out)
	}
}

// TestStdinFile: {stdin_file} feeds a child's stdin from a file (zero-copy).
func TestStdinFile(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "lines.txt")
	if err := os.WriteFile(in, []byte("apple\r\nberry\r\napricot\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := "say(capture(\"findstr\", \"ap\", {stdin_file: '" + in + "'}))"
	out := run(t, src)
	if !strings.Contains(out, "apple") || !strings.Contains(out, "apricot") || strings.Contains(out, "berry") {
		t.Errorf("stdin_file should filter to ap-lines, got %q", out)
	}
	// stdin + stdin_file are mutually exclusive
	assertBoth(t, "say(is_err(run(\"findstr\",\"x\",{stdin: 'hi', stdin_file: '"+in+"'})))", "true\n")
}

// TestCwdValidation: a bad {cwd} is a clean catchable Err, not a leaked internal launcher message.
func TestCwdValidation(t *testing.T) {
	out := run(t, `say(err_msg(capture("cmd","/c","echo hi",{cwd: "C:/no/such/dir/here"})))`)
	if !strings.Contains(out, "not an existing directory") || strings.Contains(out, "winjob.Launch") {
		t.Errorf("cwd error should be clean, got %q", out)
	}
}

// TestSendStdin: send_stdin/close_stdin drive a live child; sort receives the piped input and
// writes the sorted result, proving the input reached it.
func TestSendStdin(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "sorted.txt")
	src := "$p := start(\"cmd\", \"/c\", \"sort > \" ~ '" + outFile + "', {stdin_pipe: true})\n" +
		"send_stdin($p, \"banana\\n\")\n" +
		"send_stdin($p, \"apple\\n\")\n" +
		"close_stdin($p)\n" +
		"await($p)\n" +
		"say(\"done\")"
	if got := run(t, src); strings.TrimSpace(got) != "done" {
		t.Fatalf("script did not complete: %q", got)
	}
	b, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.ReplaceAll(strings.TrimSpace(string(b)), "\r", "")
	if got != "apple\nbanana" {
		t.Errorf("sort output = %q, want apple\\nbanana (send_stdin did not deliver the input)", got)
	}
}

// TestSendStdinRejections: send_stdin needs a {stdin_pipe} process, and stdin_pipe is start-only.
func TestSendStdinRejections(t *testing.T) {
	assertBoth(t, `$p := start("cmd","/c","exit 0"); await($p); say(is_err(send_stdin($p, "x")))`, "true\n")
	for _, vm := range []bool{false, true} {
		if _, err := runBackend(t, `capture("cmd","/c","echo hi",{stdin_pipe: true})`, vm); err == nil {
			t.Errorf("vm=%v: stdin_pipe should be rejected on synchronous forms", vm)
		}
	}
}
