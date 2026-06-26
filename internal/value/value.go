// Package value defines lang3's runtime value: a tagged, unboxed struct
// (no interface{} boxing of scalars, no NaN-boxing), as the design requires.
//
// v0 scalars: nil, bool, int64, float64, string, error. Heap-backed values
// (functions, arrays, maps, ranges) are referenced through the Obj interface so
// that holding packages need not depend on the value package internals.
package value

import "strconv"

// Tag identifies a value's dynamic type.
type Tag uint8

const (
	Nil Tag = iota
	Bool
	Int
	Float
	Str
	Err   // an error value (message + code)
	Func  // heap object: a function
	Arr   // heap object: a mutable array        (type *Array)
	Map   // heap object: an insertion-ordered map (type *OrderedMap)
	Range // heap object: an integer range        (type *IntRange)
	Chan  // heap object: a channel (shared rendezvous)
	Task  // heap object: a spawned task handle
	Proc  // heap object: a started external process handle
	Regex // heap object: a compiled, immutable regex
)

// Obj is a heap-backed value referenced by a Value.
type Obj interface {
	TypeName() string
	Display() string
	Len() int                         // element/entry count (functions report 0)
	Equal(other Obj) bool             // structural equality with a like-kind Obj
	DeepCopy(visited map[Obj]Obj) Obj // copy-on-send hook (cycle-safe via visited)
}

// Value is a single lang3 runtime value, passed by value.
type Value struct {
	tag Tag
	n   int64   // Int payload; Bool as 0/1; Err code
	f   float64 // Float payload
	s   string  // Str payload; Err message
	ref Obj     // payload for heap-backed tags (Func, Arr, Map, Range)
}

func MakeNil() Value { return Value{tag: Nil} }

func MakeBool(b bool) Value {
	v := Value{tag: Bool}
	if b {
		v.n = 1
	}
	return v
}

func MakeInt(n int64) Value                { return Value{tag: Int, n: n} }
func MakeFloat(f float64) Value            { return Value{tag: Float, f: f} }
func MakeStr(s string) Value               { return Value{tag: Str, s: s} }
func MakeErr(msg string, code int64) Value { return Value{tag: Err, s: msg, n: code} }
func MakeObj(tag Tag, o Obj) Value         { return Value{tag: tag, ref: o} }

func (v Value) Tag() Tag         { return v.tag }
func (v Value) AsInt() int64     { return v.n }
func (v Value) AsFloat() float64 { return v.f }
func (v Value) AsStr() string    { return v.s }
func (v Value) AsBool() bool     { return v.n != 0 }
func (v Value) Obj() Obj         { return v.ref }

func (v Value) IsNumber() bool { return v.tag == Int || v.tag == Float }
func (v Value) IsErr() bool    { return v.tag == Err }
func (v Value) ErrMsg() string { return v.s }
func (v Value) ErrCode() int64 { return v.n }

// Num returns an Int or Float value as a float64.
func (v Value) Num() float64 {
	if v.tag == Int {
		return float64(v.n)
	}
	return v.f
}

// TypeName is a human-readable type name for diagnostics.
func (v Value) TypeName() string {
	switch v.tag {
	case Nil:
		return "nil"
	case Bool:
		return "bool"
	case Int:
		return "int"
	case Float:
		return "float"
	case Str:
		return "string"
	case Err:
		return "error"
	}
	if v.ref != nil {
		return v.ref.TypeName()
	}
	return "?"
}

// Display renders the value for output (say): strings appear unquoted.
func (v Value) Display() string {
	switch v.tag {
	case Nil:
		return "nil"
	case Bool:
		if v.n != 0 {
			return "true"
		}
		return "false"
	case Int:
		return strconv.FormatInt(v.n, 10)
	case Float:
		return strconv.FormatFloat(v.f, 'g', -1, 64)
	case Str:
		return v.s
	case Err:
		return "error: " + v.s
	}
	if v.ref != nil {
		return v.ref.Display()
	}
	return "?"
}

// Truthy reports the value's truthiness.
//
// Accepted rule: nil, false, 0, 0.0, "", and EMPTY containers are falsy;
// everything else (non-empty containers, functions, errors) is truthy.
func (v Value) Truthy() bool {
	switch v.tag {
	case Nil:
		return false
	case Bool:
		return v.n != 0
	case Int:
		return v.n != 0
	case Float:
		return v.f != 0
	case Str:
		return v.s != ""
	case Arr, Map, Range:
		if v.ref != nil {
			return v.ref.Len() > 0
		}
		return false
	}
	return true
}

// Equal reports deep/structural equality between two values. Containers compare
// element-/entry-wise; functions compare by identity.
func Equal(l, r Value) bool {
	if l.IsNumber() && r.IsNumber() {
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
		return l.ref != nil && r.ref != nil && l.ref.Equal(r.ref)
	}
	return false
}

// DeepCopyValue returns a deep, cycle-safe copy of v, used at goroutine
// boundaries (copy-on-send). Scalars and strings copy by value; heap objects
// copy via their DeepCopy. Not yet wired — defined so the Obj contract is whole.
func DeepCopyValue(v Value, visited map[Obj]Obj) Value {
	if v.ref == nil {
		return v
	}
	return Value{tag: v.tag, ref: v.ref.DeepCopy(visited)}
}
