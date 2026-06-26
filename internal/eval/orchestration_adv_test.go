package eval

import (
	"strings"
	"testing"
)

// These exercise the advanced orchestration surface end-to-end (real child
// processes); run under -race to cover the Proc reaper, pipe waits, and the
// each_line callback. Windows commands, matching the target platform.

func TestPipeline(t *testing.T) {
	out := runWithEnv(t, NewEnv(), `say(pipe(["cmd", "/c", "echo hi there"], ["findstr", "there"]))`)
	if !strings.Contains(out, "there") {
		t.Errorf("pipe echo|findstr: %q", out)
	}
}

func TestPipelineExitCode(t *testing.T) {
	out := runWithEnv(t, NewEnv(), `$r := pipe(["cmd", "/c", "echo x"], ["cmd", "/c", "exit", "3"])
say(is_err($r), err_code($r))`)
	if !strings.Contains(out, "true 3") {
		t.Errorf("pipe should report the last stage's exit code: %q", out)
	}
}

func TestEachLineStreaming(t *testing.T) {
	out := runWithEnv(t, NewEnv(), `$lines := []
$ok := each_line("cmd", "/c", "echo a& echo b& echo c", |$l| push($lines, trim($l)))
say($ok, len($lines))`)
	if !strings.Contains(out, "true 3") {
		t.Errorf("each_line should stream 3 lines and succeed: %q", out)
	}
}

func TestExecTimeout(t *testing.T) {
	out := runWithEnv(t, NewEnv(), `$r := capture("cmd", "/c", "ping", "127.0.0.1", "-n", "6", {timeout: 200})
say(is_err($r), err_code($r))`)
	if !strings.Contains(out, "true 124") {
		t.Errorf("a timed-out command should be Err code 124: %q", out)
	}
}

func TestEachLineLongLine(t *testing.T) {
	// A stdout line beyond the 4MB scanner cap must yield a catchable Err (not hang,
	// not silently succeed). The test completing at all proves the no-hang fix.
	out := runWithEnv(t, NewEnv(), `$r := each_line("powershell", "-NoProfile", "-Command", "[Console]::Out.Write('x' * 5000000)", |$l| 0)
say(is_err($r))`)
	if !strings.Contains(out, "true") {
		t.Errorf("each_line on a >4MB line should return an Err, got %q", out)
	}
}

func TestProcAwaitAndKill(t *testing.T) {
	// await yields the exit status...
	out := runWithEnv(t, NewEnv(), `$p := start("cmd", "/c", "exit", "5")
say(err_code(await($p)))`)
	if !strings.Contains(out, "5") {
		t.Errorf("await should yield the process exit code: %q", out)
	}
	// ...and kill stops a long-runner, whose await then reports an error.
	out = runWithEnv(t, NewEnv(), `$p := start("cmd", "/c", "ping", "127.0.0.1", "-n", "30")
say(kill($p), is_err(await($p)))`)
	if !strings.Contains(out, "true true") {
		t.Errorf("kill should stop the process and await report an error: %q", out)
	}
}
