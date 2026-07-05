package eval

import (
	"strings"
	"testing"
)

// TestRecvStdout: recv_stdout reads a started child's stdout, and drains to nil at EOF. `sort`
// receives the piped input and, on close_stdin (EOF), writes the sorted result to its stdout, which
// the script drains — proving the read side end to end. (sort buffers to EOF, so this is a
// send-then-drain round-trip; interleaved live steering exercises the same code path.) Asserted on
// BOTH backends since recv_stdout is a plain builtin.
func TestRecvStdout(t *testing.T) {
	src := "$p := start(\"sort\", {stdin_pipe: true, stdout_pipe: true})\n" +
		"send_stdin($p, \"banana\\n\")\n" +
		"send_stdin($p, \"apple\\n\")\n" +
		"close_stdin($p)\n" +
		"$out := \"\"\n" +
		"$chunk := recv_stdout($p)\n" +
		"while $chunk {\n" +
		"  $out = $out ~ $chunk\n" +
		"  $chunk = recv_stdout($p)\n" +
		"}\n" +
		"await($p)\n" +
		"say(replace_all(trim($out), \"\\r\", \"\"))"
	assertBoth(t, src, "apple\nbanana\n")
}

// TestRecvStdoutEOF: a child that writes then exits yields its output, then nil (EOF, which is falsy).
func TestRecvStdoutEOF(t *testing.T) {
	src := "$p := start(\"cmd\", \"/c\", \"echo hello\", {stdout_pipe: true})\n" +
		"say(trim(recv_stdout($p)))\n" +
		"say(!recv_stdout($p))\n" +
		"await($p)"
	assertBoth(t, src, "hello\ntrue\n")
}

// TestRecvStdoutRejections: the error modes hold identically on both backends.
func TestRecvStdoutRejections(t *testing.T) {
	// not a process, and a process not started with {stdout_pipe} -> catchable Err on both backends:
	assertBoth(t, "say(is_err(recv_stdout(5)))", "true\n")
	assertBoth(t, "$p := start(\"cmd\",\"/c\",\"exit 0\"); await($p); say(is_err(recv_stdout($p)))", "true\n")
	// {stdout_pipe} on a synchronous form, and wrong arity, ABORT (uncatchable) on both backends:
	for _, vm := range []bool{false, true} {
		if _, err := runBackend(t, "capture(\"cmd\",\"/c\",\"echo hi\",{stdout_pipe: true})", vm); err == nil {
			t.Errorf("vm=%v: stdout_pipe on a synchronous form should be rejected", vm)
		}
		if _, err := runBackend(t, "$p := start(\"cmd\",\"/c\",\"exit 0\"); recv_stdout()", vm); err == nil {
			t.Errorf("vm=%v: recv_stdout with 0 args should abort", vm)
		}
	}
}

// TestRecvStdoutConcurrent: many spawned workers each steer their own child and read its stdout.
// Run under -race, this catches any unsynchronized access to the per-Proc stdout state.
func TestRecvStdoutConcurrent(t *testing.T) {
	src := "fn .work($_) {\n" +
		"  $p := start(\"cmd\", \"/c\", \"echo hi\", {stdout_pipe: true})\n" +
		"  $out := \"\"\n" +
		"  $c := recv_stdout($p)\n" +
		"  while $c {\n" +
		"    $out = $out ~ $c\n" +
		"    $c = recv_stdout($p)\n" +
		"  }\n" +
		"  await($p)\n" +
		"  trim(replace_all($out, \"\\r\", \"\"))\n" +
		"}\n" +
		"$ts := map([1, 2, 3, 4, 5, 6], |$i| spawn(.work, $i))\n" +
		"say(len(filter(map($ts, |$t| await($t)), |$r| $r == \"hi\")))"
	if got := strings.TrimSpace(run(t, src)); got != "6" {
		t.Errorf("concurrent recv_stdout: got %q workers echoing \"hi\", want 6", got)
	}
}
