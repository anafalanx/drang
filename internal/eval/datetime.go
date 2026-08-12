package eval

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/anafalanx/drang/internal/value"
)

// Date/time builtins. drang represents an instant as a float: seconds since the
// Unix epoch, with sub-second precision — so ordinary number operators handle
// arithmetic ($t + 3600) and comparison ($a < $b) with no new value type. now()
// reads the clock; sleep() pauses; format_time / parse_time / date_parts convert to and
// from human strings and components, using strftime-style %-codes in LOCAL time.

func builtinNow(args []value.Value) (value.Value, error) {
	if len(args) != 0 {
		return value.MakeNil(), fmt.Errorf("now expects no arguments, got %d", len(args))
	}
	return value.MakeFloat(epochSeconds(time.Now())), nil
}

func epochSeconds(t time.Time) float64 {
	sec := t.Unix()
	epoch := float64(sec)
	if t.Nanosecond() == 0 {
		return epoch
	}
	epoch += float64(t.Nanosecond()) / 1e9

	// A float64 epoch has progressively coarser spacing as its magnitude grows.
	// Near the end of the supported calendar range, a value one nanosecond below
	// the next second can therefore round up to that second. Keep the returned
	// value inside the time's original Unix-second interval whenever that interval
	// has a representable float; losing some fractional precision is preferable to
	// changing the calendar second.
	if sec < math.MaxInt64 && int64(math.Floor(epoch)) != sec {
		beforeUpper := math.Nextafter(float64(sec+1), math.Inf(-1))
		if int64(math.Floor(beforeUpper)) == sec {
			epoch = beforeUpper
		}
	}
	return epoch
}

func builtinSleep(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("sleep expects 1 argument (seconds), got %d", len(args))
	}
	if !args[0].IsNumber() {
		return value.MakeErr(fmt.Sprintf("sleep expects a number, got %s", args[0].TypeName()), 1), nil
	}
	secs := args[0].Num()
	nanos := secs * float64(time.Second)
	if math.IsNaN(secs) || math.IsInf(secs, 0) || secs < 0 || nanos >= 0x1p63 {
		return value.MakeErr("sleep: seconds must be finite, non-negative, and within time.Duration range", 1), nil
	}
	if secs > 0 {
		time.Sleep(time.Duration(nanos))
	}
	return value.MakeNil(), nil
}

// epochZone converts epoch seconds (with fraction) to a time.Time in the local zone, or
// UTC when utc is set.
func epochZone(epoch float64, utc bool) (time.Time, error) {
	if math.IsNaN(epoch) || math.IsInf(epoch, 0) || epoch < -0x1p63 || epoch >= 0x1p63 {
		return time.Time{}, fmt.Errorf("epoch must be finite and within int64 seconds range")
	}
	// Unix fractional seconds use floor decomposition: -0.1 is second -1 plus
	// 900 ms. Truncating toward zero can turn a value just below zero into second
	// 0 after the fractional nanoseconds round, crossing the Unix-second boundary.
	whole := math.Floor(epoch)
	sec := int64(whole)
	nsec := int64((epoch - whole) * 1e9)
	t := time.Unix(sec, nsec)
	// time.Unix accepts every int64, but time.Time's internal calendar epoch does
	// not represent the very lowest Unix seconds. Calendar conversion then wraps
	// those instants roughly 584 billion years forward (for example MinInt64
	// formats as a large positive year). A negative Unix instant must be no later
	// than 1970 in UTC, and a positive one no earlier; use that invariant to reject
	// the small unrepresentable tail instead of returning a plausible wrong date.
	year := t.UTC().Year()
	if (sec < 0 && year > 1970) || (sec > 0 && year < 1970) {
		return time.Time{}, fmt.Errorf("epoch is outside the supported calendar range")
	}
	if utc {
		return t.UTC(), nil
	}
	return t.Local(), nil
}

