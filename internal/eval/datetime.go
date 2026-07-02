package eval

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/anafalanx/drang/internal/value"
)

// Date/time builtins. drang represents an instant as a float: seconds since the
// Unix epoch, with sub-second precision — so ordinary number operators handle
// arithmetic ($t + 3600) and comparison ($a < $b) with no new value type. now()
// reads the clock; sleep() pauses; strftime / parse_time / date_parts convert to and
// from human strings and components, using strftime %-codes in LOCAL time.

func builtinNow(args []value.Value) (value.Value, error) {
	if len(args) != 0 {
		return value.MakeNil(), fmt.Errorf("now expects no arguments, got %d", len(args))
	}
	return value.MakeFloat(float64(time.Now().UnixNano()) / 1e9), nil
}

func builtinSleep(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("sleep expects 1 argument (seconds), got %d", len(args))
	}
	if !args[0].IsNumber() {
		return value.MakeErr(fmt.Sprintf("sleep expects a number, got %s", args[0].TypeName()), 1), nil
	}
	if secs := args[0].Num(); secs > 0 {
		time.Sleep(time.Duration(secs * float64(time.Second)))
	}
	return value.MakeNil(), nil
}

// epochToTime converts epoch seconds (with fraction) to a LOCAL time.Time.
func epochToTime(epoch float64) time.Time { return epochZone(epoch, false) }

// epochZone converts epoch seconds (with fraction) to a time.Time in the local zone, or
// UTC when utc is set.
func epochZone(epoch float64, utc bool) time.Time {
	sec := int64(epoch)
	nsec := int64((epoch - float64(sec)) * 1e9)
	t := time.Unix(sec, nsec)
	if utc {
		return t.UTC()
	}
	return t.Local()
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
		if k.Display() != "utc" {
			return false, fmt.Errorf("%s: unknown option %q", name, k.Display())
		}
	}
	v, ok := m.Get(value.MakeStr("utc"))
	return ok && v.Truthy(), nil
}

func builtinStrftime(args []value.Value) (value.Value, error) {
	if len(args) < 2 || len(args) > 3 {
		return value.MakeNil(), fmt.Errorf("strftime expects 2 or 3 arguments (epoch, format, opts?), got %d", len(args))
	}
	if !args[0].IsNumber() {
		return value.MakeErr(fmt.Sprintf("strftime expects a number epoch, got %s", args[0].TypeName()), 1), nil
	}
	if args[1].Tag() != value.Str {
		return value.MakeErr(fmt.Sprintf("strftime expects a format string, got %s", args[1].TypeName()), 1), nil
	}
	utc, err := utcOpt("strftime", args, 2)
	if err != nil {
		return value.MakeErr(err.Error(), 1), nil
	}
	return value.MakeStr(strftimeFormat(epochZone(args[0].Num(), utc), args[1].AsStr())), nil
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
	return value.MakeFloat(float64(t.UnixNano()) / 1e9), nil
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
	t := epochZone(args[0].Num(), utc)
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
func strftimeFormat(t time.Time, f string) string {
	var b strings.Builder
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
	return b.String()
}

// strftimeToLayout translates a strftime-style format into a Go reference layout for
// parsing. Codes without a Go-layout equivalent (e.g. %j) are unsupported.
func strftimeToLayout(f string) (string, error) {
	var b strings.Builder
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
	return b.String(), nil
}
