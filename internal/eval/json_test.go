package eval

import (
	"testing"

	"github.com/anafalanx/drang/internal/value"
)

func TestJSON(t *testing.T) {
	cases := []struct{ name, src, want string }{
		// to_json: scalars
		{"to-int", `say(to_json(42))`, "42\n"},
		{"to-float", `say(to_json(3.5))`, "3.5\n"},
		{"to-bool", `say(to_json(true))`, "true\n"},
		{"to-nil", `$m := {}; say(to_json($m["z"]))`, "null\n"},
		{"to-str", `say(to_json("hi"))`, "\"hi\"\n"},
		{"to-str-no-html-escape", `say(to_json("<a>&b"))`, "\"<a>&b\"\n"},
		{"to-arr", `say(to_json([1, 2, 3]))`, "[1,2,3]\n"},
		{"to-empty-arr", `say(to_json([]))`, "[]\n"},
		{"to-empty-obj", `say(to_json({}))`, "{}\n"},

		// to_json: maps preserve insertion order; int keys stringify
		{"to-obj-order", `$m := {}; $m["b"] = 1; $m["a"] = 2; say(to_json($m))`, "{\"b\":1,\"a\":2}\n"},
		{"to-nested", `say(to_json({"xs": [1, 2], "ok": true}))`, "{\"xs\":[1,2],\"ok\":true}\n"},
		{"to-int-key", `$m := {}; $m[1] = "x"; say(to_json($m))`, "{\"1\":\"x\"}\n"},

		// to_json: pretty (indent = N spaces)
		{"pretty-arr", `say(to_json([1, 2], 2))`, "[\n  1,\n  2\n]\n"},
		{"pretty-obj", `say(to_json({"a": 1}, 2))`, "{\n  \"a\": 1\n}\n"},

		// from_json: parsing and types
		{"from-int-is-int", `say(from_json("7") % 2)`, "1\n"},
		{"from-float", `say(to_json(from_json("1.5")))`, "1.5\n"},
		{"from-null", `say(from_json("null"))`, "nil\n"},
		{"from-arr", `say(from_json("[1, 2, 3]"))`, "[1, 2, 3]\n"},
		{"from-obj-field", `say(from_json("{\"a\": 1}").a)`, "1\n"},
		{"from-nested", `say(from_json("{\"xs\": [1, 2]}"))`, "{xs: [1, 2]}\n"},

		// round-trip preserves object key order
		{"roundtrip-order", `say(to_json(from_json("{\"b\":1,\"a\":2}")))`, "{\"b\":1,\"a\":2}\n"},

		// errors are catchable values, not aborts
		{"err-malformed", `say(is_err(from_json("{bad")))`, "true\n"},
		{"err-trailing", `say(is_err(from_json("1 2")))`, "true\n"},
		{"err-not-encodable", `say(is_err(to_json(|$x| $x)))`, "true\n"},
		{"ok-not-err", `say(is_err(from_json("[1]")))`, "false\n"},

		// deep nesting is a catchable Err, never a process crash (parse depth cap)
		{"err-deep-nest", `$s := repeat("[", 20000) ~ repeat("]", 20000); say(is_err(from_json($s)))`, "true\n"},

		// integral floats stay floats across a round-trip (rendered as N.0)
		{"integral-float", `say(to_json(7.0))`, "7.0\n"},
		{"integral-float-roundtrip", `say(to_json(from_json(to_json(7.0))))`, "7.0\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := run(t, c.src); got != c.want {
				t.Errorf("%s\n got %q\nwant %q", c.src, got, c.want)
			}
		})
	}
}

// TestJSONEdgeCases covers conditions awkward to express in drang source.
func TestJSONEdgeCases(t *testing.T) {
	// Invalid UTF-8 (from read_file/capture of binary data) must be a catchable
	// Err, not silent replacement-char corruption that breaks the round-trip.
	if v, err := builtinToJSON([]value.Value{value.MakeStr("\xc3\x28")}); err != nil || !v.IsErr() {
		t.Errorf("to_json invalid UTF-8: want Err value, got v=%s err=%v", v.Display(), err)
	}
	// An oversized indent must be rejected (misuse abort), not allowed to balloon memory.
	if _, err := builtinToJSON([]value.Value{
		value.MakeArray([]value.Value{value.MakeInt(1)}),
		value.MakeInt(1_000_000),
	}); err == nil {
		t.Error("to_json oversized indent: want an aborting error")
	}
}
