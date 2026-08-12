package eval

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/anafalanx/drang/internal/value"
)

func setResourceStringLimit(t *testing.T, limit int64) {
	t.Helper()
	old := maxStringBytes
	maxStringBytes = limit
	t.Cleanup(func() { maxStringBytes = old })
}

func setResourceCollectionLimit(t *testing.T, limit int) {
	t.Helper()
	old := maxCollectionItems
	maxCollectionItems = limit
	t.Cleanup(func() { maxCollectionItems = old })
}

func requireResourceErr(t *testing.T, name string, got value.Value, err error, contains string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s aborted: %v", name, err)
	}
	if !got.IsErr() {
		t.Fatalf("%s = %s, want a catchable resource Err", name, got.Display())
	}
	if contains != "" && !strings.Contains(got.ErrMsg(), contains) {
		t.Fatalf("%s Err = %q, want it to contain %q", name, got.ErrMsg(), contains)
	}
}

func TestResourceLimitInterpolationBackendParity(t *testing.T) {
	setResourceStringLimit(t, 64)

	// The first two interpolation parts cross the byte ceiling. A later part has
	// an observable side effect: neither backend may evaluate it after overflow,
	// nor may a VM concat fold stringify the intermediate Err back into a string.
	src := fmt.Sprintf(`$seen := 0
fn .bump() { $seen = $seen + 1; "tail" }
$chunk := "1234567890"
$x := $"%s${$chunk}${.bump()}"
say(is_err($x), $seen)
`, strings.Repeat("a", 60))

	walkOut, walkErr := runBackend(t, src, false)
	vmOut, vmErr := runBackend(t, src, true)
	if walkErr != nil || vmErr != nil {
		t.Fatalf("interpolation resource limit aborted: walker=%v VM=%v", walkErr, vmErr)
	}
	if walkOut != vmOut {
		t.Fatalf("interpolation resource-limit parity: walker=%q VM=%q", walkOut, vmOut)
	}
	if want := "true 0\n"; walkOut != want {
		t.Fatalf("interpolation overflow = %q, want %q", walkOut, want)
	}
}

func TestJSONAggregateCollectionBudget(t *testing.T) {
	setResourceCollectionLimit(t, 8)

	exact := `[0,0,0,0,0,0,0,0]`
	got, err := builtinFromJSON([]value.Value{value.MakeStr(exact)})
	if err != nil || got.IsErr() {
		t.Fatalf("from_json at the collection limit = (%s, %v), want success", got.Display(), err)
	}

	over := `[0,0,0,0,0,0,0,0,0]`
	got, err = builtinFromJSON([]value.Value{value.MakeStr(over)})
	requireResourceErr(t, "from_json over item budget", got, err, "collection")

	// Each child is individually below the limit; the aggregate document is not.
	nested := `[[0,0,0,0,0],[0,0,0,0,0]]`
	got, err = builtinFromJSON([]value.Value{value.MakeStr(nested)})
	requireResourceErr(t, "from_json aggregate item budget", got, err, "collection")

	items := make([]value.Value, 8)
	for i := range items {
		items[i] = value.MakeNil()
	}
	got, err = builtinToJSON([]value.Value{value.MakeArray(items)})
	if err != nil || got.IsErr() {
		t.Fatalf("to_json at the collection limit = (%s, %v), want success", got.Display(), err)
	}

	child := func() value.Value {
		elems := make([]value.Value, 5)
		for i := range elems {
			elems[i] = value.MakeNil()
		}
		return value.MakeArray(elems)
	}
	got, err = builtinToJSON([]value.Value{value.MakeArray([]value.Value{child(), child()})})
	requireResourceErr(t, "to_json aggregate item budget", got, err, "collection")
}

