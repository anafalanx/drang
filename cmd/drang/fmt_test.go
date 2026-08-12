package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnifiedDiff(t *testing.T) {
	d, err := unifiedDiff("x", "a\nb\nc\n", "a\nB\nc\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d, "-b") || !strings.Contains(d, "+B") || !strings.Contains(d, "  a") {
		t.Errorf("diff should show -b/+B and context a:\n%s", d)
	}
}

func TestUnifiedDiffLargeInputUsesBoundedFallback(t *testing.T) {
	old := maxDiffLCSCells
	maxDiffLCSCells = 4
	t.Cleanup(func() { maxDiffLCSCells = old })

	d, err := unifiedDiff("x", "same\nold\nend\n", "same\nnew\nend\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"  same\n", "-old\n", "+new\n", "  end\n"} {
		if !strings.Contains(d, want) {
			t.Fatalf("bounded fallback missing %q:\n%s", want, d)
		}
	}
}

func TestUnifiedDiffRejectsExcessiveLineAndOutputAllocation(t *testing.T) {
	oldLines, oldBytes := maxDiffLines, maxDiffOutputBytes
	maxDiffLines, maxDiffOutputBytes = 2, 16
	t.Cleanup(func() { maxDiffLines, maxDiffOutputBytes = oldLines, oldBytes })

	if _, err := unifiedDiff("x", "a\nb\nc\n", "x\n"); err == nil || !strings.Contains(err.Error(), "line limit") {
		t.Fatalf("line-budget error = %v", err)
	}
	if _, err := unifiedDiff("long-name", "a\n", "b\n"); err == nil || !strings.Contains(err.Error(), "output exceeds") {
		t.Fatalf("output-budget error = %v", err)
	}
}

func TestUnifiedDiffCountsActualAlignedOutput(t *testing.T) {
	oldCells, oldBytes := maxDiffLCSCells, maxDiffOutputBytes
	maxDiffLCSCells, maxDiffOutputBytes = 1, 120
	t.Cleanup(func() { maxDiffLCSCells, maxDiffOutputBytes = oldCells, oldBytes })

	common := strings.Repeat("x", 60)
	d, err := unifiedDiff("x", common+"\nold\n", common+"\nnew\n")
	if err != nil {
		t.Fatalf("an aligned diff below the actual output cap was rejected: %v", err)
	}
	if int64(len(d)) > maxDiffOutputBytes || !strings.Contains(d, "  "+common+"\n") {
		t.Fatalf("unexpected bounded diff (%d bytes):\n%s", len(d), d)
	}
}

func TestDiffLineCountMatchesSplitLines(t *testing.T) {
	for _, s := range []string{"", "a", "a\n", "\n", "a\nb", "a\nb\n"} {
		if got, want := diffLineCount(s), int64(len(splitLines(s))); got != want {
			t.Fatalf("diffLineCount(%q) = %d, want %d", s, got, want)
		}
	}
}

func TestSplitLines(t *testing.T) {
	if got := splitLines("a\nb\n"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("trailing newline: %#v", got)
	}
	if got := splitLines("a\nb"); len(got) != 2 {
		t.Errorf("no trailing newline: %#v", got)
	}
}

func TestWriteFileAtomic(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.dr")
	if err := os.WriteFile(p, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(p, "new content"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "new content" {
		t.Errorf("got %q", string(b))
	}
}

func TestIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	rw := filepath.Join(dir, "rw.dr")
	ro := filepath.Join(dir, "ro.dr")
	if err := os.WriteFile(rw, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ro, []byte("x"), 0o444); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(ro, 0o600) // let TempDir cleanup remove it
	if isReadOnly(rw) {
		t.Errorf("rw.dr (0644) reported read-only")
	}
	if !isReadOnly(ro) {
		t.Errorf("ro.dr (0444) not reported read-only")
	}
}

func TestExpandFmtPaths(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.dr"), []byte("1"), 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("2"), 0644)
	os.Mkdir(filepath.Join(dir, ".git"), 0755)
	os.WriteFile(filepath.Join(dir, ".git", "c.dr"), []byte("3"), 0644)
	got, err := expandFmtPaths([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || filepath.Base(got[0]) != "a.dr" {
		t.Errorf("want only a.dr (skip .txt and .git); got %#v", got)
	}
}
