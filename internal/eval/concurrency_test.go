package eval

import (
	"bytes"
	"strings"
	"testing"

	"github.com/anafalanx/lang3/internal/parser"
	"github.com/anafalanx/lang3/internal/value"
)

func runWithEnv(t *testing.T, env *Env, src string) string {
	t.Helper()
	var buf bytes.Buffer
	old := stdout
	stdout = &buf
	defer func() { stdout = old }()
	p := parser.New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	if err := RunProgram(prog, env); err != nil {
		t.Fatalf("run: %v", err)
	}
	return buf.String()
}

// TestSpawnFanInStress spawns many tasks and fans the results back in. Run under
// `go test -race` to exercise the goroutine boundary + DeepCopy isolation.
func TestSpawnFanInStress(t *testing.T) {
	env := NewEnv()
	const N = 60
	items := make([]value.Value, N)
	for i := range items {
		items[i] = value.MakeInt(int64(i))
	}
	env.define("items", value.MakeArray(items), false)

	src := `$tasks := $items |> map(|$n| spawn(|$x| $x * $x, $n))
$results := $tasks |> map(|$t| await($t))
say("sum", reduce($results, 0, |$a, $b| $a + $b))`
	// sum of i*i for i in 0..59 = 59*60*119/6 = 70210
	if got := runWithEnv(t, env, src); !strings.Contains(got, "sum 70210") {
		t.Errorf("fan-in sum wrong: %q", got)
	}
}

// TestChannelProducerConsumer runs a producer goroutine streaming into a channel
// while the main goroutine drains it. Race-tested.
func TestChannelProducerConsumer(t *testing.T) {
	env := NewEnv()
	src := `$c := chan()
spawn(|$out| {
  for $i in 1..100 { send($out, $i) }
  close($out)
}, $c)
$got := drain($c)
say("count", len($got), "last", $got[len($got) - 1])`
	if got := runWithEnv(t, env, src); !strings.Contains(got, "count 100 last 100") {
		t.Errorf("producer/consumer wrong: %q", got)
	}
}

// TestSpawnArgsAreCopied confirms copy-on-send: mutating the source array after
// spawn does not affect the task's isolated copy.
func TestSpawnArgsAreCopied(t *testing.T) {
	env := NewEnv()
	src := `$a := [1, 2, 3]
$t := spawn(|$xs| { push($xs, 99); len($xs) }, $a)
$n := await($t)
say("task-len", $n, "src-len", len($a))`
	// task sees its own copy [1,2,3,99] -> 4; the source $a is untouched -> 3
	if got := runWithEnv(t, env, src); !strings.Contains(got, "task-len 4 src-len 3") {
		t.Errorf("copy-on-send broken: %q", got)
	}
}

// TestChannelSendCloseRaceSafe runs a producer, a concurrent closer, and a
// drainer over one shared channel — the pattern the review found racing. Under
// `go test -race` this must stay clean (close signals via `done`, not by closing
// the data channel).
func TestChannelSendCloseRaceSafe(t *testing.T) {
	env := NewEnv()
	src := `$c := chan()
$sender := spawn(|$ch| { for $i in 0..500 { send($ch, $i) } }, $c)
$closer := spawn(|$ch| close($ch), $c)
$drainer := spawn(|$ch| drain($ch), $c)
await($sender)
await($closer)
await($drainer)
say("done")`
	if got := runWithEnv(t, env, src); !strings.Contains(got, "done") {
		t.Errorf("send/close race repro hung or failed: %q", got)
	}
}