func TestPathJoinHonorsStringBudget(t *testing.T) {
	setResourceStringLimit(t, 8)

	got, err := builtinPathJoin([]value.Value{value.MakeStr("abc"), value.MakeStr("defg")})
	if err != nil || got.IsErr() || got.Tag() != value.Str || len(got.AsStr()) != 8 {
		t.Fatalf("path_join at byte limit = (%s, %v), want an 8-byte path", got.Display(), err)
	}
	got, err = builtinPathJoin([]value.Value{value.MakeStr("abcdefgh"), value.MakeStr("")})
	if err != nil || got.IsErr() || got.Tag() != value.Str || got.AsStr() != "abcdefgh" {
		t.Fatalf("path_join trailing empty at byte limit = (%s, %v), want %q", got.Display(), err, "abcdefgh")
	}

	got, err = builtinPathJoin([]value.Value{value.MakeStr("abcd"), value.MakeStr("efgh")})
	requireResourceErr(t, "path_join over byte budget", got, err, "string limit")
}

func TestFromBase64UsesDecodedSizeAtBoundary(t *testing.T) {
	setResourceStringLimit(t, 1)

	// DecodedLen is an upper bound that includes bytes removed by '=' padding;
	// the resource decision must use the actual decoded size.
	got, err := builtinFromBase64([]value.Value{value.MakeStr("YQ==")})
	if err != nil || got.IsErr() || got.Tag() != value.Str || got.AsStr() != "a" {
		t.Fatalf("from_base64 one byte at limit = (%s, %v), want %q", got.Display(), err, "a")
	}

	got, err = builtinFromBase64([]value.Value{value.MakeStr("YWI=")})
	requireResourceErr(t, "from_base64 over decoded-size budget", got, err, "string limit")
}

func TestReplacementBudgetsUseActualOutputSize(t *testing.T) {
	setResourceStringLimit(t, 4)

	t.Run("shrinking literal", func(t *testing.T) {
		got, err := builtinReplaceAll([]value.Value{
			value.MakeStr("abcdef"), value.MakeStr("cd"), value.MakeStr(""),
		})
		if err != nil || got.IsErr() || got.Tag() != value.Str || got.AsStr() != "abef" {
			t.Fatalf("shrinking literal replace_all = (%s, %v), want %q", got.Display(), err, "abef")
		}
	})

	t.Run("identity backreference", func(t *testing.T) {
		pattern := makeRegex("(a)")
		if pattern.IsErr() {
			t.Fatalf("compile replacement test pattern: %s", pattern.Display())
		}
		got, err := builtinReplaceAll([]value.Value{
			value.MakeStr("aaaa"), pattern, value.MakeStr("$1"),
		})
		if err != nil || got.IsErr() || got.Tag() != value.Str || got.AsStr() != "aaaa" {
			t.Fatalf("identity backreference replace_all = (%s, %v), want %q", got.Display(), err, "aaaa")
		}
	})

	t.Run("duplicate named backreference", func(t *testing.T) {
		pattern := makeRegex(`(?P<x>a)|(?P<x>b)`)
		if pattern.IsErr() {
			t.Fatalf("compile duplicate-name replacement pattern: %s", pattern.Display())
		}
		got, err := builtinReplaceAll([]value.Value{
			value.MakeStr("a"), pattern, value.MakeStr("$x"),
		})
		if err != nil || got.IsErr() || got.Tag() != value.Str || got.AsStr() != "a" {
			t.Fatalf("duplicate named backreference replace_all = (%s, %v), want %q", got.Display(), err, "a")
		}
	})
}

func TestRegexReplacementBudgetsTemplateWork(t *testing.T) {
	setResourceCollectionLimit(t, 64)
	setResourceStringLimit(t, 1024)

	pattern := makeRegex("a")
	if pattern.IsErr() {
		t.Fatalf("compile replacement-work pattern: %s", pattern.Display())
	}
	repl := value.MakeStr(strings.Repeat("$missing", 4))

	// Four nonexistent references append nothing. Twelve matches are exactly
	// within the preparse + match + token-operation budget.
	got, err := builtinReplaceAll([]value.Value{value.MakeStr(strings.Repeat("a", 12)), pattern, repl})
	if err != nil || got.IsErr() || got.Tag() != value.Str || got.AsStr() != "" {
		t.Fatalf("regex replacement at work budget = (%s, %v), want empty string", got.Display(), err)
	}

	// One more match would still produce a tiny (empty) result, but previously
	// reparsed all four references for every match without charging that work.
	got, err = builtinReplaceAll([]value.Value{value.MakeStr(strings.Repeat("a", 13)), pattern, repl})
	if err != nil || !got.IsErr() || got.ErrCode() != 137 || !strings.Contains(got.ErrMsg(), "work exceeds") {
		t.Fatalf("regex replacement over work budget = (%s, %v), want catchable Err code 137", got.Display(), err)
	}
}

