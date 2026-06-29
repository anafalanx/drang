package eval

import (
	"runtime/debug"
	"testing"

	"github.com/anafalanx/drang/internal/value"
)

func TestGCKnob(t *testing.T) {
	orig := debug.SetGCPercent(100) // capture and restore so we don't disturb other tests
	debug.SetGCPercent(orig)
	defer debug.SetGCPercent(orig)

	// A preset word returns the previous percent (an int) and applies the setting.
	prev := callBuiltin(t, "sys_gc", str("relaxed"))
	if prev.Tag() != value.Int {
		t.Fatalf("sys_gc(\"relaxed\") should return the previous percent (int), got %s", prev.TypeName())
	}
	if got := debug.SetGCPercent(400); got != 400 {
		t.Errorf("sys_gc(\"relaxed\") should have set GOGC to 400, but it was %d", got)
	}

	// The int form returns the previous percent too.
	if v := callBuiltin(t, "sys_gc", value.MakeInt(250)); v.Tag() != value.Int || v.AsInt() != 400 {
		t.Errorf("sys_gc(250) should return the previous percent 400, got %v", v.Display())
	}

	// "off" disables collection.
	callBuiltin(t, "sys_gc", str("off"))
	if got := debug.SetGCPercent(100); got != -1 {
		t.Errorf("sys_gc(\"off\") should have disabled GC (-1), but it was %d", got)
	}

	// An unknown mode word is a catchable Err, not an abort.
	if e := callBuiltin(t, "sys_gc", str("bogus")); !e.IsErr() {
		t.Error("sys_gc with an unknown mode should be a catchable Err value")
	}
}

func TestCwd(t *testing.T) {
	v := callBuiltin(t, "cwd")
	if v.Tag() != value.Str || v.AsStr() == "" {
		t.Errorf("cwd should return a non-empty string path, got %v", v.Display())
	}
}

func TestStartDetached(t *testing.T) {
	// A command that cannot be started is a catchable Err (no abort, no hang).
	if e := callBuiltin(t, "start", str("definitely-not-a-real-command-xyz123")); !e.IsErr() {
		t.Errorf("start of a bogus command should be a catchable Err, got %v", e.Display())
	}
	// A real command launches detached and returns a process handle.
	p := callBuiltin(t, "start", str("cmd"), str("/c"), str("exit"))
	if p.Tag() != value.Proc {
		t.Fatalf("start should return a process handle, got %v", p.Display())
	}
	if pid := callBuiltin(t, "pid", p); pid.Tag() != value.Int || pid.AsInt() <= 0 {
		t.Errorf("pid should be a positive int, got %v", pid.Display())
	}
	// await yields the exit status (true here: `cmd /c exit` exits 0).
	if st := callBuiltin(t, "await", p); !st.Truthy() {
		t.Errorf("await of a clean exit should be truthy, got %v", st.Display())
	}
}

// exe() and is_terminal() unblock porting the zmal `z` launcher (find-own-location and
// TTY detection).

func TestExe(t *testing.T) {
	v := callBuiltin(t, "exe")
	if v.Tag() != value.Str || v.AsStr() == "" {
		t.Errorf("exe() should return a non-empty path string, got %v", v.Display())
	}
}

func TestIsTerminal(t *testing.T) {
	// Under the test harness stdio is piped, so the values are clean bools; the contract is
	// a bool for a valid stream and a catchable Err otherwise.
	for _, s := range []string{"stdin", "stdout", "stderr"} {
		if v := callBuiltin(t, "is_terminal", str(s)); v.Tag() != value.Bool {
			t.Errorf("is_terminal(%q) should return a bool, got %v", s, v.Display())
		}
	}
	if v := callBuiltin(t, "is_terminal"); v.Tag() != value.Bool {
		t.Errorf("is_terminal() should default to stdin and return a bool, got %v", v.Display())
	}
	if e := callBuiltin(t, "is_terminal", str("bogus")); !e.IsErr() {
		t.Error("is_terminal with an unknown stream should be a catchable Err")
	}
	if e := callBuiltin(t, "is_terminal", value.MakeInt(1)); !e.IsErr() {
		t.Error("is_terminal with a non-string should be a catchable Err")
	}
}
