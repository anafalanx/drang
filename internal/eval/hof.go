package eval

import (
	"fmt"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/anafalanx/drang/internal/value"
)

// hofNames are the higher-order functions handled as evaluator special forms
// (they invoke a user callback, which a plain builtin can't reach). take/drop/
// uniq take no callback and are ordinary builtins.
var hofNames = map[string]bool{
	"map": true, "filter": true, "reject": true, "each": true,
	"find": true, "any": true, "all": true, "count": true,
	"reduce": true, "flat_map": true, "pmap": true,
	"sort": true, "sort_by": true, "min_by": true, "max_by": true,
}

// evalHOF dispatches a higher-order call. Wrong argument COUNT aborts (Go error,
// per the builtin convention); a non-array source or non-function callback is a
// catchable Err value.
func evalHOF(name string, args []value.Value, env *Env) (value.Value, error) {
	if name == "reduce" {
		if len(args) != 3 {
			return value.MakeNil(), fmt.Errorf("reduce expects 3 arguments (array, init, fn), got %d", len(args))
		}
		arr, ev := hofArray(name, args[0])
		if arr == nil {
			return ev, nil
		}
		fn, ev := hofFn(name, args[2])
		if fn == nil {
			return ev, nil
		}
		return hofReduce(arr, args[1], fn)
	}

	// Ordering family: sort takes an optional comparator (1 or 2 args); the rest
	// take a key function. Handled before the generic 2-arg gate below.
	switch name {
	case "sort":
		return hofSort(name, args)
	case "sort_by":
		return hofSortBy(name, args)
	case "min_by", "max_by":
		return hofMinMaxBy(name, args)
	}

	if len(args) != 2 {
		return value.MakeNil(), fmt.Errorf("%s expects 2 arguments (array, fn), got %d", name, len(args))
	}
	arr, ev := hofArray(name, args[0])
	if arr == nil {
		return ev, nil
	}
	fn, ev := hofFn(name, args[1])
	if fn == nil {
		return ev, nil
	}
	switch name {
	case "map":
		return hofMap(arr, fn)
	case "filter":
		return hofFilter(arr, fn, true)
	case "reject":
		return hofFilter(arr, fn, false)
	case "each":
		return hofEach(args[0], arr, fn)
	case "find":
		return hofFind(arr, fn)
	case "any":
		return hofQuantify(arr, fn, true)
	case "all":
		return hofQuantify(arr, fn, false)
	case "count":
		return hofCount(arr, fn)
	case "flat_map":
		return hofFlatMap(arr, fn)
	case "pmap":
		return hofPmap(arr, fn)
	}
	return value.MakeNil(), fmt.Errorf("unknown higher-order function %s", name)
}

func hofArray(name string, v value.Value) (*value.Array, value.Value) {
	if v.Tag() != value.Arr {
		return nil, value.MakeErr(fmt.Sprintf("%s expects an array, got %s", name, v.TypeName()), 1)
	}
	return v.Obj().(*value.Array), value.MakeNil()
}

func hofFn(name string, v value.Value) (*Function, value.Value) {
	fn, ok := asFunction(v)
	if !ok {
		return nil, value.MakeErr(fmt.Sprintf("%s expects a function, got %s", name, v.TypeName()), 1)
	}
	return fn, value.MakeNil()
}

// callCb calls an element callback, passing the index too if it declares two params.
func callCb(fn *Function, el value.Value, idx int) (value.Value, error) {
	switch len(fn.Params) {
	case 1:
		return callFunction(fn, []value.Value{el})
	case 2:
		return callFunction(fn, []value.Value{el, value.MakeInt(int64(idx))})
	default:
		return value.MakeNil(), fmt.Errorf("callback takes 1 or 2 parameters, got %d", len(fn.Params))
	}
}

func hofMap(arr *value.Array, fn *Function) (value.Value, error) {
	src := append([]value.Value(nil), arr.Elems...) // snapshot
	out := make([]value.Value, len(src))
	for i, el := range src {
		v, err := callCb(fn, el, i)
		if err != nil {
			return value.MakeNil(), err
		}
		if v.IsErr() {
			return v, nil // fail loud: first error becomes the result
		}
		out[i] = v
	}
	return value.MakeArray(out), nil
}

func hofFilter(arr *value.Array, fn *Function, keep bool) (value.Value, error) {
	src := append([]value.Value(nil), arr.Elems...)
	var out []value.Value
	for i, el := range src {
		v, err := callCb(fn, el, i)
		if err != nil {
			return value.MakeNil(), err
		}
		if v.IsErr() {
			return v, nil
		}
		if v.Truthy() == keep {
			out = append(out, el)
		}
	}
	return value.MakeArray(out), nil
}