func TestRegexPatternDiagnosticsAreBounded(t *testing.T) {
	t.Run("ordinary message stays exact", func(t *testing.T) {
		const pattern = "("
		got := makeRegex(pattern)
		if !got.IsErr() {
			t.Fatalf("invalid pattern compiled: %s", got.Display())
		}
		wantPrefix := "bad regex " + strconv.Quote(pattern) + ":"
		if !strings.HasPrefix(got.ErrMsg(), wantPrefix) {
			t.Fatalf("ordinary regex diagnostic = %q, want prefix %q", got.ErrMsg(), wantPrefix)
		}

		_, errv, ok := regexArg("matches", value.MakeStr(pattern))
		if ok || !errv.IsErr() {
			t.Fatalf("regexArg accepted invalid pattern: (%s, %v)", errv.Display(), ok)
		}
		wantPrefix = "matches: bad pattern " + strconv.Quote(pattern) + ":"
		if !strings.HasPrefix(errv.ErrMsg(), wantPrefix) {
			t.Fatalf("ordinary regexArg diagnostic = %q, want prefix %q", errv.ErrMsg(), wantPrefix)
		}
	})

	t.Run("low quote limit", func(t *testing.T) {
		got := boundedRegexPatternQuote(strings.Repeat("\x00", 32), 16)
		if len(got) > 16 || !strings.HasSuffix(got, "...") {
			t.Fatalf("bounded control-byte quote = %q (%d bytes), want marked result within 16 bytes", got, len(got))
		}
		if !strings.HasPrefix(got, `"\x00`) {
			t.Fatalf("bounded control-byte quote lost its quoted prefix: %q", got)
		}
	})

	t.Run("oversized rejected pattern", func(t *testing.T) {
		pattern := strings.Repeat("\x00", maxRegexPatternBytes+1)
		got := makeRegex(pattern)
		if !got.IsErr() || !strings.Contains(got.ErrMsg(), "pattern exceeds") {
			t.Fatalf("oversized regex = %s, want pattern-limit Err", got.Display())
		}
		if len(got.ErrMsg()) > int(maxRegexDiagnosticBytes)+128 || !strings.Contains(got.ErrMsg(), "...") {
			t.Fatalf("oversized regex diagnostic is not bounded: %d bytes", len(got.ErrMsg()))
		}

		_, errv, ok := regexArg("matches", value.MakeStr(pattern))
		if ok || !errv.IsErr() || !strings.Contains(errv.ErrMsg(), "pattern exceeds") {
			t.Fatalf("oversized regexArg = (%s, %v), want pattern-limit Err", errv.Display(), ok)
		}
		if len(errv.ErrMsg()) > int(maxRegexDiagnosticBytes)+128 || !strings.Contains(errv.ErrMsg(), "...") {
			t.Fatalf("oversized regexArg diagnostic is not bounded: %d bytes", len(errv.ErrMsg()))
		}
	})
}

func TestToCSVHeaderCountsAgainstCellBudget(t *testing.T) {
	setResourceCollectionLimit(t, 4)

	record := value.MakeMap()
	rm := record.Obj().(*value.OrderedMap)
	rm.Set(value.MakeStr("a"), value.MakeInt(1))
	rm.Set(value.MakeStr("b"), value.MakeInt(2))
	rm.Set(value.MakeStr("c"), value.MakeInt(3))
	rows := value.MakeArray([]value.Value{record})

	got, err := builtinToCSV([]value.Value{rows})
	requireResourceErr(t, "to_csv header cell budget", got, err, "cell")

	opts := value.MakeMap()
	opts.Obj().(*value.OrderedMap).Set(value.MakeStr("header"), value.MakeBool(false))
	got, err = builtinToCSV([]value.Value{rows, opts})
	if err != nil || got.IsErr() {
		t.Fatalf("to_csv without header within budget = (%s, %v), want success", got.Display(), err)
	}

	exactRecord := value.MakeMap()
	em := exactRecord.Obj().(*value.OrderedMap)
	em.Set(value.MakeStr("a"), value.MakeInt(1))
	em.Set(value.MakeStr("b"), value.MakeInt(2))
	got, err = builtinToCSV([]value.Value{value.MakeArray([]value.Value{exactRecord})})
	if err != nil || got.IsErr() {
		t.Fatalf("to_csv header exactly at budget = (%s, %v), want success", got.Display(), err)
	}
}

