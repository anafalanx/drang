package value

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Display remains depth-bounded because it recursively renders nested containers.
// Equality is iterative and has no depth cutoff.
const maxValueDepth = 10000

// DisplayWithin renders v exactly like Display while refusing to exceed limit
// bytes. Container traversal is iterative and stops as soon as the next fragment
// would cross the limit, so an oversized early element cannot force traversal or
// allocation of the remainder of a large graph.
func DisplayWithin(v Value, limit int) (string, bool) {
	if limit < 0 {
		return "", false
	}
	type frame struct {
		kind  uint8 // 0 value, 1 array continuation, 2 map continuation
		v     Value
		depth int
		a     *Array
		m     *OrderedMap
		index int
		stage uint8 // map: 0 key, 1 value, 2 advance
	}
	var b strings.Builder
	write := func(s string) bool {
		room := limit - b.Len()
		if len(s) > room {
			// Keep the returned prefix on a decoding boundary. Invalid source
			// bytes count as one width-1 RuneError, matching range/truncate.
			cut := 0
			for cut < len(s) {
				_, width := utf8.DecodeRuneInString(s[cut:])
				if width > room-cut {
					break
				}
				cut += width
			}
			if cut > 0 {
				_, _ = b.WriteString(s[:cut])
			}
			return false
		}
		_, _ = b.WriteString(s)
		return true
	}
	path := make(map[Obj]bool)
	work := []frame{{v: v}}
	for len(work) > 0 {
		f := work[len(work)-1]
		work = work[:len(work)-1]
		switch f.kind {
		case 1:
			if f.index >= len(f.a.Elems) {
				if !write("]") {
					return b.String(), false
				}
				delete(path, f.a)
				continue
			}
			if f.index > 0 && !write(", ") {
				return b.String(), false
			}
			work = append(work,
				frame{kind: 1, depth: f.depth, a: f.a, index: f.index + 1},
				frame{v: f.a.Elems[f.index], depth: f.depth},
			)
			continue
		case 2:
			switch f.stage {
			case 0:
				if f.index >= len(f.m.keys) {
					if !write("}") {
						return b.String(), false
					}
					delete(path, f.m)
					continue
				}
				if f.index > 0 && !write(", ") {
					return b.String(), false
				}
				work = append(work,
					frame{kind: 2, depth: f.depth, m: f.m, index: f.index, stage: 1},
					frame{v: f.m.keys[f.index], depth: f.depth},
				)
			case 1:
				if !write(": ") {
					return b.String(), false
				}
				work = append(work,
					frame{kind: 2, depth: f.depth, m: f.m, index: f.index, stage: 2},
					frame{v: f.m.vals[f.index], depth: f.depth},
				)
			default:
				work = append(work, frame{kind: 2, depth: f.depth, m: f.m, index: f.index + 1})
			}
			continue
		}

		x := f.v
		switch x.tag {
		case Nil:
			if !write("nil") {
				return b.String(), false
			}
		case Bool:
			s := "false"
			if x.n != 0 {
				s = "true"
			}
			if !write(s) {
				return b.String(), false
			}
		case Int:
			if !write(strconv.FormatInt(x.n, 10)) {
				return b.String(), false
			}
		case Float:
			if !write(strconv.FormatFloat(x.f, 'g', -1, 64)) {
				return b.String(), false
			}
		case Str:
			if !write(x.s) {
				return b.String(), false
			}
		case Err:
			if !write("error: ") || !write(x.s) {
				return b.String(), false
			}
		case Arr:
			a, ok := x.ref.(*Array)
			if !ok || a == nil {
				if !write("?") {
					return b.String(), false
				}
				continue
			}
			if path[a] || f.depth >= maxValueDepth {
				if !write("[...]") {
					return b.String(), false
				}
				continue
			}
			if !write("[") {
				return b.String(), false
			}
			path[a] = true
			work = append(work, frame{kind: 1, depth: f.depth + 1, a: a})
		case Map:
			m, ok := x.ref.(*OrderedMap)
			if !ok || m == nil {
				if !write("?") {
					return b.String(), false
				}
				continue
			}
			if path[m] || f.depth >= maxValueDepth {
				if !write("{...}") {
					return b.String(), false
				}
				continue
			}
			if !write("{") {
				return b.String(), false
			}
			path[m] = true
			work = append(work, frame{kind: 2, depth: f.depth + 1, m: m})
		default:
			s := "?"
			if x.ref != nil {
				s = x.ref.Display()
			}
			if !write(s) {
				return b.String(), false
			}
		}
	}
	return b.String(), true
}