func hofEach(arrVal value.Value, arr *value.Array, fn *Function) (value.Value, error) {
	src := append([]value.Value(nil), arr.Elems...)
	for i, el := range src {
		v, err := callCb(fn, el, i)
		if err != nil {
			return value.MakeNil(), err
		}
		if v.IsErr() {
			return v, nil
		}
	}
	return arrVal, nil // return the original array, for |> chaining
}

func hofFind(arr *value.Array, fn *Function) (value.Value, error) {
	src := append([]value.Value(nil), arr.Elems...)
	for i, el := range src {
		v, err := callCb(fn, el, i)
		if err != nil {
			return value.MakeNil(), err
		}
		if v.IsErr() {
			return v, nil
		}
		if v.Truthy() {
			return el, nil
		}
	}
	return value.MakeNil(), nil // miss -> undef, composes with //
}

// hofQuantify implements any (stopOnTrue) and all (stop on first falsy).
func hofQuantify(arr *value.Array, fn *Function, any bool) (value.Value, error) {
	src := append([]value.Value(nil), arr.Elems...)
	for i, el := range src {
		v, err := callCb(fn, el, i)
		if err != nil {
			return value.MakeNil(), err
		}
		if v.IsErr() {
			return v, nil
		}
		if any && v.Truthy() {
			return value.MakeBool(true), nil
		}
		if !any && !v.Truthy() {
			return value.MakeBool(false), nil
		}
	}
	return value.MakeBool(!any), nil // any over empty -> false; all over empty -> true
}

func hofCount(arr *value.Array, fn *Function) (value.Value, error) {
	src := append([]value.Value(nil), arr.Elems...)
	n := int64(0)
	for i, el := range src {
		v, err := callCb(fn, el, i)
		if err != nil {
			return value.MakeNil(), err
		}
		if v.IsErr() {
			return v, nil
		}
		if v.Truthy() {
			n++
		}
	}
	return value.MakeInt(n), nil
}

func hofReduce(arr *value.Array, init value.Value, fn *Function) (value.Value, error) {
	src := append([]value.Value(nil), arr.Elems...)
	acc := init
	for i, el := range src {
		var v value.Value
		var err error
		switch len(fn.Params) {
		case 2:
			v, err = callFunction(fn, []value.Value{acc, el})
		case 3:
			v, err = callFunction(fn, []value.Value{acc, el, value.MakeInt(int64(i))})
		default:
			return value.MakeNil(), fmt.Errorf("reduce callback takes 2 or 3 parameters, got %d", len(fn.Params))
		}
		if err != nil {
			return value.MakeNil(), err
		}
		if v.IsErr() {
			return v, nil
		}
		acc = v
	}
	return acc, nil
}

// hofFlatMap maps then flattens one level: an array result is spliced in, a
// scalar result is appended as-is.
func hofFlatMap(arr *value.Array, fn *Function) (value.Value, error) {
	src := append([]value.Value(nil), arr.Elems...)
	var out []value.Value
	for i, el := range src {
		v, err := callCb(fn, el, i)
		if err != nil {
			return value.MakeNil(), err
		}
		if v.IsErr() {
			return v, nil
		}
		if v.Tag() == value.Arr {
			out = append(out, v.Obj().(*value.Array).Elems...)
		} else {
			out = append(out, v)
		}
	}
	return value.MakeArray(out), nil
}

// hofArrayFn validates the common (array, fn) shape: wrong arity aborts (Go
// error); a non-array or non-function is a catchable Err returned in ev.
func hofArrayFn(name string, args []value.Value) (arr *value.Array, fn *Function, ev value.Value, abort error) {
	if len(args) != 2 {
		return nil, nil, value.MakeNil(), fmt.Errorf("%s expects 2 arguments (array, fn), got %d", name, len(args))
	}
	a, av := hofArray(name, args[0])
	if a == nil {
		return nil, nil, av, nil
	}
	f, fv := hofFn(name, args[1])
	if f == nil {
		return nil, nil, fv, nil
	}
	return a, f, value.MakeNil(), nil
}

