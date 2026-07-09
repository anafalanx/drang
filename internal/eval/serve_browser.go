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
	"--disable-component-update",
	"--no-service-autorun",
	"--disable-features=Translate,MediaRouter",
}

// launchClampedBrowser starts Edge in --app mode against an isolated profile and
// returns the running process, whose exit signals the window has closed. An error
// means Edge wasn't found (caller falls back to the default browser).
func launchClampedBrowser(url, profileDir string) (*exec.Cmd, error) {
	edge := findEdge()
	if edge == "" {
		return nil, fmt.Errorf("msedge.exe not found")
	}
	args := append([]string{"--app=" + url}, clampFlags...)
	if profileDir != "" {
		args = append(args, "--user-data-dir="+profileDir)
	}
	cmd := exec.Command(edge, args...)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
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
