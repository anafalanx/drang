package main

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"
)

// runReap is the hidden `drang --reap` side-car. A parent drang process spawns it, hands it
// the read end of a pipe as stdin, and keeps the write end open for the parent's whole life.
// The parent sends "+ PID" / "- PID" lines as supervised children start and finish. When the
// parent dies for ANY reason (clean exit, panic, SIGKILL, crash), the OS closes the write
// end, stdin hits EOF, and the reaper kills every still-registered child tree. This is the
// portable "children die with the parent" guarantee — no job objects, no pdeathsig.
func runReap() {
	for _, pid := range reapTargets(os.Stdin) {
		killProcTree(pid)
	}
	os.Exit(0)
}

// reapTargets drains the registration stream and returns the PIDs still live at EOF (i.e.
// registered with "+" and not since deregistered with "-"). Factored out so the framing can
// be unit-tested without spawning processes. Malformed lines are ignored.
func reapTargets(r io.Reader) []int {
	live := map[int]struct{}{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if len(line) < 2 || (line[0] != '+' && line[0] != '-') {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(line[1:]))
		if err != nil || pid <= 0 {
			continue
		}
		if line[0] == '+' {
			live[pid] = struct{}{}
		} else {
			delete(live, pid)
		}
	}
	out := make([]int, 0, len(live))
	for pid := range live {
		out = append(out, pid)
	}
	return out
}
