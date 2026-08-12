package main

import (
	"strings"
	"testing"
)

func TestReadAllLimited(t *testing.T) {
	b, err := readAllLimited(strings.NewReader("12345"), 5, "test input")
	if err != nil || string(b) != "12345" {
		t.Fatalf("exact limit: bytes=%q err=%v", b, err)
	}
	if _, err := readAllLimited(strings.NewReader("123456"), 5, "test input"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("over limit error = %v", err)
	}
}
