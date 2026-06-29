//go:build !windows

package main

import "syscall"

// NOTE: this Unix kill path is build/vet-verified but not yet runtime-tested — see the
// validation note in internal/eval/supervise_unix.go and the ROADMAP item.

// killProcTree kills the child's whole process group, then the pid itself as a fallback.
// Supervised children are started as their own group leader (pgid == pid; see
// internal/eval/supervise_unix.go), so kill(-pid) reaches every descendant. Best-effort:
// errors (already dead, recycled pid) are ignored.
func killProcTree(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
