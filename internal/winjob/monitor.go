package winjob

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobAssociateCompletionPort is JOBOBJECT_ASSOCIATE_COMPLETION_PORT. x/sys/windows v0.46.0 wraps
// the core Job Object API and the info-class id (JobObjectAssociateCompletionPortInformation) but
// not this struct or the JOB_OBJECT_MSG_* message ids, so they are declared here.
type jobAssociateCompletionPort struct {
	CompletionKey  uintptr
	CompletionPort windows.Handle
}

// JOB_OBJECT_MSG_* — the message id arrives in the "bytes transferred" field of the completion.
const (
	jobMsgEndOfJobTime        = 1
	jobMsgEndOfProcessTime    = 2
	jobMsgActiveProcessLimit  = 3
	jobMsgActiveProcessZero   = 4
	jobMsgNewProcess          = 6
	jobMsgExitProcess         = 7
	jobMsgAbnormalExitProcess = 8
	jobMsgProcessMemoryLimit  = 9
	jobMsgJobMemoryLimit      = 10
	jobMsgNotificationLimit   = 11
	jobMsgJobCycleTimeLimit   = 12
)

// EventKind classifies a job lifecycle notification.
type EventKind int

const (
	EventNewProcess    EventKind = iota // a process joined the job (Pid set)
	EventExit                           // a process exited on its own, any code (Pid set)
	EventAbnormalExit                   // a process exited with an abnormal exception status (Pid set)
	EventActiveZero                     // the job's last process exited — the subtree has drained (no Pid)
	EventProcessMemoryLimit             // a process hit its per-process memory limit (Pid set)
	EventJobMemoryLimit                 // the job hit its memory limit
	EventNotificationLimit              // a per-job notification limit was crossed
	EventJobTimeLimit                   // the job hit its CPU-time limit
	EventProcessTimeLimit               // a process hit its per-process CPU-time limit (Pid set)
	EventOther
)

// Event is one job notification. Job is the completion key (a stable per-Watch token) identifying
// which watched job it came from. Pid is set for per-process events. Msg is the raw
// JOB_OBJECT_MSG_* id.
//
// Semantics to know before building on this:
//   - EventExit fires for a process that exits on its own — including a plain nonzero exit.
//   - EventAbnormalExit fires only for the documented abnormal exception statuses (access
//     violation, stack overflow, Ctrl-C exit, ...), NOT for plain nonzero exits.
//   - A process killed by TerminateJobObject (drang's tree-kill) emits NO per-pid exit event; a
//     job-terminated subtree signals completion ONLY via EventActiveZero (which carries no Pid).
//     So drain detection after a tree-kill must key on EventActiveZero.
//   - A delivered Pid may already have exited/been recycled unless you hold a handle to it;
//     Process.Wait remains the authoritative source of a child's exit code.
type Event struct {
	Job  uintptr
	Kind EventKind
	Pid  int
	Msg  uint32
}

var errMonitorClosed = errors.New("winjob: monitor is closed")

// ioport is the monitor's completion-port handle, closed exactly once (via Close or the GC
// backstop) and guarded so an association can't race the close.
type ioport struct {
	mu     sync.Mutex
	handle windows.Handle
	closed bool
}

func (p *ioport) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		windows.CloseHandle(p.handle)
	}
}

// with runs f with the live port handle held under the lock, so a concurrent close can't pull the
// handle out mid-association; it returns errMonitorClosed once closed.
func (p *ioport) with(f func(windows.Handle) error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errMonitorClosed
	}
	return f(p.handle)
}

func (p *ioport) raw() windows.Handle {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.handle
}

// Monitor watches one or more jobs' lifecycle events through a shared I/O completion port. Because
// a process in a nested job is also in its parent jobs, a port on an outer job observes the whole
// subtree — the wiring-free up-chain monitoring the sturm supervision tree builds on. Notifications
// are best-effort (the kernel may drop them under load); Process.Wait stays the authoritative source
// of a child's exit code.
type Monitor struct {
	port    *ioport
	events  chan Event
	nextKey atomic.Uint64
}

