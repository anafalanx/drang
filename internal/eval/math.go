package eval

import (
	"fmt"
	"math"

	"github.com/anafalanx/drang/internal/value"
)

// Minimal numeric helpers — daily-driver math (byte sizes, counters, report math),
// deliberately NOT a math/trig kitchen sink. abs/sum/min/max preserve int vs
// float; floor/ceil/round return an int (the useful form for counts/indices),
// erroring on NaN/Inf or values outside int64 range. Following the builtin
// convention: wrong arity aborts; a non-number operand is a catchable Err.

// builtinAbs is the numeric absolute value (the path-absolutize builtin is now
// abspath). Preserves int/float.
func builtinAbs(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("abs expects 1 argument, got %d", len(args))
	}
	switch a := args[0]; a.Tag() {
	case value.Int:
		n := a.AsInt()
		if n == math.MinInt64 {
			return value.MakeErr("abs: overflow (abs of the int64 minimum)", 1), nil
		}
		if n < 0 {
			n = -n
		}
		return value.MakeInt(n), nil
	case value.Float:
		return value.MakeFloat(math.Abs(a.AsFloat())), nil
	default:
		return value.MakeErr("abs: expected a number, got "+a.TypeName(), 1), nil
	}
}

// numericOperands gathers operands for sum/min/max from either a single array
// argument or a variadic list of scalars. A non-number operand is a catchable Err.
func numericOperands(name string, args []value.Value) (nums []value.Value, errVal value.Value, isErr bool) {
	items := args
	if len(args) == 1 && args[0].Tag() == value.Arr {
		items = args[0].Obj().(*value.Array).Elems
	}
	for _, it := range items {
		if !it.IsNumber() {
			return nil, value.MakeErr(fmt.Sprintf("%s: expected numbers, got %s", name, it.TypeName()), 1), true
		}
	}
	return items, value.MakeNil(), false
}

// builtinSum adds numbers (an array or variadic scalars); empty sum is 0. The
// result is an int when every operand is an int, else a float.
func builtinSum(args []value.Value) (value.Value, error) {
	nums, errv, isErr := numericOperands("sum", args)
	if isErr {
		return errv, nil
	}
	allInt := true
	for _, n := range nums {
		if n.Tag() == value.Float {
			allInt = false
			break
		}
	}
	if allInt {
		var s int64
		for _, n := range nums {
			s += n.AsInt()
		}
		return value.MakeInt(s), nil
	}
	var s float64
	for _, n := range nums {
		s += n.Num()
	}
	return value.MakeFloat(s), nil
}

func builtinMin(args []value.Value) (value.Value, error) { return minMax("min", args, true) }
func builtinMax(args []value.Value) (value.Value, error) { return minMax("max", args, false) }

// minMax returns the smallest/largest operand, preserving its original int/float
// value. No operands is a catchable Err.
func minMax(name string, args []value.Value, wantMin bool) (value.Value, error) {
	nums, errv, isErr := numericOperands(name, args)
	if isErr {
		return errv, nil
	}
	if len(nums) == 0 {
		return value.MakeErr(name+": no values", 1), nil
	}
	best := nums[0]
	for _, n := range nums[1:] {
		if (wantMin && n.Num() < best.Num()) || (!wantMin && n.Num() > best.Num()) {
			best = n
		}
	}
	return best, nil
}

func builtinFloor(args []value.Value) (value.Value, error) {
	return roundish("floor", args, math.Floor)
}
func builtinCeil(args []value.Value) (value.Value, error) { return roundish("ceil", args, math.Ceil) }
func builtinRound(args []value.Value) (value.Value, error) {
	return roundish("round", args, math.Round)
}

// roundish applies a float->float rounding fn and returns an int. An int passes
// through unchanged; NaN/Inf or out-of-int64-range yields a catchable Err.
func roundish(name string, args []value.Value, fn func(float64) float64) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("%s expects 1 argument, got %d", name, len(args))
	}
	switch a := args[0]; a.Tag() {
	case value.Int:
		return a, nil
	case value.Float:
		f := fn(a.AsFloat())
		if math.IsNaN(f) || math.IsInf(f, 0) || f >= math.MaxInt64 || f <= math.MinInt64 {
			return value.MakeErr(fmt.Sprintf("%s: %s is out of int range", name, a.Display()), 1), nil
		}
		return value.MakeInt(int64(f)), nil
	default:
		return value.MakeErr(fmt.Sprintf("%s: expected a number, got %s", name, a.TypeName()), 1), nil
	}
}