func displayDepth(v Value, path map[Obj]bool, depth int) string {
	if v.tag != Arr && v.tag != Map {
		return v.Display()
	}
	if path == nil {
		path = map[Obj]bool{}
	}
	o := v.ref
	if path[o] || depth >= maxValueDepth {
		if v.tag == Arr {
			return "[...]"
		}
		return "{...}"
	}
	path[o] = true
	defer delete(path, o)
	switch om := v.ref.(type) {
	case *Array:
		parts := make([]string, len(om.Elems))
		for i, e := range om.Elems {
			parts[i] = displayDepth(e, path, depth+1)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *OrderedMap:
		var b strings.Builder
		b.WriteByte('{')
		for i := range om.keys {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(displayDepth(om.keys[i], path, depth+1))
			b.WriteString(": ")
			b.WriteString(displayDepth(om.vals[i], path, depth+1))
		}
		b.WriteByte('}')
		return b.String()
	}
	return "?"
}

// equalIterative performs exact structural equality without consuming the Go
// call stack. A visited object-pair memo terminates cycles and makes shared DAGs
// linear in the number of distinct pairs reached.
func equalIterative(l, r Value) bool {
	type pair struct{ l, r Value }
	work := []pair{{l, r}}
	var seen map[[2]Obj]struct{}

	for len(work) > 0 {
		p := work[len(work)-1]
		work = work[:len(work)-1]
		l, r := p.l, p.r
		if l.IsNumber() && r.IsNumber() {
			cmp, ok := CompareNumbers(l, r)
			if !ok || cmp != 0 {
				return false
			}
			continue
		}
		if l.tag != r.tag {
			return false
		}
		switch l.tag {
		case Nil:
			continue
		case Bool:
			if l.n != r.n {
				return false
			}
		case Str:
			if l.s != r.s {
				return false
			}
		case Err:
			if l.s != r.s || l.n != r.n {
				return false
			}
		case Arr, Map, Range, Func, Chan, Task, Proc, Regex, Store:
			if l.ref == nil || r.ref == nil {
				return false
			}
			if l.ref == r.ref {
				continue
			}
			key := [2]Obj{l.ref, r.ref}
			if _, ok := seen[key]; ok {
				continue
			}
			switch lo := l.ref.(type) {
			case *Array:
				ro, ok := r.ref.(*Array)
				if !ok || len(lo.Elems) != len(ro.Elems) {
					return false
				}
				if seen == nil {
					seen = make(map[[2]Obj]struct{})
				}
				seen[key] = struct{}{}
				for i := range lo.Elems {
					work = append(work, pair{lo.Elems[i], ro.Elems[i]})
				}
			case *OrderedMap:
				ro, ok := r.ref.(*OrderedMap)
				if !ok || len(lo.keys) != len(ro.keys) {
					return false
				}
				if seen == nil {
					seen = make(map[[2]Obj]struct{})
				}
				seen[key] = struct{}{}
				for i := range lo.keys {
					mk, ok := normalizeKey(lo.keys[i])
					j, present := ro.idx[mk]
					if !ok || !present {
						return false
					}
					work = append(work, pair{lo.vals[i], ro.vals[j]})
				}
			default:
				if !l.ref.Equal(r.ref) {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}