func TestToCSVSparseLenientRecordsCountPaddedCells(t *testing.T) {
	setResourceCollectionLimit(t, 4)

	first := value.MakeMap()
	fm := first.Obj().(*value.OrderedMap)
	fm.Set(value.MakeStr("a"), value.MakeInt(1))
	fm.Set(value.MakeStr("b"), value.MakeInt(2))
	empty := value.MakeMap()
	opts := value.MakeMap()
	om := opts.Obj().(*value.OrderedMap)
	om.Set(value.MakeStr("header"), value.MakeBool(false))
	om.Set(value.MakeStr("lenient"), value.MakeBool(true))

	got, err := builtinToCSV([]value.Value{value.MakeArray([]value.Value{first, empty}), opts})
	if err != nil || got.IsErr() {
		t.Fatalf("sparse lenient records exactly at padded-cell budget = (%s, %v), want success", got.Display(), err)
	}

	got, err = builtinToCSV([]value.Value{value.MakeArray([]value.Value{first, empty, value.MakeMap()}), opts})
	requireResourceErr(t, "sparse lenient padded-cell budget", got, err, "cell")
}

func TestFromCSVQuotedSeparatorsCountAsOneCell(t *testing.T) {
	setResourceCollectionLimit(t, 4)

	const cell = "a,b,c,d,e,f"
	got, err := builtinFromCSV([]value.Value{value.MakeStr(`"` + cell + `"` + "\n")})
	if err != nil || got.IsErr() {
		t.Fatalf("from_csv one quoted cell = (%s, %v), want success", got.Display(), err)
	}
	if got.Tag() != value.Arr {
		t.Fatalf("from_csv one quoted cell returned %s, want array", got.TypeName())
	}
	rows := got.Obj().(*value.Array).Elems
	if len(rows) != 1 || rows[0].Tag() != value.Arr {
		t.Fatalf("from_csv one quoted cell shape = %s", got.Display())
	}
	fields := rows[0].Obj().(*value.Array).Elems
	if len(fields) != 1 || fields[0].Tag() != value.Str || fields[0].AsStr() != cell {
		t.Fatalf("from_csv one quoted cell fields = %s", rows[0].Display())
	}
}

func TestParseArgsHonorsResultCollectionBudgets(t *testing.T) {
	setResourceCollectionLimit(t, 4)

	argv := func(ss ...string) value.Value {
		elems := make([]value.Value, len(ss))
		for i, s := range ss {
			elems[i] = value.MakeStr(s)
		}
		return value.MakeArray(elems)
	}

	t.Run("exact", func(t *testing.T) {
		got, err := builtinParseArgs([]value.Value{argv("--a", "--b", "--c")})
		if err != nil || got.IsErr() {
			t.Fatalf("parse_args exactly at map budget = (%s, %v), want success", got.Display(), err)
		}
	})
	t.Run("option map", func(t *testing.T) {
		got, err := builtinParseArgs([]value.Value{argv("--a", "--b", "--c", "--d")})
		requireResourceErr(t, "parse_args option-map budget", got, err, "collection")
	})
	t.Run("positionals", func(t *testing.T) {
		got, err := builtinParseArgs([]value.Value{argv("a", "b", "c", "d", "e")})
		requireResourceErr(t, "parse_args positional budget", got, err, "collection")
	})
}

// resourceDisplayProbe makes eager Display traversal observable without a large
// allocation. Native value Display methods are pure, but a trailing probe tells
// us whether a capped renderer needlessly walked beyond an already-over-limit
// prefix before returning its Err.
type resourceDisplayProbe struct{ calls *int }

