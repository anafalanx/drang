package main

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestReapTargets locks the reaper's framing: "+ PID" registers, "- PID" deregisters, the
// set at EOF is what gets killed, and malformed/degenerate lines are ignored rather than
// crashing the reaper (which must be unkillably robust — it's the last line of defense).
func TestReapTargets(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []int
	}{
		{"register-two", "+ 100\n+ 200\n", []int{100, 200}},
		{"deregister-one", "+ 100\n+ 200\n- 100\n", []int{200}},
		{"dereg-before-reg", "- 50\n+ 7\n", []int{7}},
		{"reg-then-dereg-empty", "+ 9\n- 9\n", []int{}},
		{"no-trailing-newline", "+ 42", []int{42}},
		{"empty-stream", "", []int{}},
		{"malformed-ignored", "+ 1\ngarbage\n+ abc\nxyz 5\n+ 2\n", []int{1, 2}},
		{"nonpositive-ignored", "+ -5\n+ 0\n+ 3\n", []int{3}},
		{"surrounding-whitespace", "  +   42  \n", []int{42}},
		{"short-lines-ignored", "+\n-\n \n+ 8\n", []int{8}},
		{"duplicate-register", "+ 5\n+ 5\n- 5\n", []int{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := reapTargets(strings.NewReader(c.in))
			sort.Ints(got)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("reapTargets(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
