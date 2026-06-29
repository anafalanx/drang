//go:build !windows

package eval

import (
	"os/exec"
	"syscall"
)

// NOTE — UNIX SUPERVISION IS NOT YET VALIDATED AT RUNTIME. This file and reap_unix.go build
// and vet cleanly on linux/darwin (amd64 + arm64) and are a direct translation of the proven
// Windows path, but they have NOT been executed on a real Unix host. Before {supervise: true}
// is trusted on Unix, the Windows adversarial battery in cmd/drang/supervise_*_test.go (15
// test functions: tree-kill, grandchildren, concurrency, churn, fd hygiene, the reaper-killed
// ceiling, ...) MUST be ported and run on Linux and macOS. Verify in particular: the Setsid
// reaper detach; Setpgid + kill(-pid) whole-TREE reap (not just the direct child); pipe EOF
// firing on every death mode incl. SIGKILL; fds staying close-on-exec so the write end is
// never pinned open by a workload child; and the foreground-`run` SIGTTIN exclusion holding.
// Tracked in ROADMAP.md.

// detachReaper puts the reaper in its own session (new session leader, no controlling
// terminal). That decouples it from our process group, so a signal sent to our group — or a
// kill of our group — does not take the reaper down before it can do its job. A plain kill of
// our PID alone already leaves the reaper alive (it is just reparented), so this only hardens
// the group-kill case.
func detachReaper(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}

// setSuperviseAttr makes a supervised child its own process-group leader (Setpgid with a zero
// Pgid means "new group whose id is the child's pid"). The reaper then kills the whole
// descendant tree with kill(-pid, SIGKILL) — closing the Unix tree-kill gap, since killTree
// here otherwise only kills the direct child.
func setSuperviseAttr(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}