func (p *resourceDisplayProbe) TypeName() string { return "display-probe" }
func (p *resourceDisplayProbe) Display() string {
	(*p.calls)++
	return "probe"
}
func (p *resourceDisplayProbe) Len() int { return 0 }
func (p *resourceDisplayProbe) Equal(other value.Obj) bool {
	return p == other
}
func (p *resourceDisplayProbe) DeepCopy(map[value.Obj]value.Obj) value.Obj { return p }

func makeResourceNested(calls *int) value.Value {
	probe := value.MakeObj(value.Func, &resourceDisplayProbe{calls: calls})
	return value.MakeArray([]value.Value{value.MakeStr("0123456789"), probe})
}

func TestCappedRenderingStopsBeforeTrailingNestedValues(t *testing.T) {
	setResourceStringLimit(t, 8)

	t.Run("concat", func(t *testing.T) {
		calls := 0
		got := concatValues(makeResourceNested(&calls), value.MakeStr(""))
		if !got.IsErr() {
			t.Fatalf("over-limit nested concat = %s, want Err", got.Display())
		}
		if calls != 0 {
			t.Fatalf("over-limit nested concat rendered %d trailing value(s), want 0", calls)
		}
	})

	t.Run("join element", func(t *testing.T) {
		calls := 0
		got, err := joinStrings([]value.Value{
			value.MakeArray([]value.Value{makeResourceNested(&calls)}),
		})
		if err != nil {
			t.Fatalf("over-limit nested join aborted: %v", err)
		}
		if !got.IsErr() {
			t.Fatalf("over-limit nested join = %s, want Err", got.Display())
		}
		if calls != 0 {
			t.Fatalf("over-limit nested join rendered %d trailing value(s), want 0", calls)
		}
	})

	t.Run("unused join separator", func(t *testing.T) {
		calls := 0
		got, err := joinStrings([]value.Value{
			value.MakeArray(nil),
			value.MakeObj(value.Func, &resourceDisplayProbe{calls: &calls}),
		})
		if err != nil || got.IsErr() || got.Tag() != value.Str || got.AsStr() != "" {
			t.Fatalf("empty join with unused separator = (%s, %v), want empty string", got.Display(), err)
		}
		if calls != 0 {
			t.Fatalf("empty join rendered its unused separator %d time(s), want 0", calls)
		}
	})

	t.Run("str", func(t *testing.T) {
		calls := 0
		got, err := builtinStr([]value.Value{makeResourceNested(&calls)})
		if err != nil {
			t.Fatalf("over-limit nested str aborted: %v", err)
		}
		if !got.IsErr() {
			t.Fatalf("over-limit nested str = %s, want Err", got.Display())
		}
		if calls != 0 {
			t.Fatalf("over-limit nested str rendered %d trailing value(s), want 0", calls)
		}
	})

	t.Run("format", func(t *testing.T) {
		calls := 0
		got, err := builtinFormat([]value.Value{value.MakeStr("{}"), makeResourceNested(&calls)})
		if err != nil {
			t.Fatalf("over-limit nested format aborted: %v", err)
		}
		if !got.IsErr() {
			t.Fatalf("over-limit nested format = %s, want Err", got.Display())
		}
		if calls != 0 {
			t.Fatalf("over-limit nested format rendered %d trailing value(s), want 0", calls)
		}
	})

	t.Run("format precision takes a prefix", func(t *testing.T) {
		got, err := builtinFormat([]value.Value{
			value.MakeStr("{:.1s}"), value.MakeStr("abcdefghijklmnop"),
		})
		if err != nil || got.IsErr() || got.Tag() != value.Str || got.AsStr() != "a" {
			t.Fatalf("format precision prefix = (%s, %v), want %q", got.Display(), err, "a")
		}
	})

	t.Run("format precision enters a nested element", func(t *testing.T) {
		calls := 0
		got, err := builtinFormat([]value.Value{
			value.MakeStr("{:.5s}"),
			value.MakeArray([]value.Value{
				value.MakeStr("abcdefghijklmnop"),
				value.MakeObj(value.Func, &resourceDisplayProbe{calls: &calls}),
			}),
		})
		if err != nil || got.IsErr() || got.Tag() != value.Str || got.AsStr() != "[abcd" {
			t.Fatalf("nested format precision prefix = (%s, %v), want %q", got.Display(), err, "[abcd")
		}
		if calls != 0 {
			t.Fatalf("nested precision format rendered %d trailing value(s), want 0", calls)
		}
	})

	t.Run("UTF-8 rune is not split at byte ceiling", func(t *testing.T) {
		setResourceStringLimit(t, 3)
		got, err := builtinStr([]value.Value{value.MakeStr("😀")})
		requireResourceErr(t, "str four-byte rune over three-byte budget", got, err, "string limit")
	})
}

