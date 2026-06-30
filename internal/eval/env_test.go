package eval

import (
	"runtime"
	"testing"
)

// envKeyMatch must apply the host's case rule: case-insensitive only when folding (Windows),
// case-sensitive otherwise (Unix). Tested in both modes so the Unix semantics are validated
// even on a Windows dev box.
func TestEnvKeyMatch(t *testing.T) {
	// fold = false (Unix): only exact matches
	if envKeyMatch("PATH", "path", false) {
		t.Error("case-sensitive must not match PATH/path")
	}
	if !envKeyMatch("PATH", "PATH", false) {
		t.Error("case-sensitive must match identical names")
	}
	// fold = true (Windows): case-insensitive
	if !envKeyMatch("PATH", "path", true) {
		t.Error("fold must match PATH/path")
	}
	if !envKeyMatch("Path", "PATH", true) {
		t.Error("fold must match Path/PATH")
	}
	if envKeyMatch("PATH", "PATHEXT", true) {
		t.Error("fold must not match different names")
	}
}

// A differently-cased overlay key must replace the inherited var on Windows, but add a
// distinct var on Unix (where env names are case-sensitive). This branches on GOOS so it
// validates the correct behavior on whichever host runs it.
func TestSetEnvHostCaseRule(t *testing.T) {
	got := setEnvFold([]string{"PATH=/usr/bin"}, "Path", "/custom")
	if runtime.GOOS == "windows" {
		if len(got) != 1 || got[0] != "Path=/custom" {
			t.Errorf("windows: a differently-cased overlay should replace in place, got %v", got)
		}
	} else {
		if len(got) != 2 || got[0] != "PATH=/usr/bin" || got[1] != "Path=/custom" {
			t.Errorf("unix: a differently-cased overlay should add a distinct var and keep PATH, got %v", got)
		}
	}
}
