package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnifiedDiff(t *testing.T) {
	d := unifiedDiff("x", "a\nb\nc\n", "a\nB\nc\n")
	if !strings.Contains(d, "-b") || !strings.Contains(d, "+B") || !strings.Contains(d, "  a") {
		t.Errorf("diff should show -b/+B and context a:\n%s", d)
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

func TestExpandFmtPaths(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.dr"), []byte("1"), 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("2"), 0644)
	os.Mkdir(filepath.Join(dir, ".git"), 0755)
	os.WriteFile(filepath.Join(dir, ".git", "c.dr"), []byte("3"), 0644)
	got := expandFmtPaths([]string{dir})
	if len(got) != 1 || filepath.Base(got[0]) != "a.dr" {
		t.Errorf("want only a.dr (skip .txt and .git); got %#v", got)
	}
}
