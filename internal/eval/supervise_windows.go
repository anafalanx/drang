//go:build windows

package eval

import (
	"os/exec"
	"syscall"
)

// detachReaper gives the reaper its own process group so a Ctrl-C / console-close event aimed
// at our group is not delivered to it before it can reap. (A plain kill of our PID leaves the
// reaper alive as a separate process; a taskkill /T of our PID would kill the reaper too, but
// in that case it also killed the workload children, so there is nothing left to reap.)
func detachReaper(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
	cmd.SysProcAttr.HideWindow = true
}

// setSuperviseAttr is a no-op on Windows: the reaper kills the tree with taskkill /T, which
// walks the child PID tree directly, so no process-group setup on the child is needed.
func setSuperviseAttr(cmd *exec.Cmd) {}
