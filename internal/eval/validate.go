package eval

import (
	"fmt"
	"strings"

	"github.com/anafalanx/drang/internal/value"
)

// validate.go — the boundary shape checker. validate(value, shape) returns the value
// unchanged when it matches the shape, or a catchable Err listing EVERY mismatch with
// its path ("child.args[2]: want str, got int 7"). It is a verb over existing values,
// not a type system: shapes are described in maps/arrays/functions, so there is no
// second mini-language and no new value kind.
//
// A shape TERM is one of:
//   - a type token — the first-class conversion builtin str/int/float/bool, matched
//     by exact tag (no coercion, like the rest of the language)
//   - true — matches any value
//   - a map literal — the value must be a map; strict by default (an undeclared key
//     is a mismatch); a "key?" shape key is optional; a "..." shape key holds a term
//     that extra keys' values must match (its presence makes the map open)
//   - an array literal — [term] validates every element; [] is "any array"
//   - any function — a predicate: truthy passes, falsy rejects, a returned Err
//     rejects with that Err's message (see the prelude's one_of and lit)
//
// Absence follows the language's stance: a key that is missing and a key whose value
// is nil are the same thing, for required, optional, and extra keys alike.
//
// A malformed SHAPE (a string where a term belongs, a two-term array shape, a
// non-string shape key) is a programming mistake and ABORTS — uncatchably, like a
// wrong argument count — so a typo'd shape can never be silently absorbed by `//`.

// typeTokens maps the conversion builtins, used first-class as shape terms, to the
// tag they demand. The sigil wall makes these spellings safe forever: a bare `str`
// in value position can only ever be the builtin.
var typeTokens = map[string]value.Tag{
	"str":   value.Str,
	"int":   value.Int,
	"float": value.Float,
	"bool":  value.Bool,
}

func evalValidate(args []value.Value, depth int) (value.Value, error) {
	if len(args) != 2 {
		return value.MakeNil(), fmt.Errorf("validate expects 2 arguments (value, shape), got %d", len(args))
	}
	var miss []string
	if err := validateTerm("", args[0], args[1], depth, &miss); err != nil {
		return value.MakeNil(), err
	}
	if len(miss) == 0 {
		return args[0], nil
	}
	return value.MakeErr("validate: "+strings.Join(miss, "; "), 1), nil
}

// validateTerm checks v against one shape term, appending mismatches to miss. The
// returned error is reserved for aborts: a malformed shape, or a predicate that
// itself failed to run.
func validateTerm(path string, v, term value.Value, depth int, miss *[]string) error {
	switch term.Tag() {
	case value.Func:
		fn, _ := asFunction(term)
		if fn.Builtin != nil {
			if want, ok := typeTokens[fn.Name]; ok {
				if v.Tag() != want {
					*miss = append(*miss, mismatchMsg(path, fn.Name, v))
				}
				return nil
			}
		}
		rv, err := callFunction(fn, []value.Value{v}, depth)
		if err != nil {
			return err
		}
		if rv.IsErr() {
			*miss = append(*miss, atPath(path)+": "+rv.ErrMsg())
			return nil
		}
		if !rv.Truthy() {
			*miss = append(*miss, atPath(path)+": rejected by predicate")
		}
		return nil
	case value.Bool:
		if term.Truthy() {
			return nil // `true` matches any value
		}
		return shapeErr(path, "`false` is not a shape term")
	case value.Map:
		return validateMap(path, v, term.Obj().(*value.OrderedMap), depth, miss)
	case value.Arr:
		return validateArr(path, v, term.Obj().(*value.Array), depth, miss)
	}
	return shapeErr(path, fmt.Sprintf(
		"a %s is not a shape term — a term is str/int/float/bool (bare, first-class), true (any value), a {...} or [...] shape, or a predicate function; for an exact value use lit(...)",
		term.TypeName()))
}

