//go:build windows

package main

import (
	"os/exec"
	"strconv"
	"syscall"
)

// killProcTree kills the child and all its descendants with taskkill /T, which walks the PID
// tree. Best-effort: an already-dead pid just yields a non-zero exit we ignore. The taskkill
// helper runs with no window.
func killProcTree(pid int) {
	cmd := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Run()
}
