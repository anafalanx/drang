package value

import "strings"

// maxValueDepth bounds the recursion in Display and Equal over nested containers.
// Beyond it we emit a "[...]"/"{...}" placeholder (Display) or declare the values
// equal (Equal) rather than overflow Go's stack — which, unlike a builtin panic, is
// an UNRECOVERABLE fatal error (the same reason json.go caps marshaling depth). The
// bound is far above any realistic data structure, so it only ever trips on a cyclic
// or pathologically deep value. Equal also short-circuits on identical references,
// which both speeds up the common self-compare and terminates self-referential cycles.
const maxValueDepth = 10000

// displayDepth renders v. Only Array/OrderedMap recurse; every other value (scalars,
// and non-container objects like functions) renders via its own Display, which does not
// recurse into drang values. A container that is its own ancestor on the current path
// is a cycle and renders as "[...]"/"{...}" — distinguishing a genuine cycle from a
// merely shared sub-value, which is removed from the path on the way back up and so
// still renders in full. depth is a stack backstop for pathologically deep (acyclic)
// data, well below where Go's unrecoverable stack overflow would hit. path is allocated
// lazily on the first container so scalar rendering stays allocation-free.
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

// equalDepth is Equal with a depth bound, an identical-reference fast path, and a
// visited-pair memo. seen records container pairs already visited on this comparison;
// revisiting one returns true, which both breaks reference cycles and — crucially —
// prunes SHARED sub-structure so equality is linear rather than exponential in the
// number of paths to a node. A concrete mismatch still returns false and unwinds the
// whole comparison, so an optimistic revisit can never turn an unequal result equal.
// seen is allocated lazily (only when the first container pair is compared) so scalar
// comparisons stay allocation-free, and it is per-call, so concurrent Equal calls on
// shared frozen values never race.
func equalDepth(l, r Value, depth int, seen map[[2]Obj]bool) bool {
	if l.IsNumber() && r.IsNumber() {
		if l.tag == Int && r.tag == Int {
			return l.n == r.n // exact int64: float64 would collapse values above 2^53
		}
		return l.Num() == r.Num()
	}
	if l.tag != r.tag {
		return false
	}
	switch l.tag {
	case Nil:
		return true
	case Bool:
		return l.n == r.n
	case Str:
		return l.s == r.s
	case Err:
		return l.s == r.s && l.n == r.n
	case Arr, Map, Range, Func, Chan, Task, Proc, Regex:
		if l.ref == nil || r.ref == nil {
			return false
		}
		if l.ref == r.ref {
			return true // same object: equal, and breaks self-referential cycles
		}
		if depth >= maxValueDepth {
			return true // absurdly deep / cyclic: stop rather than overflow the stack
		}
		key := [2]Obj{l.ref, r.ref}
		if seen[key] {
			return true // already visited this pair: cycle, or a shared DAG node — don't re-descend
		}
		switch lo := l.ref.(type) {
		case *Array:
			ro, ok := r.ref.(*Array)
			if !ok || len(lo.Elems) != len(ro.Elems) {
				return false
			}
			if seen == nil {
				seen = map[[2]Obj]bool{}
			}
			seen[key] = true
			for i := range lo.Elems {
				if !equalDepth(lo.Elems[i], ro.Elems[i], depth+1, seen) {
					return false
				}
			}
			return true
		case *OrderedMap:
			ro, ok := r.ref.(*OrderedMap)
			if !ok || len(lo.keys) != len(ro.keys) {
				return false
			}
			if seen == nil {
				seen = map[[2]Obj]bool{}
			}
			seen[key] = true
			for i := range lo.keys {
				mk, _ := normalizeKey(lo.keys[i])
				j, present := ro.idx[mk]
				if !present || !equalDepth(lo.vals[i], ro.vals[j], depth+1, seen) {
					return false
				}
			}
			return true
		default:
			return l.ref.Equal(r.ref) // Range/Func/Chan/Task/Proc/Regex: non-recursive
		}
	}
	return false
}
