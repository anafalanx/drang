package eval

import (
	"strings"
	"testing"
)

// TestLimitOptionsRejectBadValues: a negative or unknown resource-limit option is a wrong-usage
// abort (a Go error), like the other exec options.
func TestLimitOptionsRejectBadValues(t *testing.T) {
	for _, src := range []string{
		`run("cmd","/c","echo x",{max_cpu: -5})`,
		`run("cmd","/c","echo x",{max_memory: -1})`,
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
	assertBoth(t, `say(capture_all(["cmd","/c","echo hi"],{max_procs: 8}).ok)`, "true\n")
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
