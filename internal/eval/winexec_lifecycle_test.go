package eval

import (
	"os"
	"testing"

	"github.com/anafalanx/drang/internal/winjob"
)

// A configured-limit command starts its monitor before CreateProcess. If launch then fails, the
// monitor drain goroutine retains jobCmd, so cleanup must be explicit; GC cannot break that cycle.
func TestJobCmdLaunchFailureClosesAndJoinsLimitMonitor(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c, err := newJobCmd(
		[]string{exe, "bad\x00argument"}, // UTF-16 conversion fails inside LaunchExe, after monitor setup
		execOpts{limits: winjob.Limits{ProcessMemoryBytes: 64 << 20}},
		nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("newJobCmd failed before monitor setup: %v", err)
	}
	if err := c.start(); err == nil {
		t.Fatal("start unexpectedly accepted a NUL-containing argument")
	}
	if c.monitor == nil || c.monDone == nil {
		t.Fatal("test did not reach the post-monitor launch-failure path")
	}
	select {
	case <-c.monDone:
	default:
		t.Fatal("limit-monitor drain goroutine was not joined before start returned")
	}
	if _, ok := <-c.monitor.Events(); ok {
		t.Fatal("monitor Events remained open after failed launch cleanup")
	}
	if c.job != nil {
		t.Fatal("failed launch retained its Job handle")
	}
	if len(c.childFiles) != 0 || len(c.parentPipes) != 0 {
		t.Fatal("failed launch retained stdio files")
	}
}
