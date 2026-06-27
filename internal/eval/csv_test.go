package eval

import (
	"testing"

	"github.com/anafalanx/drang/internal/value"
)

func TestCSV(t *testing.T) {
	cases := []struct{ name, src, want string }{
		// --- read ---
		{"read-arrays", `say(to_json(from_csv("a,b\nc,d")))`, `[["a","b"],["c","d"]]` + "\n"},
		{"read-header", `say(to_json(from_csv("name,age\nalice,30", {header: true})))`, `[{"name":"alice","age":"30"}]` + "\n"},
		{"empty", `say(to_json(from_csv("")))`, "[]\n"},
		{"empty-header", `say(to_json(from_csv("", {header: true})))`, "[]\n"},
		{"tsv", `say(to_json(from_csv("a\tb\tc", {sep: "\t"})))`, `[["a","b","c"]]` + "\n"},
		{"comment", `say(to_json(from_csv("a,b\n#x,y\nc,d", {comment: "#"})))`, `[["a","b"],["c","d"]]` + "\n"},
		{"trim", `say(to_json(from_csv("a,   b", {trim: true})))`, `[["a","b"]]` + "\n"},

		// --- the hard part: embedded comma/quote/newline survive a round trip ---
		{"embedded-roundtrip", `say(to_json(from_csv(to_csv([["a,b", "c\"d", "e\nf"]]))))`, `[["a,b","c\"d","e\nf"]]` + "\n"},

		// --- ragged rows: strict by default, opt into lenient ---
		{"ragged-strict-errs", `say(is_err(from_csv("a,b\n1,2,3")))`, "true\n"},
		{"ragged-lenient", `say(to_json(from_csv("a,b\n1,2,3", {lenient: true})))`, `[["a","b"],["1","2","3"]]` + "\n"},

		// --- write ---
		{"write-arrays", `say(to_csv([["a","b"],["1","2"]]) == "a,b\n1,2\n")`, "true\n"},
		{"write-records", `say(to_csv([{"name": "x", "age": 9}]) == "name,age\nx,9\n")`, "true\n"},
		{"cell-types", `say(to_csv([[1, 2.5, true]]) == "1,2.5,true\n")`, "true\n"},
		{"crlf-write", `say(to_csv([["a","b"]], {crlf: true}) == "a,b\r\n")`, "true\n"},
		{"no-header-write", `say(to_csv([{"a": 1}], {header: false}) == "1\n")`, "true\n"},

		// --- error model: bad data is a catchable Err value ---
		{"nonscalar-cell-errs", `say(is_err(to_csv([[ [1,2] ]])))`, "true\n"},
		{"record-missing-field-strict", `say(is_err(to_csv([{"a": 1}, {"b": 2}])))`, "true\n"},
		{"record-lenient-fills", `say(to_csv([{"a": 1, "b": 2}, {"a": 3}], {lenient: true}) == "a,b\n1,2\n3,\n")`, "true\n"},
		{"malformed-is-catchable", `say(is_err(from_csv("a \"b\" c")))`, "true\n"},

		// --- strict guards against silent data loss (from adversarial review) ---
		{"dup-header-errs", `say(is_err(from_csv("a,a\n1,2", {header: true})))`, "true\n"},
		{"dup-header-lenient", `say(to_json(from_csv("a,a\n1,2", {header: true, lenient: true})))`, `[{"a":"2"}]` + "\n"},
		{"ragged-array-write-errs", `say(is_err(to_csv([["a","b"],["c"]])))`, "true\n"},
		{"ragged-array-write-lenient", `say(to_csv([["a","b"],["c"]], {lenient: true}) == "a,b\nc\n")`, "true\n"},
		{"divergent-keys-errs", `say(is_err(to_csv([{"a": 1, "b": 2}, {"c": 3, "d": 4}])))`, "true\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := run(t, c.src); got != c.want {
				t.Errorf("%s\n got %q\nwant %q", c.src, got, c.want)
			}
		})
	}
}

func TestCSVStripsBOM(t *testing.T) {
	// A leading UTF-8 BOM (Excel) must not bleed into the first cell. Build the BOM
	// from raw bytes — a literal U+FEFF in Go source is an illegal byte order mark.
	bom := string([]byte{0xEF, 0xBB, 0xBF})
	v, err := builtinFromCSV([]value.Value{value.MakeStr(bom + "a,b\nc,d")})
	if err != nil {
		t.Fatal(err)
	}
	first := v.Obj().(*value.Array).Elems[0].Obj().(*value.Array).Elems
	if first[0].AsStr() != "a" {
		t.Errorf("BOM not stripped: first cell = %q, want %q", first[0].AsStr(), "a")
	}
}

func TestCSVOptionMisuseAborts(t *testing.T) {
	// Option misuse aborts with a Go error, unlike malformed input (a catchable Err).
	strOpt := func(k, v string) []value.Value {
		opts := value.MakeMap()
		opts.Obj().(*value.OrderedMap).Set(value.MakeStr(k), value.MakeStr(v))
		return []value.Value{value.MakeStr("a,b"), opts}
	}
	cases := []struct {
		name string
		args []value.Value
	}{
		{"multi-rune sep", strOpt("sep", ",,")},
		{"quote sep", strOpt("sep", `"`)},
		{"newline sep", strOpt("sep", "\n")},
		{"unknown option key", strOpt("headerr", "x")},
		{"bool option as string", strOpt("header", "false")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := builtinFromCSV(c.args); err == nil {
				t.Errorf("%s should abort (Go error), not return an Err value", c.name)
			}
		})
	}
	// sep == comment is contradictory configuration: abort.
	o := value.MakeMap()
	om := o.Obj().(*value.OrderedMap)
	om.Set(value.MakeStr("sep"), value.MakeStr("#"))
	om.Set(value.MakeStr("comment"), value.MakeStr("#"))
	if _, err := builtinFromCSV([]value.Value{value.MakeStr("a,b"), o}); err == nil {
		t.Error("sep == comment should abort")
	}
}

func TestCSVNilCell(t *testing.T) {
	// A nil value writes as an empty cell. (drang has no nil literal, so build it.)
	row := value.MakeArray([]value.Value{value.MakeStr("a"), value.MakeNil(), value.MakeStr("c")})
	v, err := builtinToCSV([]value.Value{value.MakeArray([]value.Value{row})})
	if err != nil {
		t.Fatal(err)
	}
	if v.AsStr() != "a,,c\n" {
		t.Errorf("nil cell: got %q, want %q", v.AsStr(), "a,,c\n")
	}
}
