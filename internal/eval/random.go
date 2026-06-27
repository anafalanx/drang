package eval

import (
	crand "crypto/rand"
	"fmt"
	"math/rand/v2"

	"github.com/anafalanx/drang/internal/value"
)

// Randomness builtins. rand/rand_int/shuffle/sample use math/rand/v2 (auto-seeded,
// concurrency-safe, fine for jitter/sampling); uuid uses crypto/rand. shuffle and
// sample never mutate their input array.

func builtinRand(args []value.Value) (value.Value, error) {
	if len(args) != 0 {
		return value.MakeNil(), fmt.Errorf("rand expects no arguments, got %d", len(args))
	}
	return value.MakeFloat(rand.Float64()), nil
}

// builtinRandInt: rand_int(n) -> a random int in [0, n); rand_int(lo, hi) -> [lo, hi).
func builtinRandInt(args []value.Value) (value.Value, error) {
	switch len(args) {
	case 1:
		if args[0].Tag() != value.Int {
			return value.MakeErr(fmt.Sprintf("rand_int expects an int, got %s", args[0].TypeName()), 1), nil
		}
		n := args[0].AsInt()
		if n <= 0 {
			return value.MakeErr("rand_int: n must be positive", 1), nil
		}
		return value.MakeInt(rand.Int64N(n)), nil
	case 2:
		if args[0].Tag() != value.Int || args[1].Tag() != value.Int {
			return value.MakeErr("rand_int expects int arguments", 1), nil
		}
		lo, hi := args[0].AsInt(), args[1].AsInt()
		if hi <= lo {
			return value.MakeErr("rand_int: hi must be greater than lo", 1), nil
		}
		// Width can exceed MaxInt64 (e.g. [-1, MaxInt64)); compute it in uint64 and
		// add modularly so wide ranges don't overflow. lo+r is in range, so it fits.
		w := uint64(hi) - uint64(lo)
		return value.MakeInt(int64(uint64(lo) + rand.Uint64N(w))), nil
	default:
		return value.MakeNil(), fmt.Errorf("rand_int expects 1 or 2 arguments, got %d", len(args))
	}
}

func builtinShuffle(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("shuffle expects 1 argument (an array), got %d", len(args))
	}
	if args[0].Tag() != value.Arr {
		return value.MakeErr(fmt.Sprintf("shuffle expects an array, got %s", args[0].TypeName()), 1), nil
	}
	src := args[0].Obj().(*value.Array).Elems
	out := make([]value.Value, len(src))
	copy(out, src)
	rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return value.MakeArray(out), nil
}

func builtinSample(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("sample expects 1 argument (an array), got %d", len(args))
	}
	if args[0].Tag() != value.Arr {
		return value.MakeErr(fmt.Sprintf("sample expects an array, got %s", args[0].TypeName()), 1), nil
	}
	elems := args[0].Obj().(*value.Array).Elems
	if len(elems) == 0 {
		return value.MakeErr("sample: empty array", 1), nil
	}
	return elems[rand.IntN(len(elems))], nil
}

func builtinUUID(args []value.Value) (value.Value, error) {
	if len(args) != 0 {
		return value.MakeNil(), fmt.Errorf("uuid expects no arguments, got %d", len(args))
	}
	var b [16]byte
	if _, err := crand.Read(b[:]); err != nil {
		return value.MakeErr("uuid: "+err.Error(), 1), nil
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10 (RFC 4122)
	return value.MakeStr(fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])), nil
}
