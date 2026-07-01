// Package winjob is drang's Windows process-supervision substrate, built on Job Objects. A
// child is launched born-in-job (assigned to its jobs by the kernel at CreateProcess time,
// before its first thread runs — race-free), giving die-with-parent, whole-tree kill, resource
// limits, accounting, and an event stream without the portable reaper side-car.
//
// Topology: drang holds one root job (KILL_ON_JOB_CLOSE) it puts itself in; every descendant
// auto-joins the root by inheritance, so nothing escapes drang's death. Each supervised command
// additionally gets its own nested job for per-command tree-kill and limits.
package winjob

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// procThreadAttributeJobList places a child into the given jobs at CreateProcess time, before
// its first thread runs — the race-free born-in-job mechanism. x/sys/windows v0.46.0 declares
// the sibling attributes (HANDLE_LIST, PARENT_PROCESS) but not this one, so we declare it here.
const procThreadAttributeJobList = 0x0002000D

// Job wraps a Windows Job Object handle. Terminate/Close/Assign are serialized by mu so a
// timeout/kill Terminate cannot race the reaping path's Close on the same (possibly recycled)
// handle; handle itself is immutable after New.
type Job struct {
	mu     sync.Mutex
	handle windows.Handle // immutable after New
	closed bool
}

var errClosedJob = errors.New("winjob: job is closed")

// New creates a Job Object. When killOnClose is set, closing the last handle to the job —
// which includes drang dying, when the kernel closes our handles — terminates every process
// still in the job (JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE): the native die-with-parent guarantee.
// The handle is deliberately NOT inheritable, so no child can pin the job open and defeat it.
func New(killOnClose bool) (*Job, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("CreateJobObject: %w", err)
	}
	if killOnClose {
		var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			windows.CloseHandle(h)
			return nil, fmt.Errorf("SetInformationJobObject(KILL_ON_JOB_CLOSE): %w", err)
		}
	}
	return &Job{handle: h}, nil
}

// Handle returns the raw job handle, for the born-in-job launcher's attribute list.
func (j *Job) Handle() windows.Handle { return j.handle }

// Assign adds an already-running process to the job. The born-in-job launcher does not need
// this — the child is placed into its jobs at spawn time — but it is how drang adopts a process
// it did not spawn through the launcher, and how the root job takes in drang itself.
func (j *Job) Assign(process windows.Handle) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return errClosedJob
	}
	return windows.AssignProcessToJobObject(j.handle, process)
}

// Terminate kills every process in the job with the given exit code. Because jobs nest, this
// also kills processes in the job's child jobs — the whole-tree kill that replaces
// `taskkill /F /T`. (Note: it kills job members + nested child jobs, NOT an arbitrary PID tree;
// containment is what guarantees the whole subtree is reached.)
func (j *Job) Terminate(exitCode uint32) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil // already released; nothing to terminate
	}
	return windows.TerminateJobObject(j.handle, exitCode)
}

// Close releases drang's handle to the job. For a killOnClose job where drang holds the only
// handle, this triggers die-with-parent. Idempotent, and it sets closed so a racing Terminate
// becomes a no-op rather than acting on the released (possibly recycled) handle.
func (j *Job) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	return windows.CloseHandle(j.handle)
}
