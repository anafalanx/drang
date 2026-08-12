package eval

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/anafalanx/drang/internal/value"
)

const (
	// A buffered channel stores Value headers plus the referenced payload graph.
	// 65,536 is ample for ordinary pipelines while preventing a user-supplied
	// capacity from turning one channel into an effectively unbounded queue.
	maxChannelCapacity int64 = 65_536

	// Spawn is intentionally the lower-level concurrency primitive, but it still
	// needs a process-wide ceiling so an accidental loop cannot create unbounded
	// goroutines. The failure is returned as a catchable resource-limit Err.
	maxLiveSpawnTasks int64 = 1_024
)

var (
	// liveSpawnTasks counts accepted spawn calls until their goroutine has fully
	// published its result. The quota is process-wide; scheduling/deadlock state
	// below is deliberately per execution context.
	liveSpawnTasks atomic.Int64
)

// executionContext tracks drang execution strands belonging to one run/session.
// A strand is runnable while it may make language-level progress; blocking on a
// drang channel or a same-context task temporarily removes it. Sleep/process I/O
// remain runnable because an external event can make that strand a future channel
// counterparty. When no strand can progress, every unmatched channel waiter and
// same-context task awaiter is atomically claimed and woken with a catchable
// deadlock result.
type executionContext struct {
	mu         sync.Mutex
	runnable   int
	mainActive bool
	waiters    map[*channelWaiter]struct{}
	awaiters   map[*executionSuspension]struct{}
}

// executionStrand is a stable identity for one evaluator strand. Lexical child
// scopes and nested calls retain it; each spawn and parallel pmap worker receives
// a distinct token. The non-zero field guarantees pointer identity is unique.
type executionStrand struct{ marker byte }

func newExecutionStrand() *executionStrand { return &executionStrand{marker: 1} }

// executionSuspension is an idempotent lease for one temporarily blocked
// strand. Its active bit is protected by ctx.mu. Completion paths can consume
// the lease while relinquishing their own runnable slot under that same lock,
// so there is never a transient runnable==0 window between a worker completing
// and the caller it wakes becoming eligible to run.
type executionSuspension struct {
	ctx      *executionContext
	active   bool
	deadlock func() // non-nil only for a same-context task await
}

type channelWaiter struct {
	ctx      *executionContext
	active   bool // protected by ctx.mu
	counted  bool // blocking this waiter removed one runnable strand
	deadlock func()
	pending  func() bool // true while another caller is already entering this channel
}

func newExecutionContext() *executionContext {
	return &executionContext{
		waiters:  make(map[*channelWaiter]struct{}),
		awaiters: make(map[*executionSuspension]struct{}),
	}
}

