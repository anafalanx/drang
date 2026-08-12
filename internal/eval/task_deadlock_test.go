package eval

import (
	"strings"
	"testing"
)

func TestTaskSelfDependencyReturnsCatchableDeadlock(t *testing.T) {
	src := `$handoff := chan(1)
$task := spawn(|| await(recv($handoff)))
send($handoff, $task)?
$result := await($task)
say(is_err($result), err_msg($result))`
	assertConcurrencyBoth(t, src, func(out string) bool {
		return strings.Contains(out, "true await would deadlock")
	})
}

func TestTaskMutualDependencyReturnsCatchableDeadlock(t *testing.T) {
	src := `$left_handoff := chan(1)
$right_handoff := chan(1)
$left := spawn(|| await(recv($left_handoff)))
$right := spawn(|| await(recv($right_handoff)))
send($left_handoff, $right)?
send($right_handoff, $left)?
$left_result := await($left)
$right_result := await($right)
say(is_err($left_result), is_err($right_result), err_msg($left_result), err_msg($right_result))`
	assertConcurrencyBoth(t, src, func(out string) bool {
		return strings.Contains(out, "true true await would deadlock")
	})
}

func TestTaskDeadlockRecoveryStress(t *testing.T) {
	src := `$caught := 0
for $i in 0..49 {
  $handoff := chan(1)
  $task := spawn(|| await(recv($handoff)))
  send($handoff, $task)?
  $result := await($task)
  if is_err($result) { $caught = $caught + 1 }
}
say($caught)`
	assertConcurrencyBoth(t, src, func(out string) bool { return out == "50\n" })
}

func TestTaskCompletionStillWakesManyAwaiters(t *testing.T) {
	src := `$task := spawn(|| { sleep(0.02); 7 })
$waiters := []
for $_ in 0..31 { push($waiters, spawn(|$target| await($target), $task)) }
$values := $waiters |> map(|$waiter| await($waiter))
say(len($values), sum($values))`
	assertConcurrencyBoth(t, src, func(out string) bool { return out == "32 224\n" })
}
