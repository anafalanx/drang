package eval

import (
	"fmt"

	"github.com/anafalanx/drang/internal/value"
)

// Errors are first-class values; these builtins inspect one. They complete the
// error model's read side: alongside ? (propagate) and // (recover), a script can
// now branch on a command's specific exit code or parse its message — e.g.
//
//	$r := capture("grep", $pat, $f)
//	if is_err($r) {
//	  if err_code($r) == 1 { say("no match") } else { say("grep:", err_msg($r)) }
//	}

// builtinIsErr reports whether x is an error value.
func builtinIsErr(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("is_err expects 1 argument, got %d", len(args))
	}
	return value.MakeBool(args[0].IsErr()), nil
}

// builtinErrCode returns an error value's code; for a non-error it returns 0, so
// err_code(run(cmd)) reads as "the exit code, 0 on success".
func builtinErrCode(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("err_code expects 1 argument, got %d", len(args))
	}
	if args[0].IsErr() {
		return value.MakeInt(args[0].ErrCode()), nil
	}
	return value.MakeInt(0), nil
}

// builtinErrMsg returns an error value's message; for a non-error it returns "".
func builtinErrMsg(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("err_msg expects 1 argument, got %d", len(args))
	}
	if args[0].IsErr() {
		return value.MakeStr(args[0].ErrMsg()), nil
	}
	return value.MakeStr(""), nil
}
