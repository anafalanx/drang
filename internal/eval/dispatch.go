package eval

import (
	"fmt"
	"io"

	"github.com/anafalanx/drang/internal/value"
)

// evalDispatch implements the argv-driven task runner. It is exit-terminal: on
// the normal path it returns the same exitSignal as exit(), which unwinds through
// functions/VM frames to the CLI. The CLI alone owns os.Exit; embedders and tests
// can observe the requested code without dispatch terminating their host process.
// A malformed task table or output failure is an aborting error instead.
func evalDispatch(args []value.Value, env *Env) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("dispatch expects 1 argument (a map of tasks), got %d", len(args))
	}
	if args[0].Tag() != value.Map {
		return value.MakeNil(), fmt.Errorf("dispatch expects a map of tasks, got %s", args[0].TypeName())
	}
	argv, err := dispatchArgs(env)
	if err != nil {
		return value.MakeNil(), err
	}
	code, err := dispatchResolve(args[0].Obj().(*value.OrderedMap), argv)
	if err != nil {
		return value.MakeNil(), err
	}
	return value.MakeNil(), exitSignal{code: code}
}

// dispatchResolve selects and runs the task named by argv[0], returning the
// process exit code. Errors from dispatch's own output remain Go errors: a broken
// stdout/stderr must not turn a requested listing or diagnostic into apparent success.
func dispatchResolve(tasks *value.OrderedMap, argv []string) (int, error) {
	if len(argv) == 0 || argv[0] == "--list" || argv[0] == "-l" || argv[0] == "list" {
		if err := listTasks(stdout, tasks); err != nil { // the listing IS the requested result
			return 0, err
		}
		return 0, nil
	}
	name := argv[0]
	taskVal, ok := tasks.Get(value.MakeStr(name))
	if !ok {
		if _, err := fmt.Fprintf(stderr, "drang: unknown task %q\n", name); err != nil {
			return 0, fmt.Errorf("dispatch: write unknown-task diagnostic: %w", err)
		}
		if err := listTasks(stderr, tasks); err != nil { // part of the error diagnostic, not output
			return 0, err
		}
		return 2, nil
	}
	fn, ok := asFunction(taskVal)
	if !ok {
		return 0, fmt.Errorf("dispatch: task %q is not a function (it is a %s)", name, taskVal.TypeName())
	}
	rest := make([]value.Value, len(argv)-1)
	for i, a := range argv[1:] {
		rest[i] = value.MakeStr(a)
	}
	var callArgs []value.Value
	switch len(fn.Params) {
	case 0:
		// task ignores its args
	case 1:
		callArgs = []value.Value{value.MakeArray(rest)}
	default:
		return 0, fmt.Errorf("dispatch: task %q must take 0 or 1 parameter, got %d", name, len(fn.Params))
	}
	result, err := callFunction(fn, callArgs, 0) // a dispatched task is a fresh top-level entry (depth 0)
	if err != nil {
		if code, ok := ExitRequested(err); ok {
			return code, nil // exit()/die() already carry their terminal status
		}
		if _, writeErr := fmt.Fprintln(stderr, "drang:", err); writeErr != nil {
			return 0, fmt.Errorf("dispatch: write task error: %w", writeErr)
		}
		return ExitCode(err), nil
	}
	if result.IsErr() {
		if _, err := fmt.Fprintln(stderr, "drang:", result.ErrMsg()); err != nil {
			return 0, fmt.Errorf("dispatch: write task error: %w", err)
		}
		return clampCode(result.ErrCode()), nil
	}
	return 0, nil
}

func listTasks(w io.Writer, tasks *value.OrderedMap) error {
	remaining := maxStringBytes - int64(len("tasks:\n"))
	if remaining < 0 {
		return fmt.Errorf("dispatch: task list exceeds the %d-byte string limit", maxStringBytes)
	}
	names := make([]string, 0, tasks.Len())
	for _, k := range tasks.Keys() {
		if remaining < int64(len("  \n")) {
			return fmt.Errorf("dispatch: task list exceeds the %d-byte string limit", maxStringBytes)
		}
		remaining -= int64(len("  \n"))
		var name string
		if k.Tag() == value.Str {
			name = k.AsStr()
			if int64(len(name)) > remaining {
				return fmt.Errorf("dispatch: task list exceeds the %d-byte string limit", maxStringBytes)
			}
		} else {
			var ok bool
			name, ok = displayWithin(k, remaining)
			if !ok {
				return fmt.Errorf("dispatch: task list exceeds the %d-byte string limit", maxStringBytes)
			}
		}
		names = append(names, name)
		remaining -= int64(len(name))
	}
	if _, err := fmt.Fprintln(w, "tasks:"); err != nil {
		return fmt.Errorf("dispatch: write task list: %w", err)
	}
	for _, name := range names {
		if _, err := fmt.Fprintln(w, "  "+name); err != nil {
			return fmt.Errorf("dispatch: write task list: %w", err)
		}
	}
	return nil
}

func dispatchArgs(env *Env) ([]string, error) {
	v, ok := env.get("ARGV")
	if !ok || v.Tag() != value.Arr {
		return nil, nil
	}
	elems := v.Obj().(*value.Array).Elems
	if len(elems) > maxCollectionItems {
		return nil, fmt.Errorf("dispatch: ARGV exceeds the %d-element collection limit", maxCollectionItems)
	}
	out := make([]string, 0, len(elems))
	remaining := maxStringBytes
	for _, e := range elems {
		s, ok := displayWithin(e, remaining)
		if !ok {
			return nil, fmt.Errorf("dispatch: ARGV exceeds the %d-byte string limit", maxStringBytes)
		}
		out = append(out, s)
		remaining -= int64(len(s))
	}
	return out, nil
}

// clampCode coerces an Err code into a valid process exit status (1..255),
// defaulting a zero/negative code to 1.
func clampCode(c int64) int {
	if c <= 0 {
		return 1
	}
	if c > 255 {
		return 255
	}
	return int(c)
}
