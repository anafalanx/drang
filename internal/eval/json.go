package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/anafalanx/drang/internal/value"
)

// JSON support. `from_json(s)` parses a JSON document into drang values and
// `to_json(v [, indent])` renders drang values as JSON. The mapping is:
//
//	object <-> map (insertion order preserved both ways)
//	array  <-> array
//	number ->  int when integral and in int64 range, else float
//	true/false <-> bool, null <-> nil, string <-> string
//
// to_json renders an integral float (7.0) with a trailing ".0" so it stays a float
// across a round-trip, and rejects strings with invalid UTF-8. Consistent with the
// error model, malformed input and non-encodable values are catchable Err values;
// only misuse (wrong arity or argument type) aborts.

const (
	// maxJSONDepth bounds BOTH parse and render recursion, so deeply nested input
	// (or a cyclic structure pushed onto itself) yields a clean Err instead of a
	// stack overflow — Go's fatal stack-overflow is NOT recoverable by safeBuiltin.
	maxJSONDepth = 10000
	// maxJSONIndent caps to_json's indent width, so a giant indent can't exhaust memory.
	maxJSONIndent = 80
)

// builtinFromJSON parses a JSON string into a drang value.
func builtinFromJSON(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("from_json expects 1 argument (string), got %d", len(args))
	}
	if args[0].Tag() != value.Str {
		return value.MakeNil(), fmt.Errorf("from_json expects a string, got %s", args[0].TypeName())
	}
	dec := json.NewDecoder(strings.NewReader(args[0].AsStr()))
	dec.UseNumber() // keep int/float distinction and large-int precision
	v, err := decodeJSON(dec, 0)
	if err != nil {
		return value.MakeErr("from_json: "+err.Error(), 1), nil
	}
	// Exactly one document: reject trailing tokens after the value.
	if _, err := dec.Token(); err != io.EOF {
		return value.MakeErr("from_json: trailing data after JSON value", 1), nil
	}
	return v, nil
}

func decodeJSON(dec *json.Decoder, depth int) (value.Value, error) {
	if depth > maxJSONDepth {
		return value.MakeNil(), fmt.Errorf("nested too deeply (over %d levels)", maxJSONDepth)
	}
	tok, err := dec.Token()
	if err != nil {
		return value.MakeNil(), err
	}
	return decodeJSONValue(dec, tok, depth)
}

func decodeJSONValue(dec *json.Decoder, tok json.Token, depth int) (value.Value, error) {
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			m := value.MakeMap()
			om := m.Obj().(*value.OrderedMap)
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return value.MakeNil(), err
				}
				key, ok := keyTok.(string)
				if !ok {
					return value.MakeNil(), fmt.Errorf("object key is not a string")
				}
				val, err := decodeJSON(dec, depth+1)
				if err != nil {
					return value.MakeNil(), err
				}
				om.Set(value.MakeStr(key), val) // duplicate keys: last value wins, first position kept
			}
			if _, err := dec.Token(); err != nil { // closing '}'
				return value.MakeNil(), err
			}
			return m, nil
		case '[':
			var elems []value.Value
			for dec.More() {
				el, err := decodeJSON(dec, depth+1)
				if err != nil {
					return value.MakeNil(), err
				}
				elems = append(elems, el)
			}
			if _, err := dec.Token(); err != nil { // closing ']'
				return value.MakeNil(), err
			}
			return value.MakeArray(elems), nil
		}
		return value.MakeNil(), fmt.Errorf("unexpected delimiter %q", t)
	case bool:
		return value.MakeBool(t), nil
	case string:
		return value.MakeStr(t), nil
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return value.MakeInt(i), nil
		}
		f, err := t.Float64()
		if err != nil {
			return value.MakeNil(), fmt.Errorf("invalid number %q", t.String())
		}
		return value.MakeFloat(f), nil
	case nil:
		return value.MakeNil(), nil
	}
	return value.MakeNil(), fmt.Errorf("unexpected token")
}