// utcOpt reads the optional trailing {utc: bool} options map (at args[idx], if present) for
// the datetime family. Like csvOpts it rejects a non-map opts argument and any key other than
// "utc", so a misspelled {UTC: true} can't silently fall back to local time.
func utcOpt(name string, args []value.Value, idx int) (bool, error) {
	if idx >= len(args) {
		return false, nil
	}
	opts := args[idx]
	if opts.Tag() != value.Map {
		return false, fmt.Errorf("%s options must be a map, got %s", name, opts.TypeName())
	}
	m := opts.Obj().(*value.OrderedMap)
	for _, k := range m.Keys() {
		if k.Tag() != value.Str || k.AsStr() != "utc" {
			return false, fmt.Errorf("%s: unknown option %q", name, k.Display())
		}
	}
	v, ok := m.Get(value.MakeStr("utc"))
	if !ok {
		return false, nil
	}
	if v.Tag() != value.Bool {
		return false, fmt.Errorf("%s: utc must be a bool, got %s", name, v.TypeName())
	}
	return v.AsBool(), nil
}

// builtinFormatTime formats an epoch with %-codes — the spelled-out inverse of
// parse_time (the pair rhymes; the C name strftime did not).
func builtinFormatTime(args []value.Value) (value.Value, error) {
	if len(args) < 2 || len(args) > 3 {
		return value.MakeNil(), fmt.Errorf("format_time expects 2 or 3 arguments (epoch, format, opts?), got %d", len(args))
	}
	if !args[0].IsNumber() {
		return value.MakeErr(fmt.Sprintf("format_time expects a number epoch, got %s", args[0].TypeName()), 1), nil
	}
	if args[1].Tag() != value.Str {
		return value.MakeErr(fmt.Sprintf("format_time expects a format string, got %s", args[1].TypeName()), 1), nil
	}
	utc, err := utcOpt("format_time", args, 2)
	if err != nil {
		return value.MakeErr(err.Error(), 1), nil
	}
	t, terr := epochZone(args[0].Num(), utc)
	if terr != nil {
		return value.MakeErr("format_time: "+terr.Error(), 1), nil
	}
	formatted, ferr := strftimeFormat(t, args[1].AsStr())
	if ferr != nil {
		return value.MakeErr("format_time: "+ferr.Error(), 1), nil
	}
	return value.MakeStr(formatted), nil
}

func builtinParseTime(args []value.Value) (value.Value, error) {
	if len(args) < 2 || len(args) > 3 {
		return value.MakeNil(), fmt.Errorf("parse_time expects 2 or 3 arguments (string, format, opts?), got %d", len(args))
	}
	if args[0].Tag() != value.Str || args[1].Tag() != value.Str {
		return value.MakeErr("parse_time expects (string, format) string arguments", 1), nil
	}
	layout, err := strftimeToLayout(args[1].AsStr())
	if err != nil {
		return value.MakeErr(err.Error(), 1), nil
	}
	utc, uerr := utcOpt("parse_time", args, 2)
	if uerr != nil {
		return value.MakeErr(uerr.Error(), 1), nil
	}
	loc := time.Local
	if utc {
		loc = time.UTC
	}
	t, perr := time.ParseInLocation(layout, args[0].AsStr(), loc)
	if perr != nil {
		return value.MakeErr("parse_time: "+perr.Error(), 1), nil
	}
	return value.MakeFloat(epochSeconds(t)), nil
}

func builtinDateParts(args []value.Value) (value.Value, error) {
	if len(args) < 1 || len(args) > 2 {
		return value.MakeNil(), fmt.Errorf("date_parts expects 1 or 2 arguments (epoch, opts?), got %d", len(args))
	}
	if !args[0].IsNumber() {
		return value.MakeErr(fmt.Sprintf("date_parts expects a number epoch, got %s", args[0].TypeName()), 1), nil
	}
	utc, err := utcOpt("date_parts", args, 1)
	if err != nil {
		return value.MakeErr(err.Error(), 1), nil
	}
	t, terr := epochZone(args[0].Num(), utc)
	if terr != nil {
		return value.MakeErr("date_parts: "+terr.Error(), 1), nil
	}
	m := value.MakeMap()
	om := m.Obj().(*value.OrderedMap)
	om.Set(value.MakeStr("year"), value.MakeInt(int64(t.Year())))
	om.Set(value.MakeStr("month"), value.MakeInt(int64(t.Month())))
	om.Set(value.MakeStr("day"), value.MakeInt(int64(t.Day())))
	om.Set(value.MakeStr("hour"), value.MakeInt(int64(t.Hour())))
	om.Set(value.MakeStr("minute"), value.MakeInt(int64(t.Minute())))
	om.Set(value.MakeStr("second"), value.MakeInt(int64(t.Second())))
	om.Set(value.MakeStr("weekday"), value.MakeInt(int64(t.Weekday()))) // 0 = Sunday
	om.Set(value.MakeStr("yearday"), value.MakeInt(int64(t.YearDay())))
	return m, nil
}