// beginRun returns true only to the outer entry on this execution context. The
// VM-to-walker fallback is synchronous and therefore must not count the same
// caller twice.
func (x *executionContext) beginRun() bool {
	if x == nil {
		return false
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.mainActive {
		return false
	}
	x.mainActive = true
	x.runnable++
	return true
}

func (x *executionContext) endRun(owned bool) {
	if x == nil || !owned {
		return
	}
	x.mu.Lock()
	x.mainActive = false
	if x.runnable > 0 {
		x.runnable--
	}
	wake := x.claimDeadlockedLocked()
	x.mu.Unlock()
	deliverDeadlocks(wake)
}

func (x *executionContext) addRunnable(n int) {
	if x == nil || n <= 0 {
		return
	}
	x.mu.Lock()
	x.runnable += n
	x.mu.Unlock()
}

func (x *executionContext) exitRunnable() {
	x.exitRunnableAndResume()
}

func (x *executionContext) exitRunnableAndResume(suspensions ...*executionSuspension) {
	if x == nil {
		return
	}
	x.mu.Lock()
	if x.runnable > 0 {
		x.runnable--
	}
	for _, suspension := range suspensions {
		if suspension == nil || suspension.ctx != x || !suspension.active {
			continue
		}
		suspension.active = false
		delete(x.awaiters, suspension)
		x.runnable++
	}
	wake := x.claimDeadlockedLocked()
	x.mu.Unlock()
	deliverDeadlocks(wake)
}

// suspend removes the current strand while it waits for a same-context task or
// worker group. The returned lease can be resumed by either the completion path
// or the caller; only the first wins.
func (x *executionContext) suspend() *executionSuspension {
	return x.suspendWithDeadlock(nil)
}

// suspendAwait is the task-waiting counterpart of suspend. Registering the
// suspension lets the scheduler wake dependency cycles (including self-await)
// once every strand in the execution context is waiting. The callback must not
// block; await supplies a one-element notification channel.
func (x *executionContext) suspendAwait(deadlock func()) *executionSuspension {
	if deadlock == nil {
		return x.suspend()
	}
	return x.suspendWithDeadlock(deadlock)
}

func (x *executionContext) suspendWithDeadlock(deadlock func()) *executionSuspension {
	if x == nil {
		return nil
	}
	x.mu.Lock()
	if x.runnable <= 0 {
		x.mu.Unlock()
		return nil
	}
	x.runnable--
	suspension := &executionSuspension{ctx: x, active: true, deadlock: deadlock}
	if deadlock != nil {
		x.awaiters[suspension] = struct{}{}
	}
	wake := x.claimDeadlockedLocked()
	x.mu.Unlock()
	deliverDeadlocks(wake)
	return suspension
}

func (x *executionContext) resume(suspension *executionSuspension) {
	if x == nil || suspension == nil || suspension.ctx != x {
		return
	}
	x.mu.Lock()
	if suspension.active {
		suspension.active = false
		delete(x.awaiters, suspension)
		x.runnable++
	}
	x.mu.Unlock()
}

// blockChannel registers w and removes its caller from the runnable count in one
// context lock. Callers hold only the channel lock, establishing the sole lock
// order channel -> context; context wake paths never acquire a channel lock.
func (x *executionContext) blockChannel(w *channelWaiter) {
	w.ctx = x
	x.mu.Lock()
	w.active = true
	w.counted = x.runnable > 0
	if w.counted {
		x.runnable--
	}
	x.waiters[w] = struct{}{}
	x.mu.Unlock()
}

// wakeChannel claims exactly one registered waiter. Making its strand runnable
// happens under the same lock as removing it, so close/match/deadlock races can
// never double-adjust the count.
func (x *executionContext) wakeChannel(w *channelWaiter, deliver func()) bool {
	x.mu.Lock()
	if !w.active {
		x.mu.Unlock()
		return false
	}
	w.active = false
	delete(x.waiters, w)
	if w.counted {
		x.runnable++
	}
	x.mu.Unlock()
	deliver()
	return true
}

func (x *executionContext) claimDeadlockedLocked() []func() {
	if x.runnable != 0 || (len(x.waiters) == 0 && len(x.awaiters) == 0) {
		return nil
	}
	wake := make([]func(), 0, len(x.waiters)+len(x.awaiters))
	channelEntryPending := false
	channelWaiterClaimed := false
	for w := range x.waiters {
		if !w.active {
			delete(x.waiters, w)
			continue
		}
		if w.pending != nil && w.pending() {
			channelEntryPending = true
			continue // an operation already entering this channel may match it
		}
		w.active = false
		delete(x.waiters, w)
		if w.counted {
			x.runnable++
		}
		channelWaiterClaimed = true
		wake = append(wake, w.deadlock)
	}
	// A channel caller in its entry window may still make another strand
	// runnable. Defer task-cycle decisions until that operation has either
	// matched or queued; finishPending will recheck this context.
	// Prefer the more specific channel error whenever waking a channel waiter is
	// enough to restore progress: its owning task can then finish normally and
	// publish that result to awaiters. Task waits are cycles only when no channel
	// operation was available to break the zero-runnable state.
	if !channelEntryPending && !channelWaiterClaimed {
		for suspension := range x.awaiters {
			if !suspension.active {
				delete(x.awaiters, suspension)
				continue
			}
			suspension.active = false
			delete(x.awaiters, suspension)
			x.runnable++
			wake = append(wake, suspension.deadlock)
		}
	}
	return wake
}

func (x *executionContext) recheck() {
	if x == nil {
		return
	}
	x.mu.Lock()
	wake := x.claimDeadlockedLocked()
	x.mu.Unlock()
	deliverDeadlocks(wake)
}

func deliverDeadlocks(wake []func()) {
	for _, deliver := range wake {
		deliver() // waiter result channels are buffered; delivery cannot block
	}
}

func (w *channelWaiter) wake(deliver func()) bool {
	return w.ctx.wakeChannel(w, deliver)
}

func tryAcquireSpawnTask() bool {
	for {
		live := liveSpawnTasks.Load()
		if live >= maxLiveSpawnTasks {
			return false
		}
		if liveSpawnTasks.CompareAndSwap(live, live+1) {
			return true
		}
	}
}

// Chan carries Values between goroutines. It is an intentionally SHARED reference
// type (DeepCopy returns itself) — send copies the value, not the channel.
//
// The state below implements the small subset of channel operations drang exposes
// instead of racing a send against close in a Go select. Every match, buffer enqueue,
// and close transition is ordered by mu, so close is a real linearization point:
// a send either committed before it or returns "send on a closed channel". Buffered
// values committed before close remain drainable.
type Chan struct {
	mu        sync.Mutex
	pending   atomic.Int64 // callers that entered send/recv but have not queued yet
	capacity  int
	queue     []value.Value
	senders   []*chanSender
	receivers []*chanReceiver
	closed    bool
}

type chanSender struct {
	v    value.Value
	done chan chanSendResult
	wait channelWaiter
}

type chanSendResult uint8

const (
	chanSendCommitted chanSendResult = iota
	chanSendClosed
	chanSendDeadlocked
)

type chanReceive struct {
	v        value.Value
	open     bool
	deadlock bool
}

type chanReceiver struct {
	done chan chanReceive
	wait channelWaiter
}

func (c *Chan) TypeName() string { return "channel" }
func (c *Chan) Display() string  { return "<channel>" }
func (c *Chan) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.queue) // buffered values currently queued; unbuffered channels stay at zero
}