// hofSort returns a NEW array sorted ascending (the input is never mutated). With
// one argument it uses natural ordering (numbers, strings); with a second it uses
// a comparator fn(a, b) returning negative / 0 / positive — e.g. |$a,$b| $a <=> $b.
// The sort is stable. A comparison that fails (mixed types, or a non-int
// comparator result) becomes a catchable Err; a comparator that aborts propagates.
func hofSort(name string, args []value.Value) (value.Value, error) {
	if len(args) < 1 || len(args) > 2 {
		return value.MakeNil(), fmt.Errorf("sort expects 1 or 2 arguments (array, cmp?), got %d", len(args))
	}
	arr, ev := hofArray(name, args[0])
	if arr == nil {
		return ev, nil
	}
	out := append([]value.Value(nil), arr.Elems...)
	var goErr error
	var failVal value.Value

	var less func(i, j int) bool
	if len(args) == 1 {
		less = func(i, j int) bool {
			if goErr != nil || failVal.IsErr() {
				return false
			}
			c, err := threeway(out[i], out[j])
			if err != nil {
				failVal = value.MakeErr(err.Error(), 1)
				return false
			}
			return c < 0
		}
	} else {
		fn, fv := hofFn(name, args[1])
		if fn == nil {
			return fv, nil
		}
		less = func(i, j int) bool {
			if goErr != nil || failVal.IsErr() {
				return false
			}
			v, err := callFunction(fn, []value.Value{out[i], out[j]})
			if err != nil {
				goErr = err
				return false
			}
			switch {
			case v.IsErr():
				failVal = v
			case v.Tag() != value.Int:
				failVal = value.MakeErr(fmt.Sprintf("sort comparator must return an int, got %s", v.TypeName()), 1)
			default:
				return v.AsInt() < 0
			}
			return false
		}
	}
	sort.SliceStable(out, less)
	if goErr != nil {
		return value.MakeNil(), goErr
	}
	if failVal.IsErr() {
		return failVal, nil
	}
	return value.MakeArray(out), nil
}

// hofSortBy returns a NEW array sorted ascending by keyFn(element), in natural key
// order. Keys are computed once per element (Schwartzian), so keyFn runs O(n) times.
func hofSortBy(name string, args []value.Value) (value.Value, error) {
	arr, fn, ev, abort := hofArrayFn(name, args)
	if abort != nil {
		return value.MakeNil(), abort
	}
	if ev.IsErr() {
		return ev, nil
	}
	type keyed struct{ key, elem value.Value }
	pairs := make([]keyed, len(arr.Elems))
	for i, el := range arr.Elems {
		k, err := callCb(fn, el, i)
		if err != nil {
			return value.MakeNil(), err
		}
		if k.IsErr() {
			return k, nil
		}
		pairs[i] = keyed{key: k, elem: el}
	}
	var failVal value.Value
	sort.SliceStable(pairs, func(a, b int) bool {
		if failVal.IsErr() {
			return false
		}
		c, err := threeway(pairs[a].key, pairs[b].key)
		if err != nil {
			failVal = value.MakeErr(err.Error(), 1)
			return false
		}
		return c < 0
	})
	if failVal.IsErr() {
		return failVal, nil
	}
	out := make([]value.Value, len(pairs))
	for i, p := range pairs {
		out[i] = p.elem
	}
	return value.MakeArray(out), nil
}

// hofMinMaxBy returns the element with the smallest (min_by) or largest (max_by)
// keyFn(element). An empty array yields undef, so it composes with //. Ties keep
// the first such element.
func hofMinMaxBy(name string, args []value.Value) (value.Value, error) {
	arr, fn, ev, abort := hofArrayFn(name, args)
	if abort != nil {
		return value.MakeNil(), abort
	}
	if ev.IsErr() {
		return ev, nil
	}
	if len(arr.Elems) == 0 {
		return value.MakeNil(), nil
	}
	wantMax := name == "max_by"
	var best, bestKey value.Value
	for i, el := range arr.Elems {
		k, err := callCb(fn, el, i)
		if err != nil {
			return value.MakeNil(), err
		}
		if k.IsErr() {
			return k, nil
		}
		if i == 0 {
			best, bestKey = el, k
			continue
		}
		c, err := threeway(k, bestKey)
		if err != nil {
			return value.MakeErr(err.Error(), 1), nil
		}
		if (wantMax && c > 0) || (!wantMax && c < 0) {
			best, bestKey = el, k
		}
	}
	return best, nil
}

// --- non-callback array builtins ---

func builtinTake(args []value.Value) (value.Value, error) {
	elems, n, errv, abort := sliceArgs("take", args)
	if abort != nil {
		return value.MakeNil(), abort
	}
	if errv.IsErr() {
		return errv, nil
	}
	if n < 0 {
		n = 0
	}
	if n > int64(len(elems)) {
		n = int64(len(elems))
	}
	return value.MakeArray(append([]value.Value(nil), elems[:n]...)), nil
}

func builtinDrop(args []value.Value) (value.Value, error) {
	elems, n, errv, abort := sliceArgs("drop", args)
	if abort != nil {
		return value.MakeNil(), abort
	}
	if errv.IsErr() {
		return errv, nil
	}
	if n < 0 {
		n = 0
	}
	if n > int64(len(elems)) {
		n = int64(len(elems))
	}
	return value.MakeArray(append([]value.Value(nil), elems[n:]...)), nil
}

