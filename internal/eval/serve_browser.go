package eval

// Clamped system-browser launch for serve(). Windows-only (drang targets Win 11+).
//
// We open the served URL in Microsoft Edge's --app mode: a single app window with
// no address bar, tabs, or browser chrome, running against a throwaway
// --user-data-dir (its own isolated profile — no extensions, history, or sync). On
// Win 11 Edge is always present, so this is the reliable "system browser, clamped"
// target. If Edge can't be found we fall back to the default browser (unclamped).

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/anafalanx/drang/internal/winjob"
)

// clampFlags lock the app instance down: no first-run/EULA/default-browser prompts,
// no background phone-home, no sync, no extensions, no silent updater. Combined with
// the isolated --user-data-dir and the 127.0.0.1 + token gate, nothing else on the
// box can see or steer the window.
var clampFlags = []string{
	"--no-first-run",
	"--no-default-browser-check",
	"--disable-background-networking",
	"--disable-sync",
	"--disable-extensions",
	"--disable-background-mode",
	"--disable-component-update",
	"--no-service-autorun",
	"--disable-features=Translate,MediaRouter",
}

// clampedBrowser owns Edge's entire process tree. Edge may hand an app launch to
// a descendant and let the first msedge.exe exit, so waiting on only that first
// PID can tear the HTTP server down underneath a still-open blank window.
type clampedBrowser struct {
	job     *winjob.Job
	monitor *winjob.Monitor
	process *winjob.Process
	key     uintptr
}

// Wait blocks until the whole isolated Edge process tree has drained. The job
// completion event is the fast path; the accounting query is a loss-proof
// fallback because completion-port notifications are documented as best-effort.
func (b *clampedBrowser) Wait() error {
	defer b.monitor.Close()
	defer b.job.Close()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	events := b.monitor.Events()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if ev.Job == b.key && ev.Kind == winjob.EventActiveZero {
				return b.reap()
			}
		case <-ticker.C:
			active, err := b.job.ActiveProcessCount()
			if err != nil {
				return err
			}
			if active == 0 {
				return b.reap()
			}
		}
	}
}

func (b *clampedBrowser) reap() error {
	code, err := b.process.Wait()
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("clamped browser exited with code %d", code)
	}
	return nil
}

// launchClampedBrowser starts Edge in --app mode against an isolated profile,
// born into a dedicated Job Object so every descendant is tracked race-free. An
// error means Edge was not found or could not launch (caller falls back to the
// default browser).
func launchClampedBrowser(url, profileDir string) (*clampedBrowser, error) {
	if profileDir == "" {
		return nil, fmt.Errorf("isolated profile directory is required")
	}
	edge := findEdge()
	if edge == "" {
		return nil, fmt.Errorf("msedge.exe not found")
	}
	args := append([]string{"--app=" + url}, clampFlags...)
	args = append(args, "--user-data-dir="+profileDir)
	return launchClampedProcess(edge, args)
}

// launchClampedProcess is the job-contained process-tree launcher shared by the
// Edge path and its deterministic handoff regression test.
func launchClampedProcess(exe string, args []string) (*clampedBrowser, error) {
	job, err := winjob.New(true)
	if err != nil {
		return nil, err
	}
	monitor, err := winjob.NewMonitor()
	if err != nil {
		job.Close()
		return nil, err
	}
	key, err := monitor.Watch(job)
	if err != nil {
		monitor.Close()
		job.Close()
		return nil, err
	}
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		monitor.Close()
		job.Close()
		return nil, err
	}
	stdout, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		stdin.Close()
		monitor.Close()
		job.Close()
		return nil, err
	}
	defer stdin.Close()
	defer stdout.Close()
	argv := append([]string{exe}, args...)
	process, err := winjob.LaunchExe(exe, argv, "", nil, []*winjob.Job{job}, winjob.Stdio{
		Stdin: stdin, Stdout: stdout, Stderr: stdout,
	})
	if err != nil {
		monitor.Close()
		job.Close()
		return nil, err
	}
	return &clampedBrowser{job: job, monitor: monitor, process: process, key: key}, nil
}

// findEdge locates msedge.exe in the standard install locations, then PATH.
func findEdge() string {
	for _, base := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles"), os.Getenv("LocalAppData")} {
		if base == "" {
			continue
		}
		p := filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe")
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath("msedge.exe"); err == nil {
		return p
	}
	return ""
}

// openDefaultBrowser opens url in the user's default browser via ShellExecute
// (rundll32, no shell interpreter). Unclamped fallback only.
func openDefaultBrowser(url string) {
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}