func (c *Chan) Equal(o value.Obj) bool {
	other, ok := o.(*Chan)
	return ok && other == c
}

func (c *Chan) DeepCopy(visited map[value.Obj]value.Obj) value.Obj { return c }

func (c *Chan) finishPending() {
	if c.pending.Add(-1) != 0 {
		return
	}
	c.mu.Lock()
	contexts := make(map[*executionContext]struct{})
	for _, s := range c.senders {
		if s.wait.ctx != nil {
			contexts[s.wait.ctx] = struct{}{}
		}
	}
	for _, r := range c.receivers {
		if r.wait.ctx != nil {
			contexts[r.wait.ctx] = struct{}{}
		}
	}
	c.mu.Unlock()
	for ctx := range contexts {
		ctx.recheck()
	}
}

func (s *chanSender) wake(result chanSendResult) bool {
	return s.wait.wake(func() { s.done <- result })
}

func (r *chanReceiver) wake(result chanReceive) bool {
	return r.wait.wake(func() { r.done <- result })
}

func (c *Chan) doClose() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	senders, receivers := c.senders, c.receivers
	c.senders = nil
	c.receivers = nil
	c.mu.Unlock()

	// Waiter channels are buffered, so notification never blocks close. The
	// operations were already ordered by the state transition under mu.
	for _, s := range senders {
		s.wake(chanSendClosed)
	}
	for _, r := range receivers {
		r.wake(chanReceive{v: value.MakeNil(), open: false})
	}
}

// send queues v, blocking until a receiver takes it or the channel is closed; a
// send on a closed channel — or a send that could only ever deadlock (no receiver
// and no other running task) — is a catchable error, never a race or a panic.
func (c *Chan) send(v value.Value) error { return c.sendContext(v, nil) }

