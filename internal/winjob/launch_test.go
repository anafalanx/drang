package winjob

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const helperEnv = "DRANG_WINJOB_HELPER"

// TestMain lets the test binary re-exec itself as a controllable child ("helper") for the launch
// tests — the standard Go os/exec testing pattern. The mode is selected by the helperEnv var so
// it never collides with go-test flags on the parent's argv.
func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		helperMain(mode, os.Args[1:])
		return
	}
	os.Exit(m.Run())
}

func helperMain(mode string, args []string) {
	switch mode {
	case "sleep":
		time.Sleep(60 * time.Second)
	case "cat":
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "echo-args":
		for _, a := range args {
			fmt.Println(a)
		}
	case "to-stdout":
		fmt.Fprint(os.Stdout, args[0])
	case "to-stderr":
		fmt.Fprint(os.Stderr, args[0])
	case "print-env":
		fmt.Fprint(os.Stdout, os.Getenv(args[0]))
	case "print-cwd":
		d, _ := os.Getwd()
		fmt.Fprint(os.Stdout, d)
	case "exit":
		n, _ := strconv.Atoi(args[0])
		os.Exit(n)
	case "spawn-grandchild":
		// A plain os/exec grandchild; because this helper is a job member (born-in-job), the
		// grandchild auto-joins the same job by inheritance. It would outlive us if not killed.
		gc := exec.Command("cmd", "/c", "ping", "-n", "60", "127.0.0.1") // ~60s, stdout discarded
		if err := gc.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "grandchild start:", err)
			os.Exit(1)
		}
		fmt.Println(gc.Process.Pid) // report the grandchild pid, then stay alive
		time.Sleep(60 * time.Second)
	default:
		fmt.Fprintln(os.Stderr, "unknown helper mode:", mode)
		os.Exit(2)
	}
	os.Exit(0)
}

func selfExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

// childEnv builds a child environment: the helper mode plus a baseline so the Go child loads
// cleanly, plus any extra K=V entries.
func childEnv(mode string, extra ...string) []string {
	env := []string{helperEnv + "=" + mode}
	for _, k := range []string{"SystemRoot", "windir", "PATH", "TEMP", "TMP"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return append(env, extra...)
}

func mustJob(t *testing.T, killOnClose bool) *Job {
	t.Helper()
	j, err := New(killOnClose)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

// run launches argv, feeds stdin, captures stdout+stderr, and returns them with the exit code.
// It drains both output pipes concurrently so a chatty child can't deadlock on a full pipe.
func run(t *testing.T, jobs []*Job, env []string, stdin string, argv ...string) (string, string, int) {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	p, err := Launch(argv, "", env, jobs, Stdio{Stdin: inR, Stdout: outW, Stderr: errW})
	if err != nil {
		for _, f := range []*os.File{inR, inW, outR, outW, errR, errW} {
			f.Close()
		}
		t.Fatalf("Launch: %v", err)
	}
	inR.Close() // parent no longer needs the child's ends
	outW.Close()
	errW.Close()

	go func() { _, _ = io.WriteString(inW, stdin); inW.Close() }()

	var outB, errB []byte
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); outB, _ = io.ReadAll(outR) }()
	go func() { defer wg.Done(); errB, _ = io.ReadAll(errR) }()

	code, werr := p.Wait()
	wg.Wait()
	outR.Close()
	errR.Close()
	if werr != nil {
		t.Fatalf("Wait: %v", werr)
	}
	return string(outB), string(errB), code
}

func TestLaunchExitCode(t *testing.T) {
	job := mustJob(t, false)
	defer job.Close()
	_, _, code := run(t, []*Job{job}, childEnv("exit"), "", selfExe(t), "7")
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
}

func TestLaunchStdout(t *testing.T) {
	job := mustJob(t, false)
	defer job.Close()
	out, _, code := run(t, []*Job{job}, childEnv("to-stdout"), "", selfExe(t), "hello stdout")
	if out != "hello stdout" || code != 0 {
		t.Errorf("stdout=%q code=%d, want %q 0", out, code, "hello stdout")
	}
}

func TestLaunchStderr(t *testing.T) {
	job := mustJob(t, false)
	defer job.Close()
	_, errOut, code := run(t, []*Job{job}, childEnv("to-stderr"), "", selfExe(t), "boom stderr")
	if errOut != "boom stderr" || code != 0 {
		t.Errorf("stderr=%q code=%d, want %q 0", errOut, code, "boom stderr")
	}
}

func TestLaunchStdin(t *testing.T) {
	job := mustJob(t, false)
	defer job.Close()
	out, _, _ := run(t, []*Job{job}, childEnv("cat"), "feed me please", selfExe(t))
	if out != "feed me please" {
		t.Errorf("stdin round-trip = %q, want %q", out, "feed me please")
	}
}

