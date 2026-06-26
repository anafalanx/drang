package value

import (
	"math"
	"strconv"
)

// IntRange is an inclusive integer range Lo..Hi. It is ascending; Lo > Hi is
// empty (no auto-reverse). It is iteration-only (not index-readable).
type IntRange struct{ Lo, Hi int64 }

// MakeRange returns a range Value for lo..hi (inclusive).
func MakeRange(lo, hi int64) Value { return Value{tag: Range, ref: &IntRange{Lo: lo, Hi: hi}} }

func (r *IntRange) TypeName() string { return "range" }

func (r *IntRange) Len() int {
	if r.Hi < r.Lo {
		return 0
	}
	// Compute the span in uint64 so the subtraction can't overflow across the
	// sign boundary, then saturate when the element count would exceed an int
	// (a maximal range holds up to 2^64 values, which no int can represent).
	span := uint64(r.Hi) - uint64(r.Lo)
	if span >= uint64(math.MaxInt) {
		return math.MaxInt
	}
	return int(span) + 1
}

func (r *IntRange) Display() string {
	return strconv.FormatInt(r.Lo, 10) + ".." + strconv.FormatInt(r.Hi, 10)
}

func (r *IntRange) Equal(o Obj) bool {
	b, ok := o.(*IntRange)
	return ok && r.Lo == b.Lo && r.Hi == b.Hi
}

// DeepCopy returns the range itself; ranges are immutable value-like objects.
func (r *IntRange) DeepCopy(visited map[Obj]Obj) Obj { return r }