func (c *Chan) sendContext(v value.Value, ctx *executionContext) error {
	c.pending.Add(1)
	pending := true
	finishPending := func() {
		if pending {
			pending = false
			c.finishPending()
		}
	}
	defer finishPending()

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("send on a closed channel")
	}
	for len(c.receivers) > 0 {
		r := c.receivers[0]
		c.receivers[0] = nil
		c.receivers = c.receivers[1:]
		// Buffered completion makes this non-blocking under mu. The match is now
		// committed and therefore ordered before any subsequent close.
		if r.wake(chanReceive{v: v, open: true}) {
			c.mu.Unlock()
			return nil
		}
	}
	if len(c.queue) < c.capacity {
		c.queue = append(c.queue, v)
		c.mu.Unlock()
		return nil
	}
	if ctx == nil {
		c.mu.Unlock()
		return fmt.Errorf("send would deadlock: the channel has no reader and no other task is running (a receiver must run concurrently, e.g. via spawn or pmap)")
	}
	w := &chanSender{v: v, done: make(chan chanSendResult, 1)}
	w.wait.deadlock = func() { w.done <- chanSendDeadlocked }
	w.wait.pending = func() bool { return c.pending.Load() > 0 }
	c.senders = append(c.senders, w)
	ctx.blockChannel(&w.wait)
	c.mu.Unlock()
	finishPending()
	switch <-w.done {
	case chanSendCommitted:
		return nil
	case chanSendDeadlocked:
		c.removeSender(w)
		return fmt.Errorf("send would deadlock: the channel has no reader and no other runnable task can become one")
	default:
		return fmt.Errorf("send on a closed channel")
	}
}

func (c *Chan) removeSender(target *chanSender) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, s := range c.senders {
		if s == target {
			copy(c.senders[i:], c.senders[i+1:])
			c.senders[len(c.senders)-1] = nil
			c.senders = c.senders[:len(c.senders)-1]
			return
		}
	}
}

// recv returns the next value, preferring still-buffered values even after close;
// it returns (nil, false) once the channel is closed and drained. A receive that
// could only ever deadlock (no sender and no other running task) is a catchable
// error rather than a process-wide runtime abort.
func (c *Chan) recv() (value.Value, bool, error) { return c.recvContext(nil) }

func (c *Chan) recvContext(ctx *executionContext) (value.Value, bool, error) {
	c.pending.Add(1)
	pending := true
	finishPending := func() {
		if pending {
			pending = false
			c.finishPending()
		}
	}
	defer finishPending()

	c.mu.Lock()
	if len(c.queue) > 0 {
		v := c.queue[0]
		c.queue[0] = value.MakeNil() // release references held by a long-lived channel
		c.queue = c.queue[1:]
		// Moving the oldest blocked sender into the newly free buffer slot commits
		// that send before a close can acquire mu.
		for !c.closed && len(c.senders) > 0 {
			s := c.senders[0]
			c.senders[0] = nil
			c.senders = c.senders[1:]
			if s.wake(chanSendCommitted) {
				c.queue = append(c.queue, s.v)
				break
			}
		}
		c.mu.Unlock()
		return v, true, nil
	}
	for !c.closed && len(c.senders) > 0 {
		s := c.senders[0]
		c.senders[0] = nil
		c.senders = c.senders[1:]
		if s.wake(chanSendCommitted) {
			c.mu.Unlock()
			return s.v, true, nil
		}
	}
	if c.closed {
		c.mu.Unlock()
		return value.MakeNil(), false, nil
	}
	if ctx == nil {
		c.mu.Unlock()
		return value.MakeNil(), false, fmt.Errorf("recv would deadlock: the channel has no writer and no other task is running (a sender must run concurrently, e.g. via spawn or pmap)")
	}
	w := &chanReceiver{done: make(chan chanReceive, 1)}
	w.wait.deadlock = func() { w.done <- chanReceive{v: value.MakeNil(), deadlock: true} }
	w.wait.pending = func() bool { return c.pending.Load() > 0 }
	c.receivers = append(c.receivers, w)
	ctx.blockChannel(&w.wait)
	c.mu.Unlock()
	finishPending()
	r := <-w.done
	if r.deadlock {
		c.removeReceiver(w)
		return value.MakeNil(), false, fmt.Errorf("recv would deadlock: the channel has no writer and no other runnable task can become one")
	}
	return r.v, r.open, nil
}

