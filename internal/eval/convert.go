package eval

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/anafalanx/drang/internal/value"
)

// Scalar conversions. str/bool/type never fail — every value has a display, a
// truthiness, and a type name; float fails (a catchable Err) only on an unparseable
// string or a non-number. int() lives in eval.go beside the original conversions.
// Following the builtin convention: wrong arity aborts, bad values are catchable.

// builtinStr renders any value as its display string (the say form): numbers, bools,
// nil, collections, and errors all become text.
func builtinStr(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("str expects 1 argument, got %d", len(args))
	}
	return value.MakeStr(args[0].Display()), nil
}

// builtinFloat converts to a float: an int widens, a float passes through, a string is
// parsed (surrounding whitespace tolerated). Anything else is a catchable Err.
func builtinFloat(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("float expects 1 argument, got %d", len(args))
	}
	switch a := args[0]; a.Tag() {
	case value.Float:
		return a, nil
	case value.Int:
		return value.MakeFloat(float64(a.AsInt())), nil
	case value.Str:
		f, err := strconv.ParseFloat(strings.TrimSpace(a.AsStr()), 64)
		if err != nil {
			return value.MakeErr(fmt.Sprintf("cannot parse %q as float", a.AsStr()), 1), nil
		}
		return value.MakeFloat(f), nil
	default:
		return value.MakeErr(fmt.Sprintf("cannot convert %s to float", a.TypeName()), 1), nil
	}
}

// builtinBool coerces by truthiness (nil, false, 0, 0.0, "" and empty containers are
// false; everything else is true).
func builtinBool(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("bool expects 1 argument, got %d", len(args))
	}
	return value.MakeBool(args[0].Truthy()), nil
}

// builtinType returns the value's type name: "int", "float", "string", "bool", "nil",
// "array", "map", "range", "error", "function", etc.
func builtinType(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("type expects 1 argument, got %d", len(args))
	}
	return value.MakeStr(args[0].TypeName()), nil
}
