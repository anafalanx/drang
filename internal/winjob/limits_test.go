package winjob

import (
	"os"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestSetLimitsWritesKernelState verifies SetLimits writes exactly the requested caps and flags to
// the kernel, and re-asserts KILL_ON_JOB_CLOSE in the same write (so die-with-parent is never
// dropped). It reads the state back with QueryInformationJobObject rather than trusting the write.
func TestSetLimitsWritesKernelState(t *testing.T) {
	j := mustJob(t, true) // created with killOnClose
	defer j.Close()

	lim := Limits{
		ProcessMemoryBytes: 64 << 20,
		JobMemoryBytes:     128 << 20,
		ProcessCPUTime:     5 * time.Second,
		JobCPUTime:         10 * time.Second,
		ActiveProcessCap:   3,
	}
	if err := j.SetLimits(lim); err != nil {
		t.Fatalf("SetLimits: %v", err)
	}

	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	var ret uint32
	if err := windows.QueryInformationJobObject(j.handle, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), &ret); err != nil {
		t.Fatalf("QueryInformationJobObject: %v", err)
	}

	f := info.BasicLimitInformation.LimitFlags
	for name, flag := range map[string]uint32{
		"KILL_ON_JOB_CLOSE": windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, // must survive the limit write
		"PROCESS_MEMORY":    windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY,
		"JOB_MEMORY":        windows.JOB_OBJECT_LIMIT_JOB_MEMORY,
		"PROCESS_TIME":      windows.JOB_OBJECT_LIMIT_PROCESS_TIME,
		"JOB_TIME":          windows.JOB_OBJECT_LIMIT_JOB_TIME,
		"ACTIVE_PROCESS":    windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS,
	} {
		if f&flag == 0 {
			t.Errorf("limit flag %s (0x%x) not set; flags=0x%x", name, flag, f)
		}
	}
	if got := uint64(info.ProcessMemoryLimit); got != lim.ProcessMemoryBytes {
		t.Errorf("ProcessMemoryLimit = %d, want %d", got, lim.ProcessMemoryBytes)
	}
	if got := uint64(info.JobMemoryLimit); got != lim.JobMemoryBytes {
		t.Errorf("JobMemoryLimit = %d, want %d", got, lim.JobMemoryBytes)
	}
	if got := info.BasicLimitInformation.PerProcessUserTimeLimit; got != lim.ProcessCPUTime.Nanoseconds()/100 {
		t.Errorf("PerProcessUserTimeLimit = %d ticks, want %d", got, lim.ProcessCPUTime.Nanoseconds()/100)
	}
	if got := info.BasicLimitInformation.PerJobUserTimeLimit; got != lim.JobCPUTime.Nanoseconds()/100 {
		t.Errorf("PerJobUserTimeLimit = %d ticks, want %d", got, lim.JobCPUTime.Nanoseconds()/100)
	}
	if got := info.BasicLimitInformation.ActiveProcessLimit; got != lim.ActiveProcessCap {
		t.Errorf("ActiveProcessLimit = %d, want %d", got, lim.ActiveProcessCap)
	}
}

// TestSetLimitsZeroPreservesKillOnClose: an all-zero Limits still re-asserts the base flag and sets
// nothing else — so calling SetLimits unconditionally is safe.
func TestSetLimitsZeroPreservesKillOnClose(t *testing.T) {
	j := mustJob(t, true)
	defer j.Close()
	if err := j.SetLimits(Limits{}); err != nil {
		t.Fatalf("SetLimits(zero): %v", err)
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	var ret uint32
	if err := windows.QueryInformationJobObject(j.handle, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), &ret); err != nil {
		t.Fatal(err)
	}
	if f := info.BasicLimitInformation.LimitFlags; f != windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE {
		t.Errorf("flags = 0x%x, want only KILL_ON_JOB_CLOSE (0x%x)", f, windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE)
	}
	if !(Limits{}).IsZero() {
		t.Error("Limits{}.IsZero() = false")
	}
}

// TestActiveProcessCapEnforced is the behavioral proof that a limit actually takes effect: with a
// 1-process cap, the born-in-job child (process 1) cannot spawn a grandchild (would be process 2),
// so the spawn-grandchild helper fails to start its grandchild and exits non-zero.
func TestActiveProcessCapEnforced(t *testing.T) {
	job := mustJob(t, false)
	defer job.Close()
	if err := job.SetLimits(Limits{ActiveProcessCap: 1}); err != nil {
		t.Fatalf("SetLimits: %v", err)
	}
	nul, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer nul.Close()
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	p, err := Launch([]string{selfExe(t)}, "", childEnv("spawn-grandchild"), []*Job{job},
		Stdio{Stdin: nul, Stdout: outW, Stderr: errW})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	outW.Close()
	errW.Close()
	_ = readAllString(outR)
	errText := readAllString(errR)
	code, werr := p.Wait()
	if werr != nil {
		t.Fatalf("Wait: %v", werr)
	}
	if code == 0 {
		t.Errorf("child exited 0 despite a 1-process cap blocking its grandchild; stderr=%q", errText)
	}
	if !strings.Contains(errText, "grandchild start") {
		t.Errorf("expected a grandchild-start failure, got stderr=%q", errText)
	}
}