func (c *Chan) removeReceiver(target *chanReceiver) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, r := range c.receivers {
		if r == target {
			copy(c.receivers[i:], c.receivers[i+1:])
			c.receivers[len(c.receivers)-1] = nil
			c.receivers = c.receivers[:len(c.receivers)-1]
			return
		}
	}
}

// Task is a handle to a spawned goroutine; join blocks for its (copy-isolated)
// result. Also an intentionally shared reference type.
type Task struct {
	mu        sync.Mutex
	done      chan struct{}
	res       value.Value
	completed bool
	ctx       *executionContext
	waiters   []*executionSuspension
}

func (t *Task) TypeName() string { return "task" }
func (t *Task) Display() string  { return "<task>" }
func (t *Task) Len() int         { return 0 }

func (t *Task) Equal(o value.Obj) bool {
	other, ok := o.(*Task)
	return ok && other == t
}

func (t *Task) DeepCopy(visited map[value.Obj]value.Obj) value.Obj { return t }

func chanArg(name string, v value.Value) (*Chan, value.Value, bool) {
	if v.Tag() != value.Chan {
		return nil, value.MakeErr(fmt.Sprintf("%s expects a channel, got %s", name, v.TypeName()), 1), false
	}
	return v.Obj().(*Chan), value.MakeNil(), true
}

// builtinChan makes an unbuffered channel, or a buffered one with chan(n).
func builtinChan(args []value.Value) (value.Value, error) {
	if len(args) > 1 {
		return value.MakeNil(), fmt.Errorf("chan expects 0 or 1 arguments (buffer size), got %d", len(args))
	}
	n := int64(0)
	if len(args) == 1 {
		if args[0].Tag() != value.Int {
			return value.MakeErr(fmt.Sprintf("chan buffer size must be an int, got %s", args[0].TypeName()), 1), nil
		}
		n = args[0].AsInt()
		if n < 0 {
			return value.MakeErr(fmt.Sprintf("chan buffer size must be between 0 and %d, got %d", maxChannelCapacity, n), 1), nil
		}
		if n > maxChannelCapacity {
			return value.MakeErr(fmt.Sprintf("chan buffer size must be between 0 and %d, got %d", maxChannelCapacity, n), 137), nil
		}
	}
	return value.MakeObj(value.Chan, &Chan{capacity: int(n)}), nil
}

func isConcurrencyBuiltin(name string) bool {
	switch name {
	case "await", "chan", "send", "recv", "recv_ok", "close", "drain":
		return true
	}
	return false
}

func callConcurrencyBuiltin(name string, args []value.Value, ctx *executionContext, strand *executionStrand) (value.Value, error) {
	b := func(args []value.Value) (value.Value, error) {
		switch name {
		case "await":
			return builtinAwaitContext(args, ctx, strand)
		case "chan":
			return builtinChan(args)
		case "send":
			return builtinSendContext(args, ctx)
		case "recv":
			return builtinRecvContext(args, ctx)
		case "recv_ok":
			return builtinRecvOkContext(args, ctx)
		case "close":
			return builtinCloseChan(args)
		case "drain":
			return builtinDrainContext(args, ctx)
		default:
			return value.MakeNil(), fmt.Errorf("unknown concurrency builtin %s", name)
		}
	}
	return safeBuiltin(name, b, args)
}

// builtinSend copies v (copy-on-send) and sends it; returns true, or an Err if
// the channel is closed.
func builtinSend(args []value.Value) (value.Value, error) {
	return builtinSendContext(args, nil)
}

