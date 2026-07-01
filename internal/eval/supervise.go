package eval

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// supervisor ties supervised child processes to this process's lifetime via a portable
// "babysitter pipe". On first use it spawns one reaper side-car ("drang --reap") that holds
// the read end of a pipe as its stdin while we keep the write end open for our whole life.
// We send the reaper "+ PID" / "- PID" lines as supervised children start and finish. When
// THIS process dies for ANY reason — clean exit, panic, SIGKILL, crash — the OS closes our
// file descriptors, the write end closes, the reaper's stdin hits EOF, and it kills every
// still-registered child tree. That is the cross-platform "children die with the parent"
// guarantee, with no OS job-object / pdeathsig machinery and no unsafe code.
//
// The write end must never be inherited by the workload children themselves (or EOF would
// wait on THEM, not us): os.Pipe sets close-on-exec, and exec.Cmd only un-sets it for the
// fds it explicitly assigns, so a child we never hand the write end to cannot pin it open.
type supervisor struct {
	once sync.Once
	mu   sync.Mutex
	w    *os.File // pipe write end, held open for the whole process life (never closed by us)
	ok   bool     // the reaper is running and the pipe is usable
}

var sup supervisor

// ensureReaper lazily spawns the reaper on first supervised child. Concurrency-safe via
// sync.Once, so concurrent first-supervised launches (e.g. a pmap of subprocesses) spawn
// exactly one reaper. On any failure it leaves ok=false and supervision silently degrades
// to the existing cooperative tree-kill.
func (s *supervisor) ensureReaper() {
	s.once.Do(func() {
		self, err := os.Executable()
		if err != nil {
			return
		}
		r, w, err := os.Pipe()
		if err != nil {
			return
		}
		cmd := exec.Command(self, "--reap")
		cmd.Stdin = r     // the reaper reads registrations here, and senses our death as EOF
		cmd.Stdout = nil  // discarded
		cmd.Stderr = nil  // discarded
		detachReaper(cmd) // own session/group, so it outlives us instead of dying with our group
		if startErr := cmd.Start(); startErr != nil {
			_ = r.Close()
			_ = w.Close()
			return
		}
		_ = r.Close() // only the reaper should hold the read end now
		s.mu.Lock()
		s.w = w // keep the write end open for life
		s.ok = true
		s.mu.Unlock()
	})
}

func (s *supervisor) register(pid int) {
	s.ensureReaper()
	s.send('+', pid)
}

func (s *supervisor) deregister(pid int) {
	s.send('-', pid)
}

// send writes one framed line to the reaper under the mutex (one Fprintf == one write, so
// concurrent registrations never interleave). A write error means the reaper is gone, so we
// stop trying and fall back to the cooperative tree-kill.
func (s *supervisor) send(op byte, pid int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ok || s.w == nil {
		return
	}
	if _, err := fmt.Fprintf(s.w, "%c %d\n", op, pid); err != nil {
		s.ok = false
	}
}

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

// superviseAfterStart registers a just-started supervised child with the reaper and returns
// a deregister func (always non-nil) to call once the child has been waited on. Deregistering
// a cleanly-exited child keeps the reaper from later killing a recycled PID.
func superviseAfterStart(cmd *exec.Cmd, on bool) func() {
	if !on || cmd == nil || cmd.Process == nil {
		return func() {}
	}
	pid := cmd.Process.Pid
	sup.register(pid)
	return func() { sup.deregister(pid) }
}

// runCmd runs cmd to completion, registering it with the reaper for its lifetime when
// supervised. It returns the same error cmd.Run would: the start error if it can't start,
// otherwise the wait (exit) error. When not supervised it IS cmd.Run, byte-for-byte.
func runCmd(cmd *exec.Cmd, supervise bool) error {
	if !supervise {
		return cmd.Run()
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	dereg := superviseAfterStart(cmd, true)
	defer dereg()
	return cmd.Wait()
}
