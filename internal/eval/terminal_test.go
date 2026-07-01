package eval

import (
	"os"
	"path/filepath"
	"testing"
)

// IsTerminal must report false for a regular file, a nil handle, and a plain pipe — none is a
// console or an MSYS/Cygwin pty. The true case needs a real console, which the test harness
// lacks, so it is exercised only in that it does not misfire on non-terminals here.
func TestIsTerminalNonTTY(t *testing.T) {
	if IsTerminal(nil) {
		t.Error("nil should not be a terminal")
	}

	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fh, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	if IsTerminal(fh) {
		t.Error("a regular file should not be a terminal")
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if IsTerminal(r) {
		t.Error("an anonymous pipe should not be a terminal")
	}
}
