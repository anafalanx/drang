package eval

import (
	"fmt"
	"io"
	"os"

	"github.com/anafalanx/drang/internal/value"
)

// evalDispatch implements the argv-driven task runner. It is exit-terminal: on
// the normal path it calls os.Exit with the resolved code and does not return.
// A malformed task table is an aborting error returned to the caller instead.
func evalDispatch(args []value.Value, env *Env) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("dispatch expects 1 argument (a map of tasks), got %d", len(args))
	}
	if args[0].Tag() != value.Map {
		return value.MakeNil(), fmt.Errorf("dispatch expects a map of tasks, got %s", args[0].TypeName())
	}
	code, err := dispatchResolve(args[0].Obj().(*value.OrderedMap), dispatchArgs(env))
	if err != nil {
		return value.MakeNil(), err
	}
	os.Exit(code)
	return value.MakeNil(), nil // unreachable
}

// dispatchResolve selects and runs the task named by argv[0], returning the
// process exit code. Split out (no os.Exit) so it is unit-testable.
func dispatchResolve(tasks *value.OrderedMap, argv []string) (int, error) {
	if len(argv) == 0 || argv[0] == "--list" || argv[0] == "-l" || argv[0] == "list" {
		listTasks(stdout, tasks) // the listing IS the requested result
		return 0, nil
	}
	name := argv[0]
	taskVal, ok := tasks.Get(value.MakeStr(name))
	if !ok {
		fmt.Fprintf(stderr, "drang: unknown task %q\n", name)
		listTasks(stderr, tasks) // part of the error diagnostic, not output
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
	result, err := callFunction(fn, callArgs)
	if err != nil {
		fmt.Fprintln(stderr, "drang:", err)
		return ExitCode(err), nil
	}
	if result.IsErr() {
		fmt.Fprintln(stderr, "drang:", result.ErrMsg())
		return clampCode(result.ErrCode()), nil
	}
	return 0, nil
}

func listTasks(w io.Writer, tasks *value.OrderedMap) {
	fmt.Fprintln(w, "tasks:")
	for _, k := range tasks.Keys() {
		fmt.Fprintln(w, "  "+k.Display())
	}
}

func dispatchArgs(env *Env) []string {
	v, ok := env.get("ARGV")
	if !ok || v.Tag() != value.Arr {
		return nil
	}
	elems := v.Obj().(*value.Array).Elems
	out := make([]string, len(elems))
	for i, e := range elems {
		out[i] = e.Display()
	}
	return out
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
