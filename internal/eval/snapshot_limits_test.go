package eval

import (
	"fmt"
	"strings"
	"testing"

	"github.com/anafalanx/drang/internal/value"
)

func deeplyNestedValue(depth int) value.Value {
	v := value.MakeInt(1)
	for i := 0; i < depth; i++ {
		v = value.MakeArray([]value.Value{v})
	}
	return v
}

func TestClosureSnapshotDepthLimitWalkerVMParity(t *testing.T) {
	cases := []struct {
		name string
		call string
		want string
	}{
		{"spawn-capture", `spawn(|| len($deep))`, "spawn: snapshot exceeds depth limit"},
		{"pmap-capture", `pmap([1], |$x| $x + len($deep))`, "pmap: snapshot exceeds depth limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := fmt.Sprintf(`$deep := 0
for $i in 0..%d { $deep = [$deep] }
$result := %s
say(is_err($result), err_code($result), err_msg($result))`, maxClosureSnapshotDepth+8, tc.call)
			walker, walkerErr := runBackend(t, src, false)
			vm, vmErr := runBackend(t, src, true)
			if walkerErr != nil || vmErr != nil {
				t.Fatalf("snapshot ceiling aborted: walker=%v vm=%v", walkerErr, vmErr)
			}
			if walker != vm {
				t.Fatalf("snapshot ceiling parity: walker=%q vm=%q", walker, vm)
			}
			if !strings.Contains(walker, "true 137 "+tc.want) {
				t.Fatalf("snapshot ceiling was not a catchable resource Err: %q", walker)
			}
		})
	}
}

func TestClosureSnapshotNodeBudget(t *testing.T) {
	input := value.MakeArray([]value.Value{
		value.MakeInt(1), value.MakeInt(2), value.MakeInt(3),
	})
	_, err := newClosureSnapshotWithLimits(8, 3).cloneValue(input, 0)
	if err == nil || !strings.Contains(err.Error(), "snapshot exceeds node limit 3") {
		t.Fatalf("node-limited snapshot error = %v", err)
	}
}

func TestSpawnArgumentAndPmapElementSnapshotDepthErrors(t *testing.T) {
	identity := &Function{Name: "identity", Builtin: func(args []value.Value) (value.Value, error) {
		return args[0], nil
	}}
	fnValue := value.MakeObj(value.Func, identity)
	deep := deeplyNestedValue(maxClosureSnapshotDepth + 8)

	spawned, err := evalSpawn([]value.Value{fnValue, deep})
	if err != nil || !spawned.IsErr() || spawned.ErrCode() != 137 || !strings.Contains(spawned.Display(), "spawn: snapshot exceeds depth limit") {
		t.Fatalf("deep spawn argument = %s, %v", spawned.Display(), err)
	}

	mapped, err := applyPmap(identity, deep, 0, 0)
	if err != nil || !mapped.IsErr() || mapped.ErrCode() != 137 || !strings.Contains(mapped.Display(), "pmap: snapshot exceeds depth limit") {
		t.Fatalf("deep pmap element = %s, %v", mapped.Display(), err)
	}
}

func TestSequentialFallbackPropagatesSnapshotDepthError(t *testing.T) {
	env := NewEnv()
	if err := env.define("deep", deeplyNestedValue(maxClosureSnapshotDepth+8), false); err != nil {
		t.Fatal(err)
	}
	fn := &Function{Name: "captured", Params: []string{"x"}, Env: env}
	src := []value.Value{value.MakeInt(1)}
	got, err := pmapSequential(src, fn, make([]value.Value, len(src)))
	if err != nil || !got.IsErr() || got.ErrCode() != 137 || !strings.Contains(got.Display(), "pmap: snapshot exceeds depth limit") {
		t.Fatalf("sequential-fallback snapshot = %s, %v", got.Display(), err)
	}
}

func TestCheckedSnapshotStillPreservesCycleAndAlias(t *testing.T) {
	a := &value.Array{}
	av := value.MakeObj(value.Arr, a)
	a.Elems = []value.Value{av, av}
	cloned, err := newClosureSnapshot().cloneValue(av, 0)
	if err != nil {
		t.Fatal(err)
	}
	cp := cloned.Obj().(*value.Array)
	if cp == a || cp.Elems[0].Obj() != cp || cp.Elems[1].Obj() != cp {
		t.Fatal("checked snapshot lost cycle/alias topology")
	}
}

func TestFlatMapMaterializationLimit(t *testing.T) {
	double := &Function{Name: "double", Builtin: func(args []value.Value) (value.Value, error) {
		return value.MakeArray([]value.Value{args[0], args[0]}), nil
	}}
	src := &value.Array{Elems: []value.Value{value.MakeInt(1), value.MakeInt(2)}}

	atLimit, err := hofFlatMapLimit(src, double, 0, 4)
	if err != nil || atLimit.IsErr() || atLimit.Obj().(*value.Array).Len() != 4 {
		t.Fatalf("flat_map exact limit = %s, %v", atLimit.Display(), err)
	}
	over, err := hofFlatMapLimit(src, double, 0, 3)
	if err != nil || !over.IsErr() || over.ErrCode() != 1 || !strings.Contains(over.Display(), "3-element collection limit") {
		t.Fatalf("flat_map over limit = %s, %v", over.Display(), err)
	}
}
