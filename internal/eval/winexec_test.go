package eval

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestJobCmdCapture(t *testing.T) {
	exe, err := resolveExe("cmd", execOpts{})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c := &jobCmd{exe: exe, argv: []string{"cmd", "/c", "echo", "hello"}, stdout: &out}
	if err := c.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	code, timedOut, werr := c.wait()
	if werr != nil || timedOut || code != 0 {
		t.Fatalf("wait = (%d, %v, %v)", code, timedOut, werr)
	}
	if strings.TrimSpace(out.String()) != "hello" {
		t.Errorf("stdout = %q, want hello", out.String())
	}
}

func TestJobCmdExitCode(t *testing.T) {
	exe, _ := resolveExe("cmd", execOpts{})
	c := &jobCmd{exe: exe, argv: []string{"cmd", "/c", "exit", "3"}}
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := c.wait(); code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
}

func TestJobCmdStdin(t *testing.T) {
	exe, _ := resolveExe("cmd", execOpts{})
	var out bytes.Buffer
	c := &jobCmd{exe: exe, argv: []string{"cmd", "/c", "sort"}, stdin: strings.NewReader("b\na\n"), stdout: &out}
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	c.wait()
	got := strings.ReplaceAll(strings.TrimSpace(out.String()), "\r\n", "\n")
	if got != "a\nb" {
		t.Errorf("sorted stdin = %q, want a\\nb", got)
	}
}

func TestJobCmdTimeout(t *testing.T) {
	exe, _ := resolveExe("cmd", execOpts{})
	c := &jobCmd{exe: exe, argv: []string{"cmd", "/c", "ping", "-n", "20", "127.0.0.1"}, timeout: 300 * time.Millisecond}
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	code, timedOut, _ := c.wait()
	if !timedOut {
		t.Errorf("expected timeout; code=%d elapsed=%v", code, time.Since(start))
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("timeout took too long: %v (tree-kill not effective?)", time.Since(start))
	}
}
