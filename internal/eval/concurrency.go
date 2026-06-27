package eval

import (
	"fmt"
	"sync"

	"github.com/anafalanx/drang/internal/value"
)

// Chan carries Values between goroutines. It is an intentionally SHARED reference
// type (DeepCopy returns itself) — send copies the value, not the channel. To
// make close safe from any goroutine even while producers may still be sending,
// close signals via a separate `done` channel and the data channel `ch` is NEVER
// closed — so a concurrent send and close never race on the same Go channel.
type Chan struct {
	ch      chan value.Value
	done    chan struct{} // closed by close(); the data channel itself is never closed
	closeMu sync.Mutex
	closed  bool
}

func (c *Chan) TypeName() string { return "channel" }
func (c *Chan) Display() string  { return "<channel>" }
func (c *Chan) Len() int         { return len(c.ch) } // buffered values currently queued

func (c *Chan) Equal(o value.Obj) bool {
	other, ok := o.(*Chan)
	return ok && other == c
}

func (c *Chan) DeepCopy(visited map[value.Obj]value.Obj) value.Obj { return c }

func (c *Chan) doClose() {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	if !c.closed {
		c.closed = true
		close(c.done)
	}
}

// send queues v, blocking until a receiver takes it or the channel is closed; a
// send on a closed channel is a catchable error — no race, no panic.
func (c *Chan) send(v value.Value) error {
	select {
	case <-c.done:
		return fmt.Errorf("send on a closed channel")
	default:
	}
	select {
	case c.ch <- v:
		return nil
	case <-c.done:
		return fmt.Errorf("send on a closed channel")
	}
}

// recv returns the next value, preferring still-buffered values even after close;
// it returns (nil, false) once the channel is closed and drained.
func (c *Chan) recv() (value.Value, bool) {
	select {
	case v := <-c.ch:
		return v, true
	case <-c.done:
		select {
		case v := <-c.ch:
			return v, true
		default:
			return value.MakeNil(), false
		}
	}
}

// Task is a handle to a spawned goroutine; join blocks for its (copy-isolated)
// result. Also an intentionally shared reference type.
type Task struct {
	done chan struct{}
	res  value.Value
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
		if n = args[0].AsInt(); n < 0 {
			n = 0
		}
	}
	return value.MakeObj(value.Chan, &Chan{ch: make(chan value.Value, n), done: make(chan struct{})}), nil
}

// builtinSend copies v (copy-on-send) and sends it; returns true, or an Err if
// the channel is closed.
func builtinSend(args []value.Value) (value.Value, error) {
	if len(args) != 2 {
		return value.MakeNil(), fmt.Errorf("send expects 2 arguments (channel, value), got %d", len(args))
	}
	c, errv, ok := chanArg("send", args[0])
	if !ok {
		return errv, nil
	}
	if err := c.send(value.DeepCopyValue(args[1], map[value.Obj]value.Obj{})); err != nil {
		return value.MakeErr(err.Error(), 1), nil
	}
	return value.MakeBool(true), nil
}

// builtinRecv blocks for the next value; a closed, empty channel yields undef.
func builtinRecv(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("recv expects 1 argument (channel), got %d", len(args))
	}
	c, errv, ok := chanArg("recv", args[0])
	if !ok {
		return errv, nil
	}
	v, open := c.recv()
	if !open {
		return value.MakeNil(), nil
	}
	return v, nil
}

// builtinRecvOk is recv with a closed-flag: returns [value, ok].
func builtinRecvOk(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("recv_ok expects 1 argument (channel), got %d", len(args))
	}
	c, errv, ok := chanArg("recv_ok", args[0])
	if !ok {
		return errv, nil
	}
	v, open := c.recv()
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
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("drain expects 1 argument (channel), got %d", len(args))
	}
	c, errv, ok := chanArg("drain", args[0])
	if !ok {
		return errv, nil
	}
	var out []value.Value
	for {
		v, ok := c.recv()
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
	if len(args) < 1 {
		return value.MakeNil(), fmt.Errorf("spawn expects at least a function")
	}
	fn, ok := asFunction(args[0])
	if !ok {
		return value.MakeErr(fmt.Sprintf("spawn expects a function, got %s", args[0].TypeName()), 1), nil
	}
	// Run over an isolated snapshot of the captured env so the goroutine never
	// races the main goroutine's ongoing top-level defines/sets.
	worker := &Function{Name: fn.Name, Params: fn.Params, Body: fn.Body, Env: fn.Env.snapshot(), Proto: fn.Proto}
	callArgs := make([]value.Value, len(args)-1)
	for i, a := range args[1:] {
		callArgs[i] = value.DeepCopyValue(a, map[value.Obj]value.Obj{})
	}
	t := &Task{done: make(chan struct{})}
	go func() {
		defer close(t.done)
		defer func() {
			if r := recover(); r != nil {
				t.res = value.MakeErr(fmt.Sprintf("spawned task panicked: %v", r), 1)
			}
		}()
		v, err := callFunction(worker, callArgs)
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
			t.res = value.MakeErr(err.Error(), 1)
		}
	}()
	return value.MakeObj(value.Task, t), nil
}

// builtinAwait blocks for a task's result (deep-copied out). Idempotent. If the
// task produced an Err, await returns it (so await(t)? propagates).
func builtinAwait(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("await expects 1 argument (a task or process), got %d", len(args))
	}
	switch args[0].Tag() {
	case value.Task:
		t := args[0].Obj().(*Task)
		<-t.done
		return value.DeepCopyValue(t.res, map[value.Obj]value.Obj{}), nil
	case value.Proc:
		p := args[0].Obj().(*Proc)
		<-p.done
		return p.res, nil // exit status (true / Err); a scalar-or-error, no copy needed
	default:
		return value.MakeErr(fmt.Sprintf("await expects a task or process, got %s", args[0].TypeName()), 1), nil
	}
}
