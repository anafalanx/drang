package value

import "strings"

// mapKey is the normalized, comparable form of a scalar map key.
type mapKey struct {
	tag Tag
	n   int64
	s   string
}

// normalizeKey reduces a key Value to a comparable mapKey. Integral floats
// canonicalize to Int (so $h[1] and $h[1.0] collide, matching ==). Returns
// ok=false for unhashable keys (non-integral float, nil, error, container, fn).
func normalizeKey(k Value) (mapKey, bool) {
	switch k.tag {
	case Int:
		return mapKey{tag: Int, n: k.n}, true
	case Float:
		if k.f == float64(int64(k.f)) {
			return mapKey{tag: Int, n: int64(k.f)}, true
		}
		return mapKey{}, false
	case Str:
		return mapKey{tag: Str, s: k.s}, true
	case Bool:
		return mapKey{tag: Bool, n: k.n}, true
	}
	return mapKey{}, false
}

// Hashable reports whether k may be used as a map key.
func Hashable(k Value) bool {
	_, ok := normalizeKey(k)
	return ok
}

// OrderedMap is an insertion-ordered map with scalar keys. Iteration, keys(),
// values(), and pairs() all read the same ordered key/value slices, so they can
// never diverge (Go's built-in map randomizes range order).
type OrderedMap struct {
	idx  map[mapKey]int
	keys []Value
	vals []Value
}

// MakeMap returns an empty map Value.
func MakeMap() Value { return Value{tag: Map, ref: &OrderedMap{idx: map[mapKey]int{}}} }

func (m *OrderedMap) TypeName() string { return "map" }
func (m *OrderedMap) Len() int         { return len(m.keys) }
func (m *OrderedMap) Keys() []Value    { return m.keys }
func (m *OrderedMap) Vals() []Value    { return m.vals }

func (m *OrderedMap) Get(k Value) (Value, bool) {
	mk, ok := normalizeKey(k)
	if !ok {
		return MakeNil(), false
	}
	if i, present := m.idx[mk]; present {
		return m.vals[i], true
	}
	return MakeNil(), false
}

func (m *OrderedMap) Has(k Value) bool {
	_, ok := m.Get(k)
	return ok
}

// Set inserts or overwrites k->v, preserving insertion order (overwrite keeps
// the original position). No-op for an unhashable key (callers guard first).
func (m *OrderedMap) Set(k, v Value) {
	mk, ok := normalizeKey(k)
	if !ok {
		return
	}
	if m.idx == nil {
		m.idx = map[mapKey]int{}
	}
	if i, present := m.idx[mk]; present {
		m.vals[i] = v
		return
	}
	m.idx[mk] = len(m.keys)
	m.keys = append(m.keys, k)
	m.vals = append(m.vals, v)
}

// Delete removes k, preserving the order of the remaining entries.
func (m *OrderedMap) Delete(k Value) bool {
	mk, ok := normalizeKey(k)
	if !ok {
		return false
	}
	i, present := m.idx[mk]
	if !present {
		return false
	}
	m.keys = append(m.keys[:i], m.keys[i+1:]...)
	m.vals = append(m.vals[:i], m.vals[i+1:]...)
	delete(m.idx, mk)
	for j := i; j < len(m.keys); j++ {
		nk, _ := normalizeKey(m.keys[j])
		m.idx[nk] = j
	}
	return true
}

func (m *OrderedMap) Display() string {
	var b strings.Builder
	b.WriteByte('{')
	for i := range m.keys {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(m.keys[i].Display())
		b.WriteString(": ")
		b.WriteString(m.vals[i].Display())
	}
	b.WriteByte('}')
	return b.String()
}

func (m *OrderedMap) Equal(o Obj) bool {
	n, ok := o.(*OrderedMap)
	if !ok || len(m.keys) != len(n.keys) {
		return false
	}
	for i := range m.keys {
		mk, _ := normalizeKey(m.keys[i])
		j, present := n.idx[mk]
		if !present || !Equal(m.vals[i], n.vals[j]) {
			return false
		}
	}
	return true
}

func (m *OrderedMap) DeepCopy(visited map[Obj]Obj) Obj {
	if c, ok := visited[m]; ok {
		return c
	}
	cp := &OrderedMap{
		idx:  make(map[mapKey]int, len(m.idx)),
		keys: make([]Value, len(m.keys)),
		vals: make([]Value, len(m.vals)),
	}
	visited[m] = cp
	copy(cp.keys, m.keys) // keys are scalars; copy by value is sufficient
	for k, i := range m.idx {
		cp.idx[k] = i
	}
	for i, v := range m.vals {
		cp.vals[i] = DeepCopyValue(v, visited)
	}
	return cp
}