func TestMaterializationSinksHonorStringBudget(t *testing.T) {
	setResourceStringLimit(t, 8)

	assertStopped := func(t *testing.T, calls int) {
		t.Helper()
		if calls != 0 {
			t.Fatalf("rendered %d trailing value(s), want 0", calls)
		}
	}

	t.Run("write_file", func(t *testing.T) {
		calls := 0
		path := filepath.Join(t.TempDir(), "out.txt")
		got, err := builtinWriteFile([]value.Value{value.MakeStr(path), makeResourceNested(&calls)})
		requireResourceErr(t, "write_file content", got, err, "string limit")
		assertStopped(t, calls)
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("write_file overflow touched %q: stat error = %v", path, statErr)
		}

		got, err = builtinWriteFile([]value.Value{value.MakeStr(path), value.MakeStr("abcdefgh")})
		if err != nil || got.IsErr() {
			t.Fatalf("write_file at limit = (%s, %v), want success", got.Display(), err)
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil || string(data) != "abcdefgh" {
			t.Fatalf("write_file at limit wrote %q, %v", data, readErr)
		}
	})

	t.Run("send_stdin", func(t *testing.T) {
		calls := 0
		f, err := os.CreateTemp(t.TempDir(), "stdin-*")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		proc := value.MakeObj(value.Proc, &Proc{stdinW: f})
		got, callErr := builtinSendStdin([]value.Value{proc, makeResourceNested(&calls)})
		requireResourceErr(t, "send_stdin content", got, callErr, "string limit")
		assertStopped(t, calls)
		info, statErr := f.Stat()
		if statErr != nil {
			t.Fatalf("stat send_stdin target after overflow: %v", statErr)
		}
		if info.Size() != 0 {
			t.Fatalf("send_stdin overflow wrote %d bytes", info.Size())
		}

		got, callErr = builtinSendStdin([]value.Value{proc, value.MakeStr("abcdefgh")})
		if callErr != nil || got.IsErr() {
			t.Fatalf("send_stdin at limit = (%s, %v), want success", got.Display(), callErr)
		}
		info, statErr = f.Stat()
		if statErr != nil {
			t.Fatalf("stat send_stdin target at limit: %v", statErr)
		}
		if info.Size() != 8 {
			t.Fatalf("send_stdin at limit wrote %d bytes", info.Size())
		}
	})

	t.Run("stream output", func(t *testing.T) {
		calls := 0
		if _, err := streamText(makeResourceNested(&calls)); err == nil || !strings.Contains(err.Error(), "string limit") {
			t.Fatalf("streamText overflow error = %v, want string-limit error", err)
		}
		assertStopped(t, calls)
		if got, err := streamText(value.MakeStr("abcdefgh")); err != nil || got != "abcdefgh" {
			t.Fatalf("streamText at limit = (%q, %v)", got, err)
		}
	})

	t.Run("fail", func(t *testing.T) {
		calls := 0
		got, err := builtinFail([]value.Value{makeResourceNested(&calls)})
		requireResourceErr(t, "fail message", got, err, "string limit")
		assertStopped(t, calls)
		got, err = builtinFail([]value.Value{value.MakeStr("abcdefgh")})
		if err != nil || !got.IsErr() || got.ErrMsg() != "abcdefgh" {
			t.Fatalf("fail at limit = (%s, %v), want original Err message", got.Display(), err)
		}
	})

	t.Run("die", func(t *testing.T) {
		calls := 0
		_, err := builtinDie([]value.Value{makeResourceNested(&calls)})
		if err == nil || strings.Contains(err.Error(), "exit requested") || !strings.Contains(err.Error(), "string limit") {
			t.Fatalf("die overflow error = %v, want rendering error before exit", err)
		}
		if _, exits := ExitRequested(err); exits {
			t.Fatalf("die overflow requested exit instead of reporting its rendering error: %v", err)
		}
		assertStopped(t, calls)
	})

	t.Run("dispatch argv", func(t *testing.T) {
		calls := 0
		env := NewEnv()
		if err := env.define("ARGV", value.MakeArray([]value.Value{makeResourceNested(&calls)}), false); err != nil {
			t.Fatal(err)
		}
		if _, err := dispatchArgs(env); err == nil || !strings.Contains(err.Error(), "string limit") {
			t.Fatalf("dispatchArgs overflow error = %v, want string-limit error", err)
		}
		assertStopped(t, calls)

		env = NewEnv()
		if err := env.define("ARGV", value.MakeArray([]value.Value{value.MakeStr("abcd"), value.MakeStr("efgh")}), false); err != nil {
			t.Fatal(err)
		}
		argv, err := dispatchArgs(env)
		if err != nil || len(argv) != 2 || argv[0] != "abcd" || argv[1] != "efgh" {
			t.Fatalf("dispatchArgs at limit = (%q, %v)", argv, err)
		}
	})

	t.Run("dispatch task list", func(t *testing.T) {
		tasks := value.MakeMap().Obj().(*value.OrderedMap)
		tasks.Set(value.MakeStr("x"), value.MakeNil())
		var out bytes.Buffer
		if err := listTasks(&out, tasks); err == nil || !strings.Contains(err.Error(), "string limit") {
			t.Fatalf("listTasks overflow error = %v, want string-limit error", err)
		}
		if out.Len() != 0 {
			t.Fatalf("listTasks wrote partial output %q before rejecting it", out.String())
		}
	})

	t.Run("exec arguments", func(t *testing.T) {
		argv, err := execArgStrings("run", []value.Value{value.MakeStr("abcd"), value.MakeStr("efgh")})
		if err != nil || len(argv) != 2 {
			t.Fatalf("exec arguments at limit = (%q, %v)", argv, err)
		}
		if _, err := execArgStrings("run", []value.Value{value.MakeStr("abcde"), value.MakeStr("efgh")}); err == nil || !strings.Contains(err.Error(), "string limit") {
			t.Fatalf("exec arguments over limit error = %v", err)
		}
	})

	t.Run("environment", func(t *testing.T) {
		calls := 0
		overlay := value.MakeMap().Obj().(*value.OrderedMap)
		overlay.Set(value.MakeStr("K"), makeResourceNested(&calls))
		if _, err := buildEnv(overlay, false); err == nil || !strings.Contains(err.Error(), "string limit") {
			t.Fatalf("environment overflow error = %v, want string-limit error", err)
		}
		assertStopped(t, calls)

		overlay = value.MakeMap().Obj().(*value.OrderedMap)
		overlay.Set(value.MakeStr("K"), value.MakeStr("123456"))
		env, err := buildEnv(overlay, false)
		if err != nil || len(env) != 1 || env[0] != "K=123456" {
			t.Fatalf("environment at limit = (%q, %v)", env, err)
		}
	})

	t.Run("REPL display API", func(t *testing.T) {
		calls := 0
		if _, err := DisplayValue(makeResourceNested(&calls)); err == nil || !strings.Contains(err.Error(), "string limit") {
			t.Fatalf("DisplayValue overflow error = %v, want string-limit error", err)
		}
		assertStopped(t, calls)
		if got, err := DisplayValue(value.MakeStr("abcdefgh")); err != nil || got != "abcdefgh" {
			t.Fatalf("DisplayValue at limit = (%q, %v)", got, err)
		}
	})

	t.Run("test diagnostic", func(t *testing.T) {
		calls := 0
		got := describe(makeResourceNested(&calls))
		assertStopped(t, calls)
		if len(got) > int(maxStringBytes) || !strings.HasSuffix(got, "...") {
			t.Fatalf("bounded diagnostic = %q (%d bytes), want marked result within %d bytes", got, len(got), maxStringBytes)
		}
		input := "a\\n😀\xff"
		got, ok := quotedWithin(input, 64)
		if !ok || got != strconv.Quote(input) {
			t.Fatalf("quotedWithin = (%q, %v), want %q", got, ok, strconv.Quote(input))
		}
	})
}
