package eval

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/anafalanx/drang/internal/value"
)

const activeProcessLimitHelperEnv = "DRANG_ACTIVE_PROCESS_LIMIT_HELPER"

// activeProcessLimitHelper runs as the root of a capped job. It attempts one direct CreateProcess
// via os/exec, then stays alive long enough that only the monitor's reaction to the kernel's
// ACTIVE_PROCESS_LIMIT notification can finish the test promptly.
func activeProcessLimitHelper() {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = replaceEnv(os.Environ(), activeProcessLimitHelperEnv, "")
	if err := cmd.Start(); err == nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	time.Sleep(20 * time.Second)
}

// TestLimitOptionsRejectBadValues: a negative or unknown resource-limit option is a wrong-usage
// abort (a Go error), like the other exec options.
func TestLimitOptionsRejectBadValues(t *testing.T) {
	for _, src := range []string{
		`run("cmd","/c","echo x",{timeout: 9223372036854775807})`,
		`run("cmd","/c","echo x",{max_cpu: -5})`,
		`run("cmd","/c","echo x",{max_memory: -1})`,
		`run("cmd","/c","echo x",{max_cpu: 9223372036854775807})`,
		`run("cmd","/c","echo x",{max_job_procs: 4294967296})`,
		`run("cmd","/c","echo x",{max_bogus: 1})`,
	} {
		for _, vm := range []bool{false, true} {
			if _, err := runBackend(t, src, vm); err == nil {
				t.Errorf("vm=%v: expected an abort for %q", vm, src)
			}
		}
	}
}

// TestLimitOptionsAcceptedByAllForms: a valid cap is accepted (parses + runs) on every exec form.
// The command is trivial so no breach fires; this just proves the option threads through.
func TestLimitOptionsAcceptedByAllForms(t *testing.T) {
	assertBoth(t, `say(capture("cmd","/c","echo hi",{max_memory: 50000000}))`, "hi\n")
	assertBoth(t, `say(capture_all(["cmd","/c","echo hi"],{max_job_procs: 8}).ok)`, "true\n")
	assertBoth(t, `say(run("cmd","/c","exit 0",{max_cpu: 5000}))`, "true\n") // run inherits stdio: use a no-output cmd
}

// cpuBurn is a cmd busy-loop that consumes far more than a few hundred ms of user CPU, so a small
// max_cpu cap deterministically trips before it finishes.
const cpuBurn = `["cmd","/c","for /L %i in (1,1,200000000) do @rem"]`

// TestCPULimitBreach: a CPU-time cap terminates the child and surfaces a catchable Err with code
// 137 whose message names the limit (the hybrid Monitor attribution).
func TestCPULimitBreach(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real CPU-burning process; runs in the full (nightly) suite, not -short")
	}
	out := run(t, `$r := run(`+cpuBurn+`, {max_cpu: 200})
say(err_code($r))
say(err_msg($r))`)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 || lines[0] != "137" {
		t.Fatalf("expected code 137, got %q", out)
	}
	if !strings.Contains(lines[1], "CPU-time") {
		t.Errorf("breach message should name the CPU-time limit, got %q", lines[1])
	}
}

// TestStartAcceptsLimits: limits are honored on a started (detached) process too, and the breach
// surfaces via await(proc) — the maintainer's decision that start() carries limits.
func TestStartAcceptsLimits(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real CPU-burning process; runs in the full (nightly) suite, not -short")
	}
	out := run(t, `$p := start(`+cpuBurn+`, {max_cpu: 200})
say(is_err(await($p)))`)
	if strings.TrimSpace(out) != "true" {
		t.Errorf("expected the started process's CPU cap to breach and await to surface it, got %q", out)
	}
}

// An active-process cap rejects the attempted grandchild but does not itself guarantee that the
// root exits. The monitor must classify that event, terminate the tree, and report the configured
// cap rather than leaving the root in its trailing ping loop.
func TestActiveProcessLimitEventTerminatesJob(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(activeProcessLimitHelperEnv, "1")
	opts := value.MakeMap()
	om := opts.Obj().(*value.OrderedMap)
	om.Set(value.MakeStr("max_job_procs"), value.MakeInt(1))
	om.Set(value.MakeStr("timeout"), value.MakeInt(5000)) // test backstop; the event should win
	result, err := builtinCapture([]value.Value{
		value.MakeStr(exe),
		opts,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsErr() || result.ErrCode() != 137 {
		t.Fatalf("active-process breach = %s, want Err code 137", result.Display())
	}
	if !strings.Contains(result.ErrMsg(), "process-count") {
		t.Fatalf("active-process breach did not name its limit: %q", result.ErrMsg())
	}
}
