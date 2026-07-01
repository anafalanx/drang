package eval

import "testing"

// Env-var names are matched case-insensitively (Windows semantics): a differently-cased overlay
// key replaces the inherited var in place rather than adding a duplicate.
func TestEnvKeyEqual(t *testing.T) {
	if !envKeyEqual("PATH", "path") {
		t.Error("PATH/path must match case-insensitively")
	}
	if !envKeyEqual("Path", "PATH") {
		t.Error("Path/PATH must match case-insensitively")
	}
	if envKeyEqual("PATH", "PATHEXT") {
		t.Error("distinct names must not match")
	}
}

func TestSetEnvFoldReplacesInPlace(t *testing.T) {
	got := setEnvFold([]string{`PATH=C:\bin`}, "Path", `C:\custom`)
	if len(got) != 1 || got[0] != `Path=C:\custom` {
		t.Errorf("a differently-cased overlay should replace in place, got %v", got)
	}
}
