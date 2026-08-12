package value

import "math"

// CompareNumbers compares two numeric Values without converting an int64 operand
// through float64. The bool is false for non-numbers and unordered NaN comparisons.
func CompareNumbers(l, r Value) (int, bool) {
	if !l.IsNumber() || !r.IsNumber() {
		return 0, false
	}
	if l.tag == Int && r.tag == Int {
		return compareInt64(l.n, r.n), true
	}
	if l.tag == Float && r.tag == Float {
		if math.IsNaN(l.f) || math.IsNaN(r.f) {
			return 0, false
		}
		return compareFloat64(l.f, r.f), true
	}
	if l.tag == Int {
		return compareIntFloat(l.n, r.f)
	}
	cmp, ok := compareIntFloat(r.n, l.f)
	return -cmp, ok
}

func compareInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func compareFloat64(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// compareIntFloat compares i with f exactly. Floats at or beyond 2^63 are
// handled before conversion because float64(math.MaxInt64) rounds up to 2^63.
func compareIntFloat(i int64, f float64) (int, bool) {
	if math.IsNaN(f) {
		return 0, false
	}
	if f >= 0x1p63 {
		return -1, true
	}
	if f < -0x1p63 {
		return 1, true
	}
	fi := int64(f) // safe after the bounds checks; truncates toward zero
	if cmp := compareInt64(i, fi); cmp != 0 {
		return cmp, true
	}
	return -compareFloat64(f, float64(fi)), true
}