// builtinToJSON renders a drang value as JSON. An optional second argument sets
// indentation: an int N for N spaces per level, or a whitespace string used
// directly; omitted (or 0) yields compact output.
func builtinToJSON(args []value.Value) (value.Value, error) {
	if len(args) < 1 || len(args) > 2 {
		return value.MakeNil(), fmt.Errorf("to_json expects 1 or 2 arguments (value, indent?), got %d", len(args))
	}
	indent := ""
	if len(args) == 2 {
		switch args[1].Tag() {
		case value.Int:
			n := args[1].AsInt()
			if n < 0 || n > maxJSONIndent {
				return value.MakeNil(), fmt.Errorf("to_json indent count must be between 0 and %d, got %d", maxJSONIndent, n)
			}
			indent = strings.Repeat(" ", int(n))
		case value.Str:
			indent = args[1].AsStr()
			if len(indent) > maxJSONIndent {
				return value.MakeNil(), fmt.Errorf("to_json indent string too long (max %d)", maxJSONIndent)
			}
			for _, c := range indent {
				if c != ' ' && c != '\t' {
					return value.MakeNil(), fmt.Errorf("to_json indent string must be spaces or tabs")
				}
			}
		default:
			return value.MakeNil(), fmt.Errorf("to_json indent must be an int or string, got %s", args[1].TypeName())
		}
	}
	var b strings.Builder
	if err := encodeJSON(&b, args[0], indent, 0); err != nil {
		return value.MakeErr("to_json: "+err.Error(), 1), nil
	}
	return value.MakeStr(b.String()), nil
}

func encodeJSON(b *strings.Builder, v value.Value, indent string, depth int) error {
	if depth > maxJSONDepth {
		return fmt.Errorf("value nested too deeply (cycle?)")
	}
	switch v.Tag() {
	case value.Nil:
		b.WriteString("null")
	case value.Bool:
		if v.AsBool() {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case value.Int:
		b.WriteString(strconv.FormatInt(v.AsInt(), 10))
	case value.Float:
		f := v.AsFloat()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return fmt.Errorf("cannot encode %s as JSON", strconv.FormatFloat(f, 'g', -1, 64))
		}
		s := strconv.FormatFloat(f, 'g', -1, 64)
		if !strings.ContainsAny(s, ".eE") {
			s += ".0" // keep an integral float (7.0) a float across a round-trip
		}
		b.WriteString(s)
	case value.Str:
		s := v.AsStr()
		if !utf8.ValidString(s) {
			return fmt.Errorf("cannot encode string with invalid UTF-8 as JSON")
		}
		writeJSONString(b, s)
	case value.Arr:
		return encodeJSONArray(b, v.Obj().(*value.Array), indent, depth)
	case value.Map:
		return encodeJSONMap(b, v.Obj().(*value.OrderedMap), indent, depth)
	default:
		return fmt.Errorf("cannot encode %s as JSON", v.TypeName())
	}
	return nil
}

func encodeJSONArray(b *strings.Builder, a *value.Array, indent string, depth int) error {
	if len(a.Elems) == 0 {
		b.WriteString("[]")
		return nil
	}
	b.WriteByte('[')
	for i, e := range a.Elems {
		if i > 0 {
			b.WriteByte(',')
		}
		writeJSONNewline(b, indent, depth+1)
		if err := encodeJSON(b, e, indent, depth+1); err != nil {
			return err
		}
	}
	writeJSONNewline(b, indent, depth)
	b.WriteByte(']')
	return nil
}

func encodeJSONMap(b *strings.Builder, m *value.OrderedMap, indent string, depth int) error {
	if m.Len() == 0 {
		b.WriteString("{}")
		return nil
	}
	keys, vals := m.Keys(), m.Vals()
	b.WriteByte('{')
	for i := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		writeJSONNewline(b, indent, depth+1)
		writeJSONString(b, keys[i].Display()) // scalar keys stringify (int 1 -> "1")
		b.WriteByte(':')
		if indent != "" {
			b.WriteByte(' ')
		}
		if err := encodeJSON(b, vals[i], indent, depth+1); err != nil {
			return err
		}
	}
	writeJSONNewline(b, indent, depth)
	b.WriteByte('}')
	return nil
}

// writeJSONNewline writes a newline and depth-level indentation when pretty
// (indent != ""); a no-op for compact output.
func writeJSONNewline(b *strings.Builder, indent string, depth int) {
	if indent == "" {
		return
	}
	b.WriteByte('\n')
	for i := 0; i < depth; i++ {
		b.WriteString(indent)
	}
}

// writeJSONString writes s as a quoted, escaped JSON string. Control characters
// are escaped; valid UTF-8 passes through (HTML metacharacters are not escaped,
// unlike encoding/json's default).
func writeJSONString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}
