package eval

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/anafalanx/drang/internal/value"
)

// builtinCwd returns the current working directory (native path). A task runner
// invoked through the zmal `z` front door runs from the project root, so cwd() is
// how a ported script discovers the root (the .toolchain, project files).
func builtinCwd(args []value.Value) (value.Value, error) {
	if len(args) != 0 {
		return value.MakeNil(), fmt.Errorf("cwd expects no arguments, got %d", len(args))
	}
	dir, err := os.Getwd()
	if err != nil {
		return value.MakeErr(fmt.Sprintf("cwd: %v", err), 1), nil
	}
	return value.MakeStr(dir), nil
}

// builtinEnv reads a process environment variable, returning its value, or the
// optional default (nil if omitted) when unset. Unlike the $ENV map — whose keys
// are the exact-case names from os.Environ — env() uses os.LookupEnv, which is
// CASE-INSENSITIVE on Windows, so env("PATH") works whether Windows exposes it as
// PATH or Path.
func builtinEnv(args []value.Value) (value.Value, error) {
	if len(args) < 1 || len(args) > 2 {
		return value.MakeNil(), fmt.Errorf("env expects 1 or 2 arguments (name, default?), got %d", len(args))
	}
	if args[0].Tag() != value.Str {
		return value.MakeNil(), fmt.Errorf("env expects a string name, got %s", args[0].TypeName())
	}
	if v, ok := os.LookupEnv(args[0].AsStr()); ok {
		return value.MakeStr(v), nil
	}
	if len(args) == 2 {
		return args[1], nil
	}
	return value.MakeNil(), nil
}

// gcPresets map friendly mode words to a GC target percent (Go's GOGC knob):
// lower collects more often (less peak RAM, more CPU), higher collects less often
// (more RAM, faster), and "off" disables collection entirely — ideal for a
// short-lived task run, where the process exits and the OS reclaims everything.
var gcPresets = map[string]int{
	"off":     -1,
	"lean":    20,
	"normal":  100,
	"relaxed": 400,
}

// builtinSysGC tunes the garbage collector and returns the PREVIOUS target percent,
// so a heavy phase can relax GC and then restore it:
//
//	$old := sys_gc("relaxed"); ...heavy work...; sys_gc($old)
//
//	sys_gc("off" | "lean" | "normal" | "relaxed")  — friendly presets
//	sys_gc(n)                                       — set the GOGC percent directly
//	                                              (advanced; a negative n disables GC)
func builtinSysGC(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("sys_gc expects 1 argument (a mode word or an int), got %d", len(args))
	}
	var pct int
	switch a := args[0]; a.Tag() {
	case value.Int:
		pct = int(a.AsInt())
	case value.Str:
		p, ok := gcPresets[a.AsStr()]
		if !ok {
			return value.MakeErr(fmt.Sprintf("sys_gc: unknown mode %q (use off/lean/normal/relaxed or an int)", a.AsStr()), 1), nil
		}
		pct = p
	default:
		return value.MakeErr(fmt.Sprintf("sys_gc expects a mode word or an int, got %s", a.TypeName()), 1), nil
	}
	return value.MakeInt(int64(debug.SetGCPercent(pct))), nil
}