// sliceArgs validates (array, n) for take/drop. abort is a Go error for wrong
// arity; errv is a catchable Err for a wrong-typed argument.
func sliceArgs(name string, args []value.Value) (elems []value.Value, n int64, errv value.Value, abort error) {
	if len(args) != 2 {
		return nil, 0, value.MakeNil(), fmt.Errorf("%s expects 2 arguments (array, n), got %d", name, len(args))
	}
	if args[0].Tag() != value.Arr {
		return nil, 0, value.MakeErr(fmt.Sprintf("%s expects an array, got %s", name, args[0].TypeName()), 1), nil
	}
	if args[1].Tag() != value.Int {
		return nil, 0, value.MakeErr(fmt.Sprintf("%s count must be an int, got %s", name, args[1].TypeName()), 1), nil
	}
	return args[0].Obj().(*value.Array).Elems, args[1].AsInt(), value.MakeNil(), nil
}

// builtinUniq returns the distinct elements (by structural equality), in order.
func builtinUniq(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("uniq expects 1 argument (array), got %d", len(args))
	}
	if args[0].Tag() != value.Arr {
		return value.MakeErr(fmt.Sprintf("uniq expects an array, got %s", args[0].TypeName()), 1), nil
	}
	var out []value.Value
	for _, e := range args[0].Obj().(*value.Array).Elems {
		dup := false
		for _, k := range out {
			if value.Equal(e, k) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, e)
		}
	}
	return value.MakeArray(out), nil
}

// hofPmap is the parallel map: it applies fn to each element across a bounded
// worker pool (runtime.NumCPU), returning a new array in INPUT order. It is
// race-free because each element is deep-copied for its worker (copy-on-send),
// each worker writes only its own result index (no shared slice write), and the
// callback runs over frozen top-level constants + its own per-call scope. Like
// map, it is fail-loud: the first Err a callback produces becomes the result and
// stops further work.
func hofPmap(arr *value.Array, fn *Function) (value.Value, error) {
	src := append([]value.Value(nil), arr.Elems...)
	n := len(src)
	out := make([]value.Value, n)
	if n == 0 {
		return value.MakeArray(out), nil
	}
	workers := runtime.NumCPU()
	if workers > n {
		workers = n
	}
	jobs := make(chan int, n)
	for i := 0; i < n; i++ {
		jobs <- i
	}
	close(jobs)

	var (
		mu          sync.Mutex
		firstErr    error       // a control-flow signal (exit/die) or runtime abort from a worker
		firstErrVal value.Value // the first Err-VALUE result
		haveErrVal  bool
		cancelled   atomic.Bool
		wg          sync.WaitGroup
	)
	// A Go error (exit/die, or a runtime abort) must never be downgraded to a value,
	// so it is recorded separately from an Err-value result and WINS regardless of
	// which worker failed first — otherwise a racing Err value could swallow an
	// exit. Either kind cancels the remaining work (fail-loud); a sibling's
	// cancellation may pre-empt a not-yet-run callback, which is inherent to
	// parallel fail-loud execution.
	recordGoErr := func(e error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = e
		}
		mu.Unlock()
		cancelled.Store(true)
	}
	recordErrVal := func(v value.Value) {
		mu.Lock()
		if !haveErrVal {
			firstErrVal, haveErrVal = v, true
		}
		mu.Unlock()
		cancelled.Store(true)
	}
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := range jobs {
				if cancelled.Load() {
					continue // a failure was recorded: drain the rest without running callbacks
				}
				v, err := applyPmap(fn, src[i], i)
				if err != nil {
					recordGoErr(err)
					continue
				}
				if v.IsErr() {
					recordErrVal(v)
					continue
				}
				out[i] = v // distinct index per worker — no shared write
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return value.MakeNil(), firstErr // exit/die/abort unwinds; never masked by an Err value
	}
	if haveErrVal {
		return firstErrVal, nil
	}
	return value.MakeArray(out), nil
}

// applyPmap runs one pmap callback over a deep-copied (isolated) element,
// converting a panic into a catchable Err value so a worker goroutine can never
// crash the interpreter.
func applyPmap(fn *Function, elem value.Value, idx int) (v value.Value, err error) {
	defer func() {
		if r := recover(); r != nil {
			v = value.MakeErr(fmt.Sprintf("pmap callback panicked: %v", r), 1)
			err = nil
		}
	}()
	el := value.DeepCopyValue(elem, map[value.Obj]value.Obj{})
	return callCb(fn, el, idx)
}