// strftimeFormat renders t per a strftime-style %-code format. Codes with no Go
// layout equivalent (%j, %w) are computed directly; an unknown %X is left literal.
func strftimeFormat(t time.Time, f string) (string, error) {
	b := newLimitedStringBuilder(maxStringBytes)
	for i := 0; i < len(f); i++ {
		if f[i] != '%' || i+1 >= len(f) {
			b.WriteByte(f[i])
			continue
		}
		i++
		switch f[i] {
		case 'Y':
			b.WriteString(t.Format("2006"))
		case 'y':
			b.WriteString(t.Format("06"))
		case 'm':
			b.WriteString(t.Format("01"))
		case 'd':
			b.WriteString(t.Format("02"))
		case 'e':
			b.WriteString(t.Format("_2"))
		case 'H':
			b.WriteString(t.Format("15"))
		case 'I':
			b.WriteString(t.Format("03"))
		case 'M':
			b.WriteString(t.Format("04"))
		case 'S':
			b.WriteString(t.Format("05"))
		case 'p':
			b.WriteString(t.Format("PM"))
		case 'A':
			b.WriteString(t.Format("Monday"))
		case 'a':
			b.WriteString(t.Format("Mon"))
		case 'B':
			b.WriteString(t.Format("January"))
		case 'b':
			b.WriteString(t.Format("Jan"))
		case 'j':
			b.WriteString(fmt.Sprintf("%03d", t.YearDay()))
		case 'w':
			b.WriteString(strconv.Itoa(int(t.Weekday())))
		case 'z':
			b.WriteString(t.Format("-0700"))
		case 'Z':
			b.WriteString(t.Format("MST"))
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case '%':
			b.WriteByte('%')
		default:
			b.WriteByte('%')
			b.WriteByte(f[i])
		}
	}
	if err := b.Err(); err != nil {
		return "", err
	}
	return b.String(), nil
}

// strftimeToLayout translates a strftime-style format into a Go reference layout for
// parsing. Codes without a Go-layout equivalent (e.g. %j) are unsupported.
func strftimeToLayout(f string) (string, error) {
	b := newLimitedStringBuilder(maxStringBytes)
	for i := 0; i < len(f); i++ {
		if f[i] != '%' || i+1 >= len(f) {
			b.WriteByte(f[i])
			continue
		}
		i++
		switch f[i] {
		case 'Y':
			b.WriteString("2006")
		case 'y':
			b.WriteString("06")
		case 'm':
			b.WriteString("01")
		case 'd':
			b.WriteString("02")
		case 'e':
			b.WriteString("_2")
		case 'H':
			b.WriteString("15")
		case 'I':
			b.WriteString("03")
		case 'M':
			b.WriteString("04")
		case 'S':
			b.WriteString("05")
		case 'p':
			b.WriteString("PM")
		case 'A':
			b.WriteString("Monday")
		case 'a':
			b.WriteString("Mon")
		case 'B':
			b.WriteString("January")
		case 'b':
			b.WriteString("Jan")
		case 'z':
			b.WriteString("-0700")
		case 'Z':
			b.WriteString("MST")
		case '%':
			b.WriteByte('%')
		default:
			return "", fmt.Errorf("parse_time: unsupported format code %%%c", f[i])
		}
	}
	if err := b.Err(); err != nil {
		return "", err
	}
	return b.String(), nil
}
