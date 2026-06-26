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
	prev := callBuiltin(t, "gc", str("relaxed"))
	if prev.Tag() != value.Int {
		t.Fatalf("gc(\"relaxed\") should return the previous percent (int), got %s", prev.TypeName())
	}
	if got := debug.SetGCPercent(400); got != 400 {
		t.Errorf("gc(\"relaxed\") should have set GOGC to 400, but it was %d", got)
	}

	// The int form returns the previous percent too.
	if v := callBuiltin(t, "gc", value.MakeInt(250)); v.Tag() != value.Int || v.AsInt() != 400 {
		t.Errorf("gc(250) should return the previous percent 400, got %v", v.Display())
	}

	// "off" disables collection.
	callBuiltin(t, "gc", str("off"))
	if got := debug.SetGCPercent(100); got != -1 {
		t.Errorf("gc(\"off\") should have disabled GC (-1), but it was %d", got)
	}

	// An unknown mode word is a catchable Err, not an abort.
	if e := callBuiltin(t, "gc", str("bogus")); !e.IsErr() {
		t.Error("gc with an unknown mode should be a catchable Err value")
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
