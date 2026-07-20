package eval

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const browserTreeHelperEnv = "DRANG_BROWSER_TREE_HELPER"

// TestMain re-execs this test binary as a launcher that hands work to a child
// and exits. That is the Edge failure shape: the first PID can disappear while
// the actual app process remains alive.
func TestMain(m *testing.M) {
	switch os.Getenv(browserTreeHelperEnv) {
	case "parent":
		browserTreeParent(os.Args[1:])
		os.Exit(0)
	case "child":
		browserTreeChild(os.Args[1:])
		os.Exit(0)
	case "exit-7":
		os.Exit(7)
	}
	os.Exit(m.Run())
}

func browserTreeParent(args []string) {
	if len(args) != 2 {
		os.Exit(2)
	}
	cmd := exec.Command(os.Args[0], args[0], args[1])
	cmd.Env = replaceEnv(os.Environ(), browserTreeHelperEnv, "child")
	if err := cmd.Start(); err != nil {
		os.Exit(3)
	}
	if err := os.WriteFile(args[0], []byte("ready"), 0o600); err != nil {
		os.Exit(4)
	}
}

func browserTreeChild(args []string) {
	if len(args) != 2 {
		os.Exit(2)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(args[1]); err == nil {
			return
		}
		if time.Now().After(deadline) {
			os.Exit(5)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func replaceEnv(env []string, key, val string) []string {
	prefix := key + "="
	for i, entry := range env {
		if len(entry) >= len(prefix) && entry[:len(prefix)] == prefix {
			env[i] = prefix + val
			return env
		}
	}
	return append(env, prefix+val)
}

func TestClampedBrowserWaitsForHandedOffChild(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	release := filepath.Join(dir, "release")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(browserTreeHelperEnv, "parent")
	browser, err := launchClampedProcess(exe, []string{ready, release})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- browser.Wait() }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper parent did not launch its child")
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("browser wait returned when only the launcher exited: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("browser wait did not return after the whole helper tree drained")
	}
}

func TestClampedBrowserRequiresIsolatedProfile(t *testing.T) {
	if _, err := launchClampedBrowser("http://127.0.0.1/", ""); err == nil {
		t.Fatal("launchClampedBrowser accepted an empty isolated-profile path")
	}
}

func TestClampedBrowserReportsNonzeroLauncherExit(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(browserTreeHelperEnv, "exit-7")
	browser, err := launchClampedProcess(exe, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = browser.Wait()
	if err == nil || !strings.Contains(err.Error(), "code 7") {
		t.Fatalf("browser wait error = %v, want exit code 7", err)
	}
}
