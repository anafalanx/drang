package eval

import (
	"strings"
	"testing"
	"time"

	"github.com/anafalanx/drang/internal/value"
)

// These exercise the advanced orchestration surface end-to-end (real child
// processes); run under -race to cover the Proc reaper, pipe waits, and the
// stream_lines callback. Windows commands, matching the target platform.

func TestPipeline(t *testing.T) {
	out := runWithEnv(t, NewEnv(), `say(pipe(["cmd", "/c", "echo hi there"], ["findstr", "there"]))`)
	if !strings.Contains(out, "there") {
		t.Errorf("pipe echo|findstr: %q", out)
	}
}

func TestCaptureOverflowTerminatesCommandJob(t *testing.T) {
	saved := maxCaptureBytes
	maxCaptureBytes = 32 << 10
	defer func() { maxCaptureBytes = saved }()

	done := make(chan value.Value, 1)
	go func() {
		v, _ := builtinCapture([]value.Value{
			value.MakeStr("cmd"), value.MakeStr("/d"), value.MakeStr("/q"), value.MakeStr("/c"),
			value.MakeStr("(for /L %i in (1,1,10000) do @echo 012345678901234567890123456789) & ping 127.0.0.1 -n 30 >nul"),
		})
		done <- v
	}()
	select {
	case got := <-done:
		if !got.IsErr() || got.ErrCode() != 137 {
			t.Fatalf("capture overflow = %s (code %d), want Err code 137", got.Display(), got.ErrCode())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("capture overflow did not terminate the command job promptly")
	}
}

func TestAwaitStartedProcessDoesNotRequireConcurrentPipeDrain(t *testing.T) {
	pv, err := builtinStart([]value.Value{
		value.MakeStr("cmd"), value.MakeStr("/d"), value.MakeStr("/q"), value.MakeStr("/c"),
		value.MakeStr("for /L %i in (1,1,5000) do @echo 012345678901234567890123456789"),
		mkMap(value.MakeStr("stdout_pipe"), value.MakeBool(true)),
	})
	if err != nil || pv.IsErr() {
		t.Fatalf("start = %s, %v", pv.Display(), err)
	}
	done := make(chan value.Value, 1)
	go func() {
		v, _ := builtinAwait([]value.Value{pv})
		done <- v
	}()
	select {
	case got := <-done:
		if got.IsErr() {
			t.Fatalf("await = %s", got.Display())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("await blocked because stdout_pipe was not drained concurrently")
	}

	total := 0
	for {
		chunk, err := builtinRecvStdout([]value.Value{pv})
		if err != nil || chunk.IsErr() {
			t.Fatalf("recv_stdout after await = %s, %v", chunk.Display(), err)
		}
		if chunk.Tag() == value.Nil {
			break
		}
		total += len(chunk.AsStr())
	}
	if total < 100_000 {
		t.Fatalf("buffered stdout bytes = %d, expected full retained output", total)
	}
}

func TestStartedPipeOverflowTerminatesProcess(t *testing.T) {
	saved := maxStartedPipeBufferBytes
	maxStartedPipeBufferBytes = 32 << 10
	defer func() { maxStartedPipeBufferBytes = saved }()
	pv, err := builtinStart([]value.Value{
		value.MakeStr("cmd"), value.MakeStr("/d"), value.MakeStr("/q"), value.MakeStr("/c"),
		value.MakeStr("(for /L %i in (1,1,10000) do @echo 012345678901234567890123456789) & ping 127.0.0.1 -n 30 >nul"),
		mkMap(value.MakeStr("stdout_pipe"), value.MakeBool(true)),
	})
	if err != nil || pv.IsErr() {
		t.Fatalf("start = %s, %v", pv.Display(), err)
	}
	done := make(chan value.Value, 1)
	go func() {
		v, _ := builtinAwait([]value.Value{pv})
		done <- v
	}()
	select {
	case got := <-done:
		if !got.IsErr() || got.ErrCode() != 137 || !strings.Contains(got.ErrMsg(), "unread pipe output") {
			t.Fatalf("pipe overflow await = %s", got.Display())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("started process was not terminated after unread pipe overflow")
	}
}

func TestStartedPipeFastExitOverflowIsReported(t *testing.T) {
	saved := maxStartedPipeBufferBytes
	maxStartedPipeBufferBytes = 8 << 10
	defer func() { maxStartedPipeBufferBytes = saved }()
	for i := 0; i < 3; i++ {
		pv, err := builtinStart([]value.Value{
			value.MakeStr("cmd"), value.MakeStr("/d"), value.MakeStr("/q"), value.MakeStr("/c"),
			value.MakeStr("for /L %i in (1,1,2000) do @echo 012345678901234567890123456789"),
			mkMap(value.MakeStr("stdout_pipe"), value.MakeBool(true)),
		})
		if err != nil || pv.IsErr() {
			t.Fatalf("start %d = %s, %v", i, pv.Display(), err)
		}
		got, err := builtinAwait([]value.Value{pv})
		if err != nil {
			t.Fatalf("await %d: %v", i, err)
		}
		if !got.IsErr() || got.ErrCode() != 137 || !strings.Contains(got.ErrMsg(), "unread pipe output") {
			t.Fatalf("fast-exit pipe overflow %d = %s", i, got.Display())
		}
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
$ok := stream_lines("cmd", "/c", "echo a& echo b& echo c", |$l| push($lines, trim($l)))
say($ok, len($lines))`)
	if !strings.Contains(out, "true 3") {
		t.Errorf("stream_lines should stream 3 lines and succeed: %q", out)
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
	out := runWithEnv(t, NewEnv(), `$r := stream_lines("powershell", "-NoProfile", "-Command", "[Console]::Out.Write('x' * 5000000)", |$l| 0)
say(is_err($r))`)
	if !strings.Contains(out, "true") {
		t.Errorf("stream_lines on a >4MB line should return an Err, got %q", out)
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
