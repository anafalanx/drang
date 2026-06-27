package value

import "strings"

// Array is a mutable, reference-semantic array. It is held via *Array so that an
// append (which may reallocate the backing slice) is visible through every alias.
// frozen makes it immutable (see value.Freeze): in-place mutators reject a frozen
// array, so it is safe to share read-only across importers/goroutines.
type Array struct {
	Elems  []Value
	frozen bool
}

// MakeArray wraps elems in an array Value.
func MakeArray(elems []Value) Value { return Value{tag: Arr, ref: &Array{Elems: elems}} }

func (a *Array) TypeName() string { return "array" }
func (a *Array) Len() int         { return len(a.Elems) }
func (a *Array) IsFrozen() bool   { return a.frozen }

func (a *Array) Display() string {
	parts := make([]string, len(a.Elems))
	for i, e := range a.Elems {
		parts[i] = e.Display()
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func (a *Array) Equal(o Obj) bool {
	b, ok := o.(*Array)
	if !ok || len(a.Elems) != len(b.Elems) {
		return false
	}
	for i := range a.Elems {
		if !Equal(a.Elems[i], b.Elems[i]) {
			return false
		}
	}
	return true
}

func (a *Array) DeepCopy(visited map[Obj]Obj) Obj {
	if c, ok := visited[a]; ok {
		return c
	}
	cp := &Array{Elems: make([]Value, len(a.Elems))}
	visited[a] = cp
	for i, e := range a.Elems {
		cp.Elems[i] = DeepCopyValue(e, visited)
	}
	return cp
}