func builtinSendContext(args []value.Value, ctx *executionContext) (value.Value, error) {
	if len(args) != 2 {
		return value.MakeNil(), fmt.Errorf("send expects 2 arguments (channel, value), got %d", len(args))
	}
	c, errv, ok := chanArg("send", args[0])
	if !ok {
		return errv, nil
	}
	copyValue, snapshotErr := newClosureSnapshot().cloneValue(args[1], 0)
	if snapshotErr != nil {
		return value.MakeErr(fmt.Sprintf("send: %v", snapshotErr), 137), nil
	}
	if err := c.sendContext(copyValue, ctx); err != nil {
		return value.MakeErr(err.Error(), 1), nil
	}
	return value.MakeBool(true), nil
}

// builtinRecv blocks for the next value; a closed, empty channel yields undef.
func builtinRecv(args []value.Value) (value.Value, error) {
	return builtinRecvContext(args, nil)
}

func builtinRecvContext(args []value.Value, ctx *executionContext) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("recv expects 1 argument (channel), got %d", len(args))
	}
	c, errv, ok := chanArg("recv", args[0])
	if !ok {
		return errv, nil
	}
	v, open, err := c.recvContext(ctx)
	if err != nil {
		return value.MakeErr(err.Error(), 1), nil
	}
	if !open {
		return value.MakeNil(), nil
	}
	return v, nil
}

// builtinRecvOk is recv with a closed-flag: returns [value, ok].
func builtinRecvOk(args []value.Value) (value.Value, error) {
	return builtinRecvOkContext(args, nil)
}

func builtinRecvOkContext(args []value.Value, ctx *executionContext) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("recv_ok expects 1 argument (channel), got %d", len(args))
	}
	c, errv, ok := chanArg("recv_ok", args[0])
	if !ok {
		return errv, nil
	}
	v, open, err := c.recvContext(ctx)
	if err != nil {
		return value.MakeErr(err.Error(), 1), nil
	}
	return value.MakeArray([]value.Value{v, value.MakeBool(open)}), nil
}

func builtinCloseChan(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("close expects 1 argument (channel), got %d", len(args))
	}
	c, errv, ok := chanArg("close", args[0])
	if !ok {
		return errv, nil
	}
	c.doClose()
	return value.MakeNil(), nil
}

// builtinDrain collects every remaining value into an array, blocking until the
// channel is closed (the producer must close it).
func builtinDrain(args []value.Value) (value.Value, error) {
	return builtinDrainContext(args, nil)
}

func builtinDrainContext(args []value.Value, ctx *executionContext) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("drain expects 1 argument (channel), got %d", len(args))
	}
	c, errv, ok := chanArg("drain", args[0])
	if !ok {
		return errv, nil
	}
	var out []value.Value
	for {
		v, ok, err := c.recvContext(ctx)
		if err != nil {
			return value.MakeErr(err.Error(), 1), nil
		}
		if !ok {
			break
		}
		out = append(out, v)
	}
	return value.MakeArray(out), nil
}

// evalSpawn runs fn (with deep-copied args) on its own goroutine, returning a
// Task handle. A worker error or panic is captured as the task's Err result.
// It is a special form (not a map builtin) because it calls callFunction.
func evalSpawn(args []value.Value) (value.Value, error) {
	return evalSpawnContext(args, nil)
}

