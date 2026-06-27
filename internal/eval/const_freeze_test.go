package eval

import (
	"strings"
	"testing"
)

// These exercise the concurrency payoff of deep-frozen constants: a callback that
// tries to mutate a captured constant is rejected (a catchable error) rather than
// racing. Run under `go test -race` in CI, they assert the race is gone.

func TestPmapOverConstIsRaceFree(t *testing.T) {
	src := `$shared ::= [0]
$r := [1,2,3,4,5,6,7,8] |> pmap(|$x| push($shared, $x))
say($r)
say(len($shared))`
	got := run(t, src)
	if !strings.Contains(got, "frozen") {
		t.Errorf("expected pmap workers to be rejected mutating a frozen constant, got %q", got)
	}
	if !strings.HasSuffix(strings.TrimRight(got, "\n"), "1") {
		t.Errorf("expected the constant to be unmutated (len 1), got %q", got)
	}
}

func TestSpawnOverConstIsRaceFree(t *testing.T) {
	src := `$shared ::= [0]
$t := spawn(|| push($shared, 1))
say(await($t))
say(len($shared))`
	got := run(t, src)
	if !strings.Contains(got, "frozen") {
		t.Errorf("expected the spawned task to be rejected mutating a frozen constant, got %q", got)
	}
	if !strings.HasSuffix(strings.TrimRight(got, "\n"), "1") {
		t.Errorf("expected the constant to be unmutated (len 1), got %q", got)
	}
}
