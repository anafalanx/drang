package value

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

// Display and Equal route through the depth-bounded, cycle-safe helpers in recurse.go
// so a self-referential or pathologically deep array cannot overflow Go's
// (unrecoverable) stack.
func (a *Array) Display() string { return displayDepth(Value{tag: Arr, ref: a}, nil, 0) }

func (a *Array) Equal(o Obj) bool {
	b, ok := o.(*Array)
	if !ok {
		return false
	}
	return equalDepth(Value{tag: Arr, ref: a}, Value{tag: Arr, ref: b}, 0, nil)
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