func evalSpawnContext(args []value.Value, ctx *executionContext) (value.Value, error) {
	if len(args) < 1 {
		return value.MakeNil(), fmt.Errorf("spawn expects at least a function")
	}
	fn, ok := asFunction(args[0])
	if !ok {
		return value.MakeErr(fmt.Sprintf("spawn expects a function, got %s", args[0].TypeName()), 1), nil
	}
	if ctx == nil && fn.Env != nil {
		ctx = fn.Env.executionContext()
	}
	if ctx == nil {
		// Direct Go callers do not have an evaluator run to supply a scheduler
		// context. A private context still lets the worker turn an unmatched
		// channel operation into a catchable deadlock instead of hanging.
		ctx = newExecutionContext()
	}
	// Clone the worker closure, every reachable captured closure/container, and
	// the explicit arguments as one graph. One visited set across both sides
	// preserves aliases such as spawn(|| ..., $capturedSameArray).
	snapshot := newClosureSnapshot()
	worker, snapshotErr := snapshot.cloneFunction(fn, 0)
	if snapshotErr != nil {
		return value.MakeErr(fmt.Sprintf("spawn: %v", snapshotErr), 137), nil
	}
	callArgs := make([]value.Value, len(args)-1)
	for i, a := range args[1:] {
		callArgs[i], snapshotErr = snapshot.cloneValue(a, 0)
		if snapshotErr != nil {
			return value.MakeErr(fmt.Sprintf("spawn: %v", snapshotErr), 137), nil
		}
	}
	if !tryAcquireSpawnTask() {
		return value.MakeErr(fmt.Sprintf("spawn: too many live tasks (limit %d)", maxLiveSpawnTasks), 137), nil
	}
	t := &Task{done: make(chan struct{}), ctx: ctx}
	ctx.addRunnable(1) // count it before start so channel blocking sees the worker
	go func() {
		defer func() {
			t.mu.Lock()
			t.completed = true
			ctx.exitRunnableAndResume(t.waiters...)
			t.waiters = nil
			close(t.done)
			t.mu.Unlock()
			liveSpawnTasks.Add(-1)
		}()
		defer func() {
			if r := recover(); r != nil {
				t.res = value.MakeErr(fmt.Sprintf("spawned task panicked: %v", r), 1)
			}
		}()
		v, err := callFunction(worker, callArgs, 0) // fresh goroutine and Go stack: count depth from zero
		if err == nil {
			t.res = v
		} else if code, ok := ExitRequested(err); ok {
			// A detached task can't exit the whole process; surface the intent
			// clearly (with the requested code) instead of the internal signal text.
			ec := int64(code)
			if ec == 0 {
				ec = 1
			}
			t.res = value.MakeErr(fmt.Sprintf("exit(%d) inside a spawned task", code), ec)
		} else {
			// Any worker error — including the runaway-recursion escalation — becomes
			// the task's Err (the task model: errors surface at await). The escalation
			// resets the overflow budget as it fires, so a demoted storm cannot poison
			// the rest of the program's guard budget.
			t.res = value.MakeErr(err.Error(), 1)
		}
	}()
	return value.MakeObj(value.Task, t), nil
}

// builtinAwait blocks for a task's result (deep-copied out). Idempotent. If the
// task produced an Err, await returns it (so await(t)? propagates).
func builtinAwait(args []value.Value) (value.Value, error) {
	return builtinAwaitContext(args, nil, nil)
}

func builtinAwaitContext(args []value.Value, ctx *executionContext, strand *executionStrand) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("await expects 1 argument (a task or process), got %d", len(args))
	}
	switch args[0].Tag() {
	case value.Task:
		t := args[0].Obj().(*Task)
		t.mu.Lock()
		if !t.completed {
			var suspension *executionSuspension
			var deadlocked chan struct{}
			if ctx != nil && ctx == t.ctx {
				deadlocked = make(chan struct{}, 1)
				suspension = ctx.suspendAwait(func() { deadlocked <- struct{}{} })
				if suspension != nil {
					t.waiters = append(t.waiters, suspension)
				}
			}
			done := t.done
			t.mu.Unlock()
			if suspension == nil {
				<-done
			} else {
				select {
				case <-done:
					// Normal completion atomically resumes every same-context
					// awaiter before publishing done.
				case <-deadlocked:
					return value.MakeErr("await would deadlock: no runnable task can complete the dependency", 1), nil
				}
			}
			ctx.resume(suspension) // idempotent fallback; completion normally consumed it
		} else {
			t.mu.Unlock()
		}
		result, snapshotErr := newClosureSnapshotForStrand(strand).cloneValue(t.res, 0)
		if snapshotErr != nil {
			return value.MakeErr(fmt.Sprintf("await: %v", snapshotErr), 137), nil
		}
		return result, nil
	case value.Proc:
		p := args[0].Obj().(*Proc)
		<-p.done
		return p.res, nil // exit status (true / Err); a scalar-or-error, no copy needed
	default:
		return value.MakeErr(fmt.Sprintf("await expects a task or process, got %s", args[0].TypeName()), 1), nil
	}
}
