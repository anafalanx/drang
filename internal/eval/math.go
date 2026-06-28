package eval

import (
	"fmt"
	"math"

	"github.com/anafalanx/drang/internal/value"
)

// Minimal numeric helpers — daily-driver math (byte sizes, counters, report math,
// scaling, percentages), deliberately NOT a math/trig kitchen sink (no sin/cos, no
// bignum). abs/sum/min/max preserve int vs float; floor/ceil/round and truncating-
// integer div return an int; sqrt/log return a float; pow returns an int when both
// operands are ints and the exponent is non-negative, else a float. Following the
// builtin convention: wrong arity aborts; a bad operand (non-number, sqrt of a negative,
// log of a non-positive, divide-by-zero, out-of-range/overflow) is a catchable Err.

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
			if addOverflows(s, n.AsInt()) {
				return value.MakeErr("sum: integer overflow", 1), nil
			}
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
		// float64(MaxInt64) rounds up to 2^63 (overflows), but float64(MinInt64) is
		// exactly -2^63 and in range — so the low bound is strict.
		if math.IsNaN(f) || math.IsInf(f, 0) || f >= math.MaxInt64 || f < math.MinInt64 {
			return value.MakeErr(fmt.Sprintf("%s: %s is out of int range", name, a.Display()), 1), nil
		}
		return value.MakeInt(int64(f)), nil
	default:
		return value.MakeErr(fmt.Sprintf("%s: expected a number, got %s", name, a.TypeName()), 1), nil
	}
}

// builtinSqrt is the square root, always a float. A negative operand is a catchable Err
// (rather than a silent NaN).
func builtinSqrt(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("sqrt expects 1 argument, got %d", len(args))
	}
	a := args[0]
	if !a.IsNumber() {
		return value.MakeErr("sqrt: expected a number, got "+a.TypeName(), 1), nil
	}
	if a.Num() < 0 {
		return value.MakeErr("sqrt: of a negative number", 1), nil
	}
	return value.MakeFloat(math.Sqrt(a.Num())), nil
}

// builtinPow raises base to exp. Both int with a non-negative exp gives an exact int
// (overflow is a catchable Err); otherwise a float.
func builtinPow(args []value.Value) (value.Value, error) {
	if len(args) != 2 {
		return value.MakeNil(), fmt.Errorf("pow expects 2 arguments, got %d", len(args))
	}
	base, exp := args[0], args[1]
	if !base.IsNumber() || !exp.IsNumber() {
		return value.MakeErr("pow: expected numbers", 1), nil
	}
	if base.Tag() == value.Int && exp.Tag() == value.Int && exp.AsInt() >= 0 {
		n, ok := intPow(base.AsInt(), exp.AsInt())
		if !ok {
			return value.MakeErr("pow: integer overflow", 1), nil
		}
		return value.MakeInt(n), nil
	}
	return value.MakeFloat(math.Pow(base.Num(), exp.Num())), nil
}

// intPow computes base**exp for exp >= 0 by squaring, with overflow detection.
func intPow(base, exp int64) (int64, bool) {
	result := int64(1)
	for exp > 0 {
		if exp&1 == 1 {
			if mulOverflows(result, base) {
				return 0, false
			}
			result *= base
		}
		exp >>= 1
		if exp > 0 {
			if mulOverflows(base, base) {
				return 0, false
			}
			base *= base
		}
	}
	return result, true
}

// builtinLog is the natural logarithm, or log base b with a second argument. The operand
// must be positive (and the base positive and != 1); otherwise a catchable Err. Float.
func builtinLog(args []value.Value) (value.Value, error) {
	if len(args) != 1 && len(args) != 2 {
		return value.MakeNil(), fmt.Errorf("log expects 1 or 2 arguments, got %d", len(args))
	}
	x := args[0]
	if !x.IsNumber() {
		return value.MakeErr("log: expected a number, got "+x.TypeName(), 1), nil
	}
	if x.Num() <= 0 {
		return value.MakeErr("log: of a non-positive number", 1), nil
	}
	if len(args) == 1 {
		return value.MakeFloat(math.Log(x.Num())), nil
	}
	b := args[1]
	if !b.IsNumber() {
		return value.MakeErr("log: base must be a number, got "+b.TypeName(), 1), nil
	}
	if b.Num() <= 0 || b.Num() == 1 {
		return value.MakeErr("log: base must be positive and not 1", 1), nil
	}
	return value.MakeFloat(math.Log(x.Num()) / math.Log(b.Num())), nil
}

// builtinDiv is truncating integer division (toward zero, matching the % remainder so
// div(a,b)*b + a%b == a): div(17, 5) == 3, div(-17, 5) == -3. Division by zero is a
// catchable Err. Two ints divide exactly; a float operand truncates the quotient.
func builtinDiv(args []value.Value) (value.Value, error) {
	if len(args) != 2 {
		return value.MakeNil(), fmt.Errorf("div expects 2 arguments, got %d", len(args))
	}
	a, b := args[0], args[1]
	if !a.IsNumber() || !b.IsNumber() {
		return value.MakeErr("div: expected numbers", 1), nil
	}
	if a.Tag() == value.Int && b.Tag() == value.Int {
		if b.AsInt() == 0 {
			return value.MakeErr("div: division by zero", 1), nil
		}
		if a.AsInt() == math.MinInt64 && b.AsInt() == -1 {
			return value.MakeErr("div: overflow", 1), nil
		}
		return value.MakeInt(a.AsInt() / b.AsInt()), nil
	}
	if b.Num() == 0 {
		return value.MakeErr("div: division by zero", 1), nil
	}
	q := math.Trunc(a.Num() / b.Num())
	if math.IsNaN(q) || math.IsInf(q, 0) || q >= math.MaxInt64 || q < math.MinInt64 {
		return value.MakeErr("div: result is out of int range", 1), nil
	}
	return value.MakeInt(int64(q)), nil
}
