package eval

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/anafalanx/drang/internal/value"
)

func TestCheckedFloatToIntConversionBothBackends(t *testing.T) {
	assertBoth(t, `say(int(12.75))`, "12\n")
	assertBoth(t, `say(is_err(int(9223372036854775808.0)))`, "true\n")
	assertBoth(t, `say(is_err(int(exp(1000))))`, "true\n")
}

func TestMinIntNegationOverflowsBothBackends(t *testing.T) {
	src := `$n := 0 - 9223372036854775807 - 1; say(-$n)`
	for _, vm := range []bool{false, true} {
		_, err := runBackend(t, src, vm)
		if err == nil || !strings.Contains(err.Error(), "overflow") {
			t.Errorf("vm=%v: got error %v, want integer overflow", vm, err)
		}
	}
}

func TestMixedIntFloatComparisonIsExactBothBackends(t *testing.T) {
	assertBoth(t, `say(9007199254740993 == 9007199254740992.0)`, "false\n")
	assertBoth(t, `say(9007199254740993 > 9007199254740992.0)`, "true\n")
	assertBoth(t, `say(9007199254740993 <=> 9007199254740992.0)`, "1\n")
	assertBoth(t, `say(min(9007199254740993, 9007199254740992.0))`, "9.007199254740992e+15\n")
}

func TestTimeRoundTripAndValidationBothBackends(t *testing.T) {
	assertBoth(t, `$e := parse_time("2500-01-01 00:00:00", "%Y-%m-%d %H:%M:%S", {utc: true}); say(format_time($e, "%Y-%m-%d %H:%M:%S", {utc: true}))`, "2500-01-01 00:00:00\n")
	assertBoth(t, `$e := parse_time("0001-01-01", "%Y-%m-%d", {utc: true}); say(format_time($e, "%Y-%m-%d", {utc: true}))`, "0001-01-01\n")
	assertBoth(t, `$e := parse_time("2026-01-01 00:00:00.999999999", "%Y-%m-%d %H:%M:%S.999999999", {utc: true}); say(format_time($e, "%Y-%m-%d %H:%M:%S", {utc: true}))`, "2026-01-01 00:00:00\n")
	assertBoth(t, `$e := parse_time("1969-12-31 23:59:59.999999999", "%Y-%m-%d %H:%M:%S.999999999", {utc: true}); say(format_time($e, "%Y-%m-%d %H:%M:%S", {utc: true}))`, "1969-12-31 23:59:59\n")
	assertBoth(t, `$e := parse_time("9999-12-31 23:59:59.999999999", "%Y-%m-%d %H:%M:%S.999999999", {utc: true}); say(format_time($e, "%Y-%m-%d %H:%M:%S", {utc: true}))`, "9999-12-31 23:59:59\n")
	assertBoth(t, `say(is_err(format_time(exp(1000), "%Y", {utc: true})))`, "true\n")
	assertBoth(t, `say(is_err(date_parts(exp(1000))))`, "true\n")
	// Go's time.Unix accepts MinInt64 seconds, but calendar conversion wraps it
	// into a huge positive year. Reject it rather than returning the wrong date.
	assertBoth(t, `say(is_err(format_time(-9223372036854775808.0, "%Y", {utc: true})))`, "true\n")
	assertBoth(t, `say(is_err(date_parts(-9223372036854775808.0, {utc: true})))`, "true\n")
	assertBoth(t, `say(is_err(sleep(0 - 1)))`, "true\n")
	assertBoth(t, `say(is_err(sleep(exp(1000))))`, "true\n")
	assertBoth(t, `say(is_err(format_time(0, "%Y", {utc: "true"})))`, "true\n")
}