func validateMap(path string, v value.Value, shape *value.OrderedMap, depth int, miss *[]string) error {
	if v.Tag() != value.Map {
		*miss = append(*miss, mismatchMsg(path, "map", v))
		return nil
	}
	om := v.Obj().(*value.OrderedMap)
	var wild value.Value
	hasWild := false
	declared := map[string]bool{}
	sk, sv := shape.Keys(), shape.Vals()
	for i := range sk {
		if sk[i].Tag() != value.Str {
			return shapeErr(path, fmt.Sprintf("shape map keys must be strings, got %s", sk[i].TypeName()))
		}
		key := sk[i].AsStr()
		if key == "..." {
			wild, hasWild = sv[i], true
			continue
		}
		optional := strings.HasSuffix(key, "?")
		base := strings.TrimSuffix(key, "?")
		declared[base] = true
		val, present := om.Get(value.MakeStr(base))
		if !present || val.Tag() == value.Nil { // absent, or present-as-nil (same thing)
			if !optional {
				*miss = append(*miss, joinPath(path, base)+": missing (want "+termName(sv[i])+")")
			}
			continue
		}
		if err := validateTerm(joinPath(path, base), val, sv[i], depth, miss); err != nil {
			return err
		}
	}
	// Undeclared keys: validated against the "..." term when the shape is open,
	// mismatches otherwise. A nil-valued extra counts as absent and is ignored.
	vk, vv := om.Keys(), om.Vals()
	for i := range vk {
		if vk[i].Tag() == value.Str && declared[vk[i].AsStr()] {
			continue
		}
		if vv[i].Tag() == value.Nil {
			continue
		}
		p := joinPath(path, keyDesc(vk[i]))
		if hasWild {
			if err := validateTerm(p, vv[i], wild, depth, miss); err != nil {
				return err
			}
		} else {
			*miss = append(*miss, p+": unexpected key")
		}
	}
	return nil
}

func validateArr(path string, v value.Value, shape *value.Array, depth int, miss *[]string) error {
	if v.Tag() != value.Arr {
		*miss = append(*miss, mismatchMsg(path, "array", v))
		return nil
	}
	switch len(shape.Elems) {
	case 0:
		return nil // [] is "any array"
	case 1:
		for i, el := range v.Obj().(*value.Array).Elems {
			if err := validateTerm(fmt.Sprintf("%s[%d]", path, i), el, shape.Elems[0], depth, miss); err != nil {
				return err
			}
		}
		return nil
	}
	return shapeErr(path, "an array shape holds exactly one element term (for alternatives use one_of([...]))")
}

// --- message helpers -------------------------------------------------------

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// atPath names the site of a mismatch; the whole (root) value reads as "value".
func atPath(path string) string {
	if path == "" {
		return "value"
	}
	return path
}

func mismatchMsg(path, want string, v value.Value) string {
	return atPath(path) + ": want " + want + ", got " + gotDesc(v)
}

// gotDesc shows the offending value's type, plus the value itself for scalars
// (truncated), so a mismatch is diagnosable without a re-run.
func gotDesc(v value.Value) string {
	switch v.Tag() {
	case value.Str:
		s := v.AsStr()
		if len(s) > 30 {
			s = s[:27] + "..."
		}
		return fmt.Sprintf("str %q", s)
	case value.Int, value.Float, value.Bool:
		return v.TypeName() + " " + v.Display()
	}
	return v.TypeName()
}

// termName describes a shape term for the "missing (want ...)" message.
func termName(term value.Value) string {
	switch term.Tag() {
	case value.Func:
		if fn, ok := asFunction(term); ok && fn.Builtin != nil {
			if _, isToken := typeTokens[fn.Name]; isToken {
				return fn.Name
			}
		}
		return "predicate"
	case value.Map:
		return "map shape"
	case value.Arr:
		return "array shape"
	case value.Bool:
		if term.Truthy() {
			return "any value"
		}
	}
	return term.TypeName()
}

// keyDesc renders a data map key for a path segment (string keys bare, others displayed).
func keyDesc(k value.Value) string {
	if k.Tag() == value.Str {
		return k.AsStr()
	}
	return k.Display()
}

func shapeErr(path, msg string) error {
	return fmt.Errorf("validate: invalid shape at %s — %s", atPath(path), msg)
}
