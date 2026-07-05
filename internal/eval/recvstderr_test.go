package eval

import (
	"strings"
	"testing"
)

// TestRecvStderr: recv_stderr reads a started child's stderr (a stream distinct from stdout) and
// drains to nil at EOF. The child redirects its echo to stderr (1>&2). Asserted on BOTH backends
// since recv_stderr is a plain builtin.
func TestRecvStderr(t *testing.T) {
	src := "$p := start(\"cmd\", \"/c\", \"echo err_line 1>&2\", {stderr_pipe: true})\n" +
		"$out := \"\"\n" +
		"$chunk := recv_stderr($p)\n" +
		"while $chunk {\n" +
		"  $out = $out ~ $chunk\n" +
		"  $chunk = recv_stderr($p)\n" +
		"}\n" +
		"await($p)\n" +
		"say(replace_all(trim($out), \"\\r\", \"\"))"
	assertBoth(t, src, "err_line\n")
}

// TestRecvStderrEOF: a child that writes to stderr then exits yields its output, then nil (EOF).
func TestRecvStderrEOF(t *testing.T) {
	src := "$p := start(\"cmd\", \"/c\", \"echo hello 1>&2\", {stderr_pipe: true})\n" +
		"say(trim(recv_stderr($p)))\n" +
		"say(!recv_stderr($p))\n" +
		"await($p)"
	assertBoth(t, src, "hello\ntrue\n")
}

// TestRecvStderrSeparate: with both {stdout_pipe} and {stderr_pipe}, stdout and stderr arrive on
// independent pipes and do not cross-contaminate. Output is tiny (fits both pipe buffers), so a
// sequential drain (all of stdout, then all of stderr) cannot deadlock here.
func TestRecvStderrSeparate(t *testing.T) {
	src := "fn .drain($rd) {\n" +
		"  $s := \"\"\n" +
		"  $x := $rd()\n" +
		"  while $x { $s = $s ~ $x; $x = $rd() }\n" +
		"  trim(replace_all($s, \"\\r\", \"\"))\n" +
		"}\n" +
		"$p := start(\"cmd\", \"/c\", \"echo OUT& echo ERR 1>&2\", {stdout_pipe: true, stderr_pipe: true})\n" +
		"$o := .drain(|| recv_stdout($p))\n" +
		"$e := .drain(|| recv_stderr($p))\n" +
		"await($p)\n" +
		"say(\"stdout=\" ~ $o ~ \" stderr=\" ~ $e)"
	assertBoth(t, src, "stdout=OUT stderr=ERR\n")
}

// TestRecvStderrBothConcurrent: drain a single child's stdout and stderr CONCURRENTLY (stderr in a
// spawned task, stdout on the main task) — the documented deadlock-free pattern for reading both.
// Under -race this proves the per-Proc stdout and stderr readers (separate mutexes) do not race.
// The main task defines no top-level variable after the spawn (the whole result is one read-only
// say() expression), so both goroutines only READ the shared scope — the concurrency under test is
// the two Proc readers, not drang's spawn/scope semantics.
func TestRecvStderrBothConcurrent(t *testing.T) {
	src := "fn .drain($rd) {\n" +
		"  $s := \"\"\n" +
		"  $x := $rd()\n" +
		"  while $x { $s = $s ~ $x; $x = $rd() }\n" +
		"  trim(replace_all($s, \"\\r\", \"\"))\n" +
		"}\n" +
		"$p := start(\"cmd\", \"/c\", \"echo OUT& echo ERR 1>&2\", {stdout_pipe: true, stderr_pipe: true})\n" +
		"$et := spawn(|$_| .drain(|| recv_stderr($p)), 0)\n" +
		"say(\"stdout=\" ~ .drain(|| recv_stdout($p)) ~ \" stderr=\" ~ await($et))"
	if got := strings.TrimSpace(run(t, src)); got != "stdout=OUT stderr=ERR" {
		t.Errorf("concurrent stdout+stderr drain: got %q, want \"stdout=OUT stderr=ERR\"", got)
	}
}

// TestRecvStderrRejections: the error modes hold identically on both backends.
func TestRecvStderrRejections(t *testing.T) {
	// not a process, and a process not started with {stderr_pipe} -> catchable Err on both backends:
	assertBoth(t, "say(is_err(recv_stderr(5)))", "true\n")
	assertBoth(t, "$p := start(\"cmd\",\"/c\",\"exit 0\"); await($p); say(is_err(recv_stderr($p)))", "true\n")
	// {stderr_pipe} on a synchronous form, {stderr_pipe}+{merge_stderr}, and wrong arity all ABORT
	// (uncatchable) on both backends:
	for _, vm := range []bool{false, true} {
		if _, err := runBackend(t, "capture(\"cmd\",\"/c\",\"echo hi\",{stderr_pipe: true})", vm); err == nil {
			t.Errorf("vm=%v: stderr_pipe on a synchronous form should be rejected", vm)
		}
		if _, err := runBackend(t, "start(\"cmd\",\"/c\",\"echo hi\",{stderr_pipe: true, merge_stderr: true})", vm); err == nil {
			t.Errorf("vm=%v: stderr_pipe + merge_stderr should be rejected", vm)
		}
		if _, err := runBackend(t, "$p := start(\"cmd\",\"/c\",\"exit 0\"); recv_stderr()", vm); err == nil {
			t.Errorf("vm=%v: recv_stderr with 0 args should abort", vm)
		}
	}
}

// TestRecvStderrConcurrent: many spawned workers each start a stderr-writing child and drain it.
// Run under -race, this catches any unsynchronized access to the per-Proc stderr state.
func TestRecvStderrConcurrent(t *testing.T) {
	src := "fn .work($_) {\n" +
		"  $p := start(\"cmd\", \"/c\", \"echo hi 1>&2\", {stderr_pipe: true})\n" +
		"  $out := \"\"\n" +
		"  $c := recv_stderr($p)\n" +
		"  while $c {\n" +
		"    $out = $out ~ $c\n" +
		"    $c = recv_stderr($p)\n" +
		"  }\n" +
		"  await($p)\n" +
		"  trim(replace_all($out, \"\\r\", \"\"))\n" +
		"}\n" +
		"$ts := map([1, 2, 3, 4, 5, 6], |$i| spawn(.work, $i))\n" +
		"say(len(filter(map($ts, |$t| await($t)), |$r| $r == \"hi\")))"
	if got := strings.TrimSpace(run(t, src)); got != "6" {
		t.Errorf("concurrent recv_stderr: got %q workers echoing \"hi\", want 6", got)
	}
}