func TestLaunchEnvExplicit(t *testing.T) {
	job := mustJob(t, false)
	defer job.Close()
	out, _, _ := run(t, []*Job{job}, childEnv("print-env", "MYVAR=explicit-value"), "", selfExe(t), "MYVAR")
	if out != "explicit-value" {
		t.Errorf("explicit env = %q, want %q", out, "explicit-value")
	}
}

func TestLaunchEnvInherit(t *testing.T) {
	t.Setenv(helperEnv, "print-env")
	t.Setenv("INHERITED_VAR", "from-parent")
	job := mustJob(t, false)
	defer job.Close()
	// env=nil => the child inherits the parent's environment (which now carries both vars).
	out, _, _ := run(t, []*Job{job}, nil, "", selfExe(t), "INHERITED_VAR")
	if out != "from-parent" {
		t.Errorf("inherited env = %q, want %q", out, "from-parent")
	}
}

func TestLaunchCwd(t *testing.T) {
	dir := t.TempDir()
	job := mustJob(t, false)
	defer job.Close()
	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	defer func() {
		for _, f := range []*os.File{inW, outR, errR} {
			f.Close()
		}
	}()
	p, err := Launch([]string{selfExe(t)}, dir, childEnv("print-cwd"), []*Job{job}, Stdio{Stdin: inR, Stdout: outW, Stderr: errW})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	inR.Close()
	outW.Close()
	errW.Close()
	inW.Close()
	out, _ := io.ReadAll(outR)
	_, _ = io.ReadAll(errR)
	if _, err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	// The child may report a resolved form (e.g. an 8.3/symlink-normalized path); compare by base.
	got := strings.TrimSpace(string(out))
	if !strings.EqualFold(got, dir) && !strings.EqualFold(strings.TrimPrefix(got, `\\?\`), dir) {
		// Fall back to comparing the final path element, which must match.
		wantBase := dir[strings.LastIndexAny(dir, `\/`)+1:]
		gotBase := got[strings.LastIndexAny(got, `\/`)+1:]
		if !strings.EqualFold(gotBase, wantBase) {
			t.Errorf("cwd = %q, want %q", got, dir)
		}
	}
}

// TestLaunchArgEscaping round-trips a battery of nasty arguments through syscall.EscapeArg and
// the child's CommandLineToArgvW parse — the classic Windows quoting minefield.
func TestLaunchArgEscaping(t *testing.T) {
	job := mustJob(t, false)
	defer job.Close()
	args := []string{
		"simple",
		"with space",
		`with"quote`,
		`back\slash`,
		`two\\slashes`,
		`trailing\`,
		`c:\path\to\file`,
		"tab\there",
		"",
		"unicode-café-日本",
	}
	argv := append([]string{selfExe(t)}, args...)
	out, _, code := run(t, []*Job{job}, childEnv("echo-args"), "", argv...)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	lines := strings.Split(out, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1] // drop the trailing empty from the final Println newline
	}
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	if len(lines) != len(args) {
		t.Fatalf("got %d args %q, want %d %q", len(lines), lines, len(args), args)
	}
	for i := range args {
		if lines[i] != args[i] {
			t.Errorf("arg %d: got %q want %q", i, lines[i], args[i])
		}
	}
}

// TestLaunchBornInJob proves the child is a member of BOTH jobs it was born into (root + command
// nesting) — the race-free born-in-job guarantee.
func TestLaunchBornInJob(t *testing.T) {
	root := mustJob(t, true)
	defer root.Close()
	cmd := mustJob(t, false)
	defer cmd.Close()

	nul, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer nul.Close()
	p, err := Launch([]string{selfExe(t)}, "", childEnv("sleep"), []*Job{root, cmd}, Stdio{Stdin: nul, Stdout: nul, Stderr: nul})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer cmd.Terminate(1)

	for name, job := range map[string]*Job{"root": root, "command": cmd} {
		in, err := InJob(p.Handle(), job.handle)
		if err != nil {
			t.Fatalf("InJob(%s): %v", name, err)
		}
		if !in {
			t.Errorf("child is not a member of the %s job", name)
		}
	}
}

// TestLaunchGrandchildTreeKill is the decisive test: a grandchild the child forks is a DESCENDANT
// in the command job (born-in-job + inheritance), so TerminateJobObject reaches it — the precision
// that option ③ (adopt-after-start, no root) could not guarantee.
func TestLaunchGrandchildTreeKill(t *testing.T) {
	job := mustJob(t, false)
	defer job.Close()
	gcH, childProc, outR := launchGrandchildTree(t, job)
	defer windows.CloseHandle(gcH)
	defer outR.Close()

	if err := job.Terminate(1); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	assertDeadSoon(t, "child", childProc.Handle())
	assertHandleDeadSoon(t, "grandchild", gcH)
}

// TestLaunchKillOnCloseGrandchild proves die-with-parent reaches the grandchild too: closing the
// only handle to a KILL_ON_JOB_CLOSE job kills the whole subtree.
func TestLaunchKillOnCloseGrandchild(t *testing.T) {
	job := mustJob(t, true)
	gcH, _, outR := launchGrandchildTree(t, job)
	defer windows.CloseHandle(gcH)
	defer outR.Close()

	_ = job.Close() // the only handle -> the subtree is terminated
	assertHandleDeadSoon(t, "grandchild", gcH)
}

// launchGrandchildTree launches the spawn-grandchild helper into job, reads the grandchild pid,
// and returns an open handle to the grandchild, the child Process, and the stdout reader.
func launchGrandchildTree(t *testing.T, job *Job) (windows.Handle, *Process, *os.File) {
	t.Helper()
	nul, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	p, err := Launch([]string{selfExe(t)}, "", childEnv("spawn-grandchild"), []*Job{job}, Stdio{Stdin: nul, Stdout: outW, Stderr: outW})
	nul.Close()
	if err != nil {
		outR.Close()
		outW.Close()
		t.Fatalf("Launch: %v", err)
	}
	outW.Close()

	line, err := bufio.NewReader(outR).ReadString('\n')
	if err != nil {
		t.Fatalf("reading grandchild pid: %v", err)
	}
	gcPid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("bad grandchild pid %q: %v", line, err)
	}
	gcH, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(gcPid))
	if err != nil {
		t.Fatalf("OpenProcess grandchild %d: %v", gcPid, err)
	}
	return gcH, p, outR
}

func assertHandleDeadSoon(t *testing.T, name string, h windows.Handle) {
	t.Helper()
	s, err := windows.WaitForSingleObject(h, 10000)
	if err != nil {
		t.Fatalf("WaitForSingleObject(%s): %v", name, err)
	}
	if s != windows.WAIT_OBJECT_0 {
		t.Fatalf("%s survived (state 0x%x) — expected it to be killed", name, s)
	}
}

func assertDeadSoon(t *testing.T, name string, h windows.Handle) {
	t.Helper()
	assertHandleDeadSoon(t, name, h)
}

// TestLaunchConcurrent launches many children at once, each into its own job — the pmap fan-out
// case. It stresses the per-call attribute-list and handle setup for races (run with -race).
func TestLaunchConcurrent(t *testing.T) {
	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			job, err := New(false)
			if err != nil {
				errs <- err
				return
			}
			defer job.Close()
			_, _, code := run(t, []*Job{job}, childEnv("exit"), "", selfExe(t), strconv.Itoa(i%3))
			if code != i%3 {
				errs <- fmt.Errorf("child %d exit=%d want %d", i, code, i%3)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestLaunchManySequential launches and reaps many children to shake out handle leaks / gradual
// failures.
func TestLaunchManySequential(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping many-spawn test in -short")
	}
	job := mustJob(t, false)
	defer job.Close()
	for i := 0; i < 100; i++ {
		_, _, code := run(t, []*Job{job}, childEnv("exit"), "", selfExe(t), "0")
		if code != 0 {
			t.Fatalf("iteration %d: exit %d", i, code)
		}
	}
}

func TestLaunchBadExe(t *testing.T) {
	job := mustJob(t, false)
	defer job.Close()
	nul, _ := os.Open(os.DevNull)
	defer nul.Close()
	_, err := Launch([]string{`C:\this\does\not\exist\nope.exe`}, "", childEnv("exit"), []*Job{job}, Stdio{Stdin: nul, Stdout: nul, Stderr: nul})
	if err == nil {
		t.Fatal("expected an error launching a nonexistent executable")
	}
}

func TestLaunchNilStdioRejected(t *testing.T) {
	job := mustJob(t, false)
	defer job.Close()
	if _, err := Launch([]string{selfExe(t)}, "", childEnv("exit"), []*Job{job}, Stdio{}); err == nil {
		t.Fatal("expected an error for nil stdio handles")
	}
}

func TestLaunchNoJobRejected(t *testing.T) {
	nul, _ := os.Open(os.DevNull)
	defer nul.Close()
	if _, err := Launch([]string{selfExe(t)}, "", nil, nil, Stdio{Stdin: nul, Stdout: nul, Stderr: nul}); err == nil {
		t.Fatal("expected an error when no job is supplied")
	}
}