func TestEpochSecondsPreservesRepresentableValuesAndSecondBoundary(t *testing.T) {
	exact := []struct {
		name string
		t    time.Time
		want float64
	}{
		{"whole positive", time.Unix(123, 0).UTC(), 123},
		{"fraction positive", time.Unix(123, 125_000_000).UTC(), 123.125},
		{"fraction negative", time.Unix(-1, 500_000_000).UTC(), -0.5},
	}
	for _, tc := range exact {
		t.Run(tc.name, func(t *testing.T) {
			if got := epochSeconds(tc.t); got != tc.want {
				t.Fatalf("epochSeconds(%s) = %.17g, want %.17g", tc.t, got, tc.want)
			}
		})
	}

	boundary := []struct {
		name string
		t    time.Time
	}{
		{"ordinary positive", time.Date(2026, 1, 1, 0, 0, 0, 999_999_999, time.UTC)},
		{"before epoch", time.Date(1969, 12, 31, 23, 59, 59, 999_999_999, time.UTC)},
		{"year 9999", time.Date(9999, 12, 31, 23, 59, 59, 999_999_999, time.UTC)},
	}
	for _, tc := range boundary {
		t.Run(tc.name, func(t *testing.T) {
			epoch := epochSeconds(tc.t)
			got, err := epochZone(epoch, true)
			if err != nil {
				t.Fatalf("epochZone(epochSeconds(%s)) failed: %v", tc.t, err)
			}
			if got.Unix() != tc.t.Unix() {
				t.Fatalf("epochSeconds(%s) = %.17g reconstructed Unix second %d, want %d", tc.t, epoch, got.Unix(), tc.t.Unix())
			}
		})
	}
}

func TestKnownOptionTypesAreStrictBothBackends(t *testing.T) {
	assertBoth(t, `say(is_err(write_file("_never_created.txt", "x", {append: "false"})))`, "true\n")
	assertBoth(t, `say(is_err(http_get("http://127.0.0.1:1", {timeout: "10"})))`, "true\n")
	assertBoth(t, `say(is_err(http_get("http://127.0.0.1:1", {max_body: 0 - 1})))`, "true\n")
	assertBoth(t, `say(is_err(http_get("http://127.0.0.1:1", {redirects: 0 - 1})))`, "true\n")
	assertBoth(t, `say(is_err(http_get("http://127.0.0.1:1", {insecure: "false"})))`, "true\n")
	assertBoth(t, `say(is_err(http_get("http://127.0.0.1:1", {max_bdy: 10})))`, "true\n")
}

func TestServeOptionValidation(t *testing.T) {
	routes := value.MakeMap()
	cases := []struct {
		name string
		opts value.Value
	}{
		{"static type", mkMap(value.MakeStr("routes"), routes, value.MakeStr("static"), value.MakeBool(false))},
		{"port type", mkMap(value.MakeStr("routes"), routes, value.MakeStr("port"), value.MakeFloat(8080))},
		{"port range", mkMap(value.MakeStr("routes"), routes, value.MakeStr("port"), value.MakeInt(65536))},
		{"open type", mkMap(value.MakeStr("routes"), routes, value.MakeStr("open"), value.MakeStr("false"))},
		{"unknown", mkMap(value.MakeStr("routes"), routes, value.MakeStr("opne"), value.MakeBool(false))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, errv := buildGUIServer(tc.opts.Obj().(*value.OrderedMap))
			if g != nil || !errv.IsErr() {
				t.Fatalf("buildGUIServer = (%v, %s), want catchable Err", g, errv.Display())
			}
		})
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestSayAndWarnPropagateWriteErrors(t *testing.T) {
	oldOut := swapStdout(failingWriter{})
	_, sayErr := builtinSay([]value.Value{value.MakeStr("x")})
	swapStdout(oldOut)
	if sayErr == nil || !strings.Contains(sayErr.Error(), "write failed") {
		t.Fatalf("say error = %v, want writer failure", sayErr)
	}

	oldErr := swapStderr(failingWriter{})
	_, warnErr := builtinWarn([]value.Value{value.MakeStr("x")})
	swapStderr(oldErr)
	if warnErr == nil || !strings.Contains(warnErr.Error(), "write failed") {
		t.Fatalf("warn error = %v, want writer failure", warnErr)
	}
}
