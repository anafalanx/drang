package value

import "testing"

func TestFreezeMarksContainers(t *testing.T) {
	a := MakeArray([]Value{MakeInt(1)})
	Freeze(a)
	if !a.Obj().(*Array).IsFrozen() {
		t.Error("array not frozen after Freeze")
	}
	m := MakeMap()
	m.Obj().(*OrderedMap).Set(MakeStr("k"), MakeInt(1))
	Freeze(m)
	if !m.Obj().(*OrderedMap).IsFrozen() {
		t.Error("map not frozen after Freeze")
	}
}

func TestFreezeIsDeep(t *testing.T) {
	inner := MakeArray([]Value{MakeInt(1)})
	m := MakeMap()
	m.Obj().(*OrderedMap).Set(MakeStr("k"), inner)
	Freeze(m)
	if !inner.Obj().(*Array).IsFrozen() {
		t.Error("nested array not frozen by a deep Freeze of its container")
	}
}

func TestFreezeIsCycleSafe(t *testing.T) {
	a := MakeArray(nil)
	ao := a.Obj().(*Array)
	ao.Elems = append(ao.Elems, a) // a references itself
	Freeze(a)                      // must terminate (frozen flag is the cycle guard)
	if !ao.IsFrozen() {
		t.Error("self-referential array not frozen")
	}
}

func TestFreezeScalarsAreNoops(t *testing.T) {
	// Must not panic on values with no heap container.
	Freeze(MakeInt(1))
	Freeze(MakeStr("x"))
	Freeze(MakeNil())
}