// NewMonitor creates a completion port and starts its delivery goroutine.
func NewMonitor() (*Monitor, error) {
	h, err := windows.CreateIoCompletionPort(windows.InvalidHandle, 0, 0, 1)
	if err != nil {
		return nil, err
	}
	p := &ioport{handle: h}
	m := &Monitor{port: p, events: make(chan Event, 256)}
	// The loop references only the port and channel (NOT m), so m can be GC'd if the caller drops
	// it, letting the cleanup below fire.
	go monitorLoop(p, m.events)
	// Backstop: if the Monitor is dropped without Close(), reclaim the port — which unblocks and
	// ends the loop goroutine too. Idempotent with Close (both go through ioport.close).
	runtime.AddCleanup(m, (*ioport).close, p)
	return m, nil
}

// Watch associates job with this monitor's port so its notifications flow to Events(), returning a
// stable per-Watch token that tags this job's Events (Event.Job). Associating a port replays a
// NEW_PROCESS for every process already in the job, so Watch after Launch still sees existing
// members; Watch before Launch is preferred only to avoid the narrow race of a process that both
// starts and exits during the association. Returns an error if the monitor is closed.
func (m *Monitor) Watch(job *Job) (uintptr, error) {
	key := uintptr(m.nextKey.Add(1)) // a stable token, not the recyclable job handle value
	err := m.port.with(func(h windows.Handle) error {
		info := jobAssociateCompletionPort{CompletionKey: key, CompletionPort: h}
		_, e := windows.SetInformationJobObject(job.handle, windows.JobObjectAssociateCompletionPortInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
		return e
	})
	if err != nil {
		return 0, err
	}
	return key, nil
}

// Events is the notification stream, closed when the Monitor is closed. Consume promptly: the
// channel is buffered but overflow events are dropped (best-effort, as the OS itself is).
func (m *Monitor) Events() <-chan Event { return m.events }

// Close stops delivery and releases the completion port. Idempotent. Closing the port unblocks the
// delivery goroutine, which then closes Events().
func (m *Monitor) Close() error {
	m.port.close()
	return nil
}

func monitorLoop(port *ioport, events chan Event) {
	defer close(events)
	h := port.raw() // Close() closes this handle, which unblocks GetQueuedCompletionStatus below
	for {
		var msg uint32
		var key uintptr
		var ov *windows.Overlapped
		if err := windows.GetQueuedCompletionStatus(h, &msg, &key, &ov, windows.INFINITE); err != nil {
			return // the port was closed (Close or the GC backstop) or broke; stop delivering
		}
		ev := Event{Job: key, Msg: msg, Kind: classifyMsg(msg)}
		if isProcessMsg(msg) {
			ev.Pid = int(uintptr(unsafe.Pointer(ov))) // lpOverlapped carries the pid for process messages
		}
		select {
		case events <- ev:
		default: // best-effort: drop rather than stall draining the port
		}
	}
}

func classifyMsg(msg uint32) EventKind {
	switch msg {
	case jobMsgNewProcess:
		return EventNewProcess
	case jobMsgExitProcess:
		return EventExit
	case jobMsgAbnormalExitProcess:
		return EventAbnormalExit
	case jobMsgActiveProcessZero:
		return EventActiveZero
	case jobMsgProcessMemoryLimit:
		return EventProcessMemoryLimit
	case jobMsgJobMemoryLimit:
		return EventJobMemoryLimit
	case jobMsgNotificationLimit:
		return EventNotificationLimit
	case jobMsgEndOfJobTime:
		return EventJobTimeLimit
	case jobMsgEndOfProcessTime:
		return EventProcessTimeLimit
	default:
		return EventOther
	}
}

func isProcessMsg(msg uint32) bool {
	switch msg {
	case jobMsgNewProcess, jobMsgExitProcess, jobMsgAbnormalExitProcess, jobMsgEndOfProcessTime:
		return true
	}
	return false
}
