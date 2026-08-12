package value

import (
	"math"
	"testing"
)

func TestMixedNumericEqualityIsExact(t *testing.T) {
	cases := []struct {
		name string
		l, r Value
		want bool
	}{
		{"ordinary integral float", MakeInt(1), MakeFloat(1), true},
		{"above float exact integer range", MakeInt(9007199254740993), MakeFloat(9007199254740992), false},
		{"minimum int", MakeInt(math.MinInt64), MakeFloat(-0x1p63), true},
		{"maximum rounds up", MakeInt(math.MaxInt64), MakeFloat(0x1p63), false},
		{"NaN", MakeFloat(math.NaN()), MakeFloat(math.NaN()), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Equal(tc.l, tc.r); got != tc.want {
				t.Fatalf("Equal(%s, %s) = %v, want %v", tc.l.Display(), tc.r.Display(), got, tc.want)
			}
		})
	}
}

func TestCompareNumbersMixedIntFloatIsExact(t *testing.T) {
	cases := []struct {
		l, r Value
		want int
	}{
		{MakeInt(9007199254740993), MakeFloat(9007199254740992), 1},
		{MakeFloat(9007199254740992), MakeInt(9007199254740993), -1},
		{MakeInt(-2), MakeFloat(-1.5), -1},
		{MakeInt(-1), MakeFloat(-1.5), 1},
		{MakeInt(math.MaxInt64), MakeFloat(0x1p63), -1},
		{MakeInt(math.MinInt64), MakeFloat(-0x1p63), 0},
	}
	for _, tc := range cases {
		got, ok := CompareNumbers(tc.l, tc.r)
		if !ok || got != tc.want {
			t.Errorf("CompareNumbers(%s, %s) = (%d, %v), want (%d, true)", tc.l.Display(), tc.r.Display(), got, ok, tc.want)
		}
	}
}

func TestNumericMapKeysMatchEquality(t *testing.T) {
	m := MakeMap().Obj().(*OrderedMap)
	m.Set(MakeInt(9007199254740993), MakeStr("exact"))
	if _, ok := m.Get(MakeFloat(9007199254740992)); ok {
		t.Fatal("distinct mixed numeric values collided as map keys")
	}
	if Hashable(MakeFloat(0x1p63)) {
		t.Fatal("out-of-int64-range integral float must not be hashable")
	}
}

func TestDeepEqualityIsIterativeAndExact(t *testing.T) {
	chain := func(leaf Value) Value {
		v := leaf
		for range maxValueDepth + 100 {
			v = MakeArray([]Value{v})
		}
		return v
	}
	if Equal(chain(MakeInt(1)), chain(MakeInt(2))) {
		t.Fatal("deep arrays with different leaves compared equal")
	}
	if !Equal(chain(MakeInt(1)), chain(MakeFloat(1))) {
		t.Fatal("deep arrays with numerically equal leaves compared unequal")
	}
}

func TestEqualityTerminatesOnCycles(t *testing.T) {
	a := MakeArray(nil)
	b := MakeArray(nil)
	a.Obj().(*Array).Elems = []Value{MakeInt(1), a}
	b.Obj().(*Array).Elems = []Value{MakeFloat(1), b}
	if !Equal(a, b) {
		t.Fatal("equivalent cyclic arrays compared unequal")
	}
	b.Obj().(*Array).Elems[0] = MakeInt(2)
	if Equal(a, b) {
		t.Fatal("different cyclic arrays compared equal")
	}
}
