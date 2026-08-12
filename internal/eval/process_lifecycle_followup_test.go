package eval

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anafalanx/drang/internal/value"
	"golang.org/x/sys/windows"
)

const inheritedPipeHelperEnv = "DRANG_INHERITED_PIPE_HELPER"

// inheritedPipeHelper re-execs this test binary as a short-lived root which launches a persistent
// descendant inheriting stdout/stderr. It is the exact pipe-lifetime shape that used to strand
// synchronous copiers and started-process drainers after the root had already exited.
func inheritedPipeHelper() {
	switch os.Getenv(inheritedPipeHelperEnv) {
	case "root":
		if len(os.Args) != 2 {
			os.Exit(2)
		}
		cmd := exec.Command(os.Args[0])
		cmd.Env = replaceEnv(os.Environ(), inheritedPipeHelperEnv, "descendant")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			os.Exit(3)
		}
		if err := os.WriteFile(os.Args[1], []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
			_ = cmd.Process.Kill()
			os.Exit(4)
		}
		_, _ = fmt.Fprintln(os.Stdout, "root-output")
	case "descendant":
		time.Sleep(30 * time.Second)
	default:
		os.Exit(5)
	}
}

func pipeHelperOpts(supervise bool) value.Value {
	opts := value.MakeMap()
	om := opts.Obj().(*value.OrderedMap)
	om.Set(value.MakeStr("stdout_pipe"), value.MakeBool(true))
	if supervise {
		om.Set(value.MakeStr("supervise"), value.MakeBool(true))
	}
	return opts
}

func waitHelperPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, err := os.ReadFile(path)
		if err == nil {
			pid, perr := strconv.Atoi(strings.TrimSpace(string(b)))
			if perr == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper did not publish descendant pid at %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func openHelperProcess(pid int) (windows.Handle, error) {
	return windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
}

func helperProcessAlive(pid int) bool {
	h, err := openHelperProcess(pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	state, err := windows.WaitForSingleObject(h, 0)
	return err == nil && state == uint32(windows.WAIT_TIMEOUT)
}

func terminateHelperProcess(pid int) {
	h, err := openHelperProcess(pid)
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	_ = windows.TerminateProcess(h, 1)
	_, _ = windows.WaitForSingleObject(h, 5_000)
}

func waitHelperGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for helperProcessAlive(pid) {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
	return true
}

func collectProcStdout(t *testing.T, p *Proc) string {
	t.Helper()
	var b strings.Builder
	for {
		chunk, eof, err := p.readStdout()
		if err != nil {
			t.Fatalf("read stdout: %v", err)
		}
		b.WriteString(chunk)
		if eof {
			return b.String()
		}
	}
}

func TestSynchronousWaitEndsInheritedPipeDescendant(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pidFile := t.TempDir() + `\descendant.pid`
	t.Setenv(inheritedPipeHelperEnv, "root")
	var out bytes.Buffer
	c, err := newJobCmd([]string{exe, pidFile}, execOpts{}, nil, &out, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	type waitResult struct {
		code int
		err  error
	}
	done := make(chan waitResult, 1)
	go func() {
		code, _, err := c.wait()
		done <- waitResult{code: code, err: err}
	}()
	select {
	case got := <-done:
		if got.err != nil || got.code != 0 {
			t.Fatalf("wait = (%d, %v)", got.code, got.err)
		}
	case <-time.After(8 * time.Second):
		c.killTree()
		<-done
		t.Fatal("synchronous wait hung on a descendant holding inherited stdout")
	}
	if !strings.Contains(out.String(), "root-output") {
		t.Fatalf("lost root output: %q", out.String())
	}
	pid := waitHelperPID(t, pidFile)
	if !waitHelperGone(pid, 2*time.Second) {
		terminateHelperProcess(pid)
		t.Fatal("synchronous job left its inherited-stdio descendant alive")
	}
}

func TestStartedUnsupervisedDescendantGetsBoundedPipeDrain(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pidFile := t.TempDir() + `\descendant.pid`
	t.Setenv(inheritedPipeHelperEnv, "root")
	oldGrace := startedPipeDrainGrace
	startedPipeDrainGrace = 75 * time.Millisecond
	defer func() { startedPipeDrainGrace = oldGrace }()

	v, err := builtinStart([]value.Value{value.MakeStr(exe), value.MakeStr(pidFile), pipeHelperOpts(false)})
	if err != nil || v.IsErr() {
		t.Fatalf("start = %s, %v", v.Display(), err)
	}
	p := v.Obj().(*Proc)
	select {
	case <-p.done:
	case <-time.After(8 * time.Second):
		p.terminate()
		t.Fatal("await/status stayed blocked on an unsupervised descendant's inherited stdout")
	}
	pid := waitHelperPID(t, pidFile)
	defer terminateHelperProcess(pid)
	if !helperProcessAlive(pid) {
		t.Fatal("bounded pipe drain killed an unsupervised descendant")
	}
	if got := collectProcStdout(t, p); !strings.Contains(got, "root-output") {
		t.Fatalf("bounded drain lost ordinary root output: %q", got)
	}
	if p.res.IsErr() {
		t.Fatalf("root success became %s", p.res.Display())
	}
}

func TestStartedSupervisedDescendantDiesAndDrains(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pidFile := t.TempDir() + `\descendant.pid`
	t.Setenv(inheritedPipeHelperEnv, "root")
	oldGrace := startedPipeDrainGrace
	startedPipeDrainGrace = 2 * time.Second
	defer func() { startedPipeDrainGrace = oldGrace }()

	v, err := builtinStart([]value.Value{value.MakeStr(exe), value.MakeStr(pidFile), pipeHelperOpts(true)})
	if err != nil || v.IsErr() {
		t.Fatalf("start = %s, %v", v.Display(), err)
	}
	p := v.Obj().(*Proc)
	select {
	case <-p.done:
	case <-time.After(8 * time.Second):
		p.terminate()
		t.Fatal("supervised descendant prevented started-process completion")
	}
	pid := waitHelperPID(t, pidFile)
	if !waitHelperGone(pid, 2*time.Second) {
		terminateHelperProcess(pid)
		t.Fatal("supervise=true left the inherited-stdio descendant alive")
	}
	if got := collectProcStdout(t, p); !strings.Contains(got, "root-output") {
		t.Fatalf("supervised drain lost ordinary root output: %q", got)
	}
}
