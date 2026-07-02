package winjob

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMakeBatchCmdLine pins the exact command line built for a batch launch. The security property
// is that no argument text can escape its quoting to become a cmd.exe command or a %VAR% expansion;
// these golden strings are how that property is checked without spawning a process.
func TestMakeBatchCmdLine(t *testing.T) {
	const pre = `cmd.exe /e:ON /v:OFF /d /c "`
	tests := []struct {
		name   string
		script string
		args   []string
		want   string
	}{
		{"no args", `C:\x.bat`, nil, pre + `"C:\x.bat""`},
		{"plain arg unquoted", `C:\x.bat`, []string{"hello"}, pre + `"C:\x.bat" hello"`},
		{"space forces quote", `C:\x.bat`, []string{"a b"}, pre + `"C:\x.bat" "a b""`},
		{"embedded quote is doubled", `C:\x.bat`, []string{`a"b`}, pre + `"C:\x.bat" "a""b""`},
		{"ampersand is quoted, not live", `C:\x.bat`, []string{"a&b"}, pre + `"C:\x.bat" "a&b""`},
		{"percent is neutralized", `C:\x.bat`, []string{"%FOO%"}, pre + `"C:\x.bat" "%%cd:~,%FOO%%cd:~,%""`},
		{"empty arg is quoted", `C:\x.bat`, []string{""}, pre + `"C:\x.bat" """`},
		{"trailing backslash doubled", `C:\x.bat`, []string{`foo\`}, pre + `"C:\x.bat" "foo\\""`},
		{"lone backslash needs no quote", `C:\x.bat`, []string{`a\b`}, pre + `"C:\x.bat" a\b"`},
		{"the classic injection payload is inert", `C:\x.bat`, []string{`a" & echo INJECTED & echo`},
			pre + `"C:\x.bat" "a"" & echo INJECTED & echo""`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := makeBatchCmdLine(tt.script, tt.args)
			if err != nil {
				t.Fatalf("makeBatchCmdLine: %v", err)
			}
			if got != tt.want {
				t.Errorf("makeBatchCmdLine\n got %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestMakeBatchCmdLineRejects(t *testing.T) {
	tests := []struct {
		name   string
		script string
		args   []string
	}{
		{"script with quote", `C:\a"b.bat`, nil},
		{"script trailing backslash", `C:\x\`, nil},
		{"script with NUL", "C:\\x\x00.bat", nil},
		{"arg with NUL", `C:\x.bat`, []string{"a\x00b"}},
		{"arg with CR", `C:\x.bat`, []string{"a\rb"}},
		{"arg with LF", `C:\x.bat`, []string{"a\nb"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := makeBatchCmdLine(tt.script, tt.args); err == nil {
				t.Errorf("makeBatchCmdLine(%q, %q) = nil error, want rejection", tt.script, tt.args)
			}
		})
	}
}

func TestIsBatchTarget(t *testing.T) {
	yes := []string{`x.bat`, `x.cmd`, `C:\dir\Tool.BAT`, `C:\dir\run.Cmd`}
	no := []string{`x.exe`, `x.com`, `x`, `x.bat.exe`, `C:\dir\tool.ps1`}
	for _, p := range yes {
		if !IsBatchTarget(p) {
			t.Errorf("IsBatchTarget(%q) = false, want true", p)
		}
	}
	for _, p := range no {
		if IsBatchTarget(p) {
			t.Errorf("IsBatchTarget(%q) = true, want false", p)
		}
	}
}

// TestLaunchBatchNoInjection is the decisive end-to-end proof: launching a real .bat with a hostile
// argument through winjob must pass the argument as inert data — cmd.exe must NOT execute the
// injected command. The canary is a side effect (a file the injected command would create); its
// absence, plus a clean exit, is the guarantee. Before the CVE-2024-24576 fix this test creates the
// canary file and fails.
func TestLaunchBatchNoInjection(t *testing.T) {
	dir := t.TempDir()
	batPath := filepath.Join(dir, "echo.bat")
	if err := os.WriteFile(batPath, []byte("@echo off\r\necho RAN\r\necho ARG=[%1]\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// If cmd interpreted the argument, `& echo owned> canary.txt &` would run and create the canary
	// in the child's cwd (dir). With the fix it is one inert, quoted argument.
	payload := `x" & echo owned> canary.txt & rem "`
	canary := filepath.Join(dir, "canary.txt")

	job := mustJob(t, false)
	defer job.Close()

	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	p, err := LaunchExe(batPath, []string{batPath, payload}, dir, childEnv(""), []*Job{job},
		Stdio{Stdin: inR, Stdout: outW, Stderr: errW})
	if err != nil {
		for _, f := range []*os.File{inR, inW, outR, outW, errR, errW} {
			f.Close()
		}
		t.Fatalf("LaunchExe: %v", err)
	}
	inR.Close()
	outW.Close()
	errW.Close()
	inW.Close()

	outB := readAllString(outR)
	_ = readAllString(errR)
	code, werr := p.Wait()
	if werr != nil {
		t.Fatalf("Wait: %v", werr)
	}

	if _, err := os.Stat(canary); err == nil {
		t.Fatalf("INJECTION: canary file %q was created — the argument was executed as a command", canary)
	}
	if !strings.Contains(outB, "RAN") {
		t.Errorf("batch did not run as expected; stdout=%q", outB)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0; stdout=%q", code, outB)
	}
}

func readAllString(f *os.File) string {
	defer f.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := f.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return sb.String()
}
