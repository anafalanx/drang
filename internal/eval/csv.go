package eval

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/anafalanx/drang/internal/value"
)

// CSV support. `from_csv(text [, opts])` parses RFC 4180 CSV into rows and
// `to_csv(rows [, opts])` renders rows back. Built on encoding/csv, which handles
// the genuinely hard parts: fields with embedded commas/quotes/newlines and the
// doubled-quote escape (""). Cells are ALWAYS strings on read (no type inference —
// convert explicitly with int()/etc.).
//
// Rows are arrays of strings by default; with {header: true} the first row names the
// columns and each later row becomes a record (map) keyed by those names. to_csv
// accepts either shape (records auto-write a header from the first record's keys,
// values pulled by key). By default it is strict: ragged rows, duplicate header
// names, and records whose keys differ from the header are errors; {lenient: true}
// relaxes these (pad/truncate, last-column-wins, drop unknown keys).
//
// Error convention: option misuse (a bad type, a multi-rune or invalid separator,
// an unknown option key) ABORTS; malformed CSV and unencodable rows are catchable
// Err values.
//
// Inherited limits (from encoding/csv): a CR/LF pair *inside* a quoted field is read
// back as a lone LF; blank lines are skipped, so a single all-empty-field row
// (written as a bare blank line) does not survive a round trip.

var fromCSVOptKeys = map[string]bool{
	"sep": true, "header": true, "comment": true, "trim": true, "lazy_quotes": true, "lenient": true,
}
var toCSVOptKeys = map[string]bool{
	"sep": true, "header": true, "crlf": true, "lenient": true, "sanitize": true,
}

// builtinFromCSV parses CSV text into an array of rows.
func builtinFromCSV(args []value.Value) (value.Value, error) {
	if len(args) < 1 || len(args) > 2 {
		return value.MakeNil(), fmt.Errorf("from_csv expects 1 or 2 arguments (string, options?), got %d", len(args))
	}
	if args[0].Tag() != value.Str {
		return value.MakeNil(), fmt.Errorf("from_csv expects a string, got %s", args[0].TypeName())
	}
	if int64(len(args[0].AsStr())) > maxStringBytes {
		return value.MakeErr(fmt.Sprintf("from_csv: input exceeds the %d-byte string limit", maxStringBytes), 1), nil
	}
	opts, err := csvOpts("from_csv", args, fromCSVOptKeys)
	if err != nil {
		return value.MakeNil(), err
	}
	sep, err := optRune(opts, "sep", ',')
	if err != nil {
		return value.MakeNil(), err
	}
	comment, err := optRune(opts, "comment", 0)
	if err != nil {
		return value.MakeNil(), err
	}
	if comment != 0 && comment == sep {
		return value.MakeNil(), fmt.Errorf("from_csv: sep and comment must differ")
	}
	header, err := optBool(opts, "header", false)
	if err != nil {
		return value.MakeNil(), err
	}
	trim, err := optBool(opts, "trim", false)
	if err != nil {
		return value.MakeNil(), err
	}
	lazy, err := optBool(opts, "lazy_quotes", false)
	if err != nil {
		return value.MakeNil(), err
	}
	lenient, err := optBool(opts, "lenient", false)
	if err != nil {
		return value.MakeNil(), err
	}

	r := csv.NewReader(strings.NewReader(stripBOM(args[0].AsStr())))
	r.Comma = sep
	r.Comment = comment
	r.TrimLeadingSpace = trim
	r.LazyQuotes = lazy
	r.FieldsPerRecord = 0 // strict: the first record fixes the column count
	if lenient {
		r.FieldsPerRecord = -1 // no per-record count check
	}
	// Reject a single-record delimiter flood before encoding/csv allocates millions
	// of field descriptors. The scan understands quoted fields, so separators in
	// one legitimate quoted cell do not create a false resource-limit error.
	if csvRecordFieldFlood(stripBOM(args[0].AsStr()), sep, comment, trim, lazy) {
		return value.MakeErr(fmt.Sprintf("from_csv: result exceeds the %d-cell collection limit", maxCollectionItems), 1), nil
	}

	// Read incrementally rather than ReadAll: this avoids retaining both the
	// [][]string parse tree and the converted drang graph, and lets the aggregate
	// cell budget stop hostile input before the full result is materialized.
	read := func() ([]string, value.Value, bool) {
		rec, e := r.Read()
		switch {
		case e == io.EOF:
			return nil, value.MakeNil(), false
		case e != nil:
			return nil, value.MakeErr("from_csv: "+e.Error(), 1), false
		default:
			return rec, value.MakeNil(), true
		}
	}

	totalCells := 0
	if !header {
		rows := make([]value.Value, 0)
		for {
			rec, errv, ok := read()
			if !ok {
				if errv.Tag() == value.Err {
					return errv, nil
				}
				return value.MakeArray(rows), nil
			}
			if len(rec) > maxCollectionItems-totalCells {
				return value.MakeErr(fmt.Sprintf("from_csv: result exceeds the %d-cell collection limit", maxCollectionItems), 1), nil
			}
			totalCells += len(rec)
			rows = append(rows, recordToArray(rec))
		}
	}

	keys, errv, ok := read()
	if !ok {
		if errv.Tag() == value.Err {
			return errv, nil
		}
		return value.MakeArray(nil), nil // no header row -> no rows
	}
	totalCells = len(keys)
	if totalCells > maxCollectionItems {
		return value.MakeErr(fmt.Sprintf("from_csv: result exceeds the %d-cell collection limit", maxCollectionItems), 1), nil
	}
	if !lenient {
		if dup, ok := firstDuplicate(keys); ok {
			return value.MakeErr(fmt.Sprintf("from_csv: duplicate header name %q (set lenient to keep the last column)", dup), 1), nil
		}
	}
	rows := make([]value.Value, 0)
	for {
		rec, errv, ok := read()
		if !ok {
			if errv.Tag() == value.Err {
				return errv, nil
			}
			return value.MakeArray(rows), nil
		}
		if len(rec) > maxCollectionItems-totalCells {
			return value.MakeErr(fmt.Sprintf("from_csv: input exceeds the %d-cell collection limit", maxCollectionItems), 1), nil
		}
		totalCells += len(rec)
		// Lenient rows are padded to the header width. Bound the materialized
		// maps, not just the number of fields physically present in the input.
		if len(keys) > 0 && len(rows) >= maxCollectionItems/len(keys) {
			return value.MakeErr(fmt.Sprintf("from_csv: result exceeds the %d-cell collection limit", maxCollectionItems), 1), nil
		}
		rows = append(rows, recordToMap(keys, rec))
	}
}

// csvRecordFieldFlood is a non-allocating guard in front of encoding/csv.Reader.
// It mirrors the quote boundaries that determine whether a separator creates a
// field. Malformed strict quoting returns false because Reader will stop at that
// quote before it can materialize a later separator flood.
func csvRecordFieldFlood(s string, sep, comment rune, trim, lazy bool) bool {
	fields := 1
	fieldStart, recordStart := true, true
	inQuote, commentLine := false, false
	for i := 0; i < len(s); {
		r, width := utf8.DecodeRuneInString(s[i:])
		if commentLine {
			i += width
			if r == '\n' {
				fields, fieldStart, recordStart, commentLine = 1, true, true, false
			}
			continue
		}
		if inQuote {
			if r != '"' {
				i += width
				continue
			}
			next, nextWidth := rune(0), 0
			if i+width < len(s) {
				next, nextWidth = utf8.DecodeRuneInString(s[i+width:])
			}
			if next == '"' { // doubled quote inside a quoted field
				i += width + nextWidth
				continue
			}
			closing := nextWidth == 0 || next == sep || next == '\n' || (next == '\r' && i+width+nextWidth < len(s) && s[i+width+nextWidth] == '\n')
			if closing {
				inQuote = false
				i += width
				continue
			}
			if !lazy {
				return false
			}
			i += width // LazyQuotes treats this quote as cell data.
			continue
		}

		if recordStart && comment != 0 && r == comment {
			commentLine = true
			i += width
			continue
		}
		if r == '\n' {
			fields, fieldStart, recordStart = 1, true, true
			i += width
			continue
		}
		if r == sep {
			fields++
			if fields > maxCollectionItems {
				return true
			}
			fieldStart, recordStart = true, false
			i += width
			continue
		}
		if fieldStart {
			if trim && unicode.IsSpace(r) {
				recordStart = false // leading space prevents comment-line recognition
				i += width
				continue
			}
			if r == '"' {
				inQuote, fieldStart, recordStart = true, false, false
				i += width
				continue
			}
		} else if r == '"' && !lazy {
			return false
		}
		fieldStart, recordStart = false, false
		i += width
	}
	return false
}

// stripBOM removes a leading UTF-8 byte-order mark (EF BB BF), which Excel and some
// Windows tools prepend to CSV files.
func stripBOM(s string) string {
	if len(s) >= 3 && s[0] == 0xEF && s[1] == 0xBB && s[2] == 0xBF {
		return s[3:]
	}
	return s
}

func firstDuplicate(keys []string) (string, bool) {
	seen := make(map[string]bool, len(keys))
	for _, k := range keys {
		if seen[k] {
			return k, true
		}
		seen[k] = true
	}
	return "", false
}

func recordToArray(rec []string) value.Value {
	elems := make([]value.Value, len(rec))
	for i, f := range rec {
		elems[i] = value.MakeStr(f)
	}
	return value.MakeArray(elems)
}

// recordToMap zips header keys with a record's fields. A short record (lenient mode
// only — strict mode errors at ReadAll) pads missing trailing fields with ""; fields
// beyond the header are dropped.
func recordToMap(keys, rec []string) value.Value {
	m := value.MakeMap()
	om := m.Obj().(*value.OrderedMap)
	for i, k := range keys {
		field := ""
		if i < len(rec) {
			field = rec[i]
		}
		om.Set(value.MakeStr(k), value.MakeStr(field))
	}
	return m
}

// builtinToCSV renders an array of rows (arrays or records) as CSV text.
func builtinToCSV(args []value.Value) (value.Value, error) {
	if len(args) < 1 || len(args) > 2 {
		return value.MakeNil(), fmt.Errorf("to_csv expects 1 or 2 arguments (rows, options?), got %d", len(args))
	}
	if args[0].Tag() != value.Arr {
		return value.MakeNil(), fmt.Errorf("to_csv expects an array of rows, got %s", args[0].TypeName())
	}
	opts, err := csvOpts("to_csv", args, toCSVOptKeys)
	if err != nil {
		return value.MakeNil(), err
	}
	sep, err := optRune(opts, "sep", ',')
	if err != nil {
		return value.MakeNil(), err
	}
	crlf, err := optBool(opts, "crlf", true) // CRLF by default (RFC 4180 / Excel); {crlf: false} for \n
	if err != nil {
		return value.MakeNil(), err
	}
	writeHeader, err := optBool(opts, "header", true) // records write a header row by default
	if err != nil {
		return value.MakeNil(), err
	}
	lenient, err := optBool(opts, "lenient", false)
	if err != nil {
		return value.MakeNil(), err
	}
	sanitize, err := optBool(opts, "sanitize", false) // opt-in CSV-injection defense (off = faithful data)
	if err != nil {
		return value.MakeNil(), err
	}

	rows := args[0].Obj().(*value.Array).Elems
	if csvShapeItems(rows, writeHeader) > maxCollectionItems {
		return value.MakeErr(fmt.Sprintf("to_csv: input exceeds the %d-cell collection limit", maxCollectionItems), 1), nil
	}
	records, derr := rowsToRecords(rows, writeHeader, lenient)
	if derr != nil {
		return value.MakeErr("to_csv: "+derr.Error(), 1), nil
	}
	if sanitize {
		for _, rec := range records {
			for i, f := range rec {
				rec[i] = sanitizeCSVField(f)
			}
		}
	}
	b := newLimitedStringBuilder(maxStringBytes)
	w := csv.NewWriter(b)
	w.Comma = sep
	w.UseCRLF = crlf
	if err := w.WriteAll(records); err != nil { // WriteAll flushes
		return value.MakeErr("to_csv: "+err.Error(), 1), nil
	}
	return value.MakeStr(b.String()), nil
}

func csvShapeItems(rows []value.Value, writeHeader bool) int {
	if len(rows) > maxCollectionItems {
		return maxCollectionItems + 1
	}
	total := 0
	add := func(n int) bool {
		if n < 0 || n > maxCollectionItems-total {
			return false
		}
		total += n
		return true
	}
	// Record rows always materialize to the first record's full key width. In
	// lenient mode a later sparse record is padded with empty cells, so counting
	// only its physically present keys would let a large rectangular result pass
	// this pre-allocation guard.
	if len(rows) > 0 && rows[0].Tag() == value.Map {
		width := rows[0].Obj().(*value.OrderedMap).Len()
		if writeHeader && !add(width) {
			return maxCollectionItems + 1
		}
		for range rows {
			if !add(width) {
				return maxCollectionItems + 1
			}
		}
		return total
	}
	for i, row := range rows {
		switch row.Tag() {
		case value.Arr:
			if !add(row.Obj().(*value.Array).Len()) {
				return maxCollectionItems + 1
			}
		case value.Map:
			n := row.Obj().(*value.OrderedMap).Len()
			if !add(n) || (i == 0 && writeHeader && !add(n)) {
				return maxCollectionItems + 1
			}
		default:
			continue
		}
	}
	return total
}

// sanitizeCSVField neutralizes a field a spreadsheet might execute as a formula. A cell that
// begins with '=', '+', '-', '@', or a leading control-whitespace char — tab (0x09), carriage
// return (0x0D), or line feed (0x0A) — can run as a formula in Excel / Google Sheets (CSV
// injection; this is exactly the OWASP dangerous-lead set) because the leading whitespace is
// stripped before the "=cmd|..." payload is evaluated. Prefixing a single quote forces the whole
// cell to literal text. This is opt-in (to_csv {sanitize: true}) because it changes the data: a
// leading '-' number like "-5" becomes "'-5", so it belongs only on output destined for a
// spreadsheet, not on faithful data exchange. (A leading SPACE is deliberately not treated as
// dangerous: unlike tab/CR/LF it is not stripped, so it already defeats formula evaluation.)
func sanitizeCSVField(f string) string {
	if f == "" {
		return f
	}
	switch f[0] {
	case '=', '+', '-', '@', '\t', '\r', '\n':
		return "'" + f
	}
	return f
}

// rowsToRecords converts drang rows into [][]string, choosing array vs record mode
// from the first row (the rest must match).
func rowsToRecords(rows []value.Value, writeHeader, lenient bool) ([][]string, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	switch rows[0].Tag() {
	case value.Arr:
		return arrayRowsToRecords(rows, lenient)
	case value.Map:
		return mapRowsToRecords(rows, writeHeader, lenient)
	default:
		return nil, fmt.Errorf("each row must be an array or a record, but row 1 is %s", rows[0].TypeName())
	}
}

func arrayRowsToRecords(rows []value.Value, lenient bool) ([][]string, error) {
	out := make([][]string, len(rows))
	width := -1
	for i, row := range rows {
		if row.Tag() != value.Arr {
			return nil, fmt.Errorf("row %d has type %s, but row 1 is an array", i+1, row.TypeName())
		}
		elems := row.Obj().(*value.Array).Elems
		if width < 0 {
			width = len(elems)
		}
		if !lenient && len(elems) != width {
			return nil, fmt.Errorf("row %d has %d fields, but row 1 has %d (set lenient to allow ragged rows)", i+1, len(elems), width)
		}
		rec := make([]string, len(elems))
		for j, cell := range elems {
			s, err := cellString(cell)
			if err != nil {
				return nil, fmt.Errorf("row %d field %d: %v", i+1, j+1, err)
			}
			rec[j] = s
		}
		out[i] = rec
	}
	return out, nil
}

func mapRowsToRecords(rows []value.Value, writeHeader, lenient bool) ([][]string, error) {
	keys := rows[0].Obj().(*value.OrderedMap).Keys()
	header := make([]string, len(keys))
	headerSet := make(map[string]bool, len(keys))
	for i, k := range keys {
		header[i] = k.Display() // a scalar key stringifies (int 1 -> "1")
		// Distinct keys can stringify to the same header (int 1 vs string "1"); reject the
		// collision, mirroring from_csv's strict duplicate-header rejection.
		if headerSet[header[i]] {
			return nil, fmt.Errorf("duplicate header %q (distinct record keys collide as strings)", header[i])
		}
		headerSet[header[i]] = true
	}
	var out [][]string
	if writeHeader {
		out = append(out, header)
	}
	for i, row := range rows {
		if row.Tag() != value.Map {
			return nil, fmt.Errorf("row %d has type %s, but row 1 is a record", i+1, row.TypeName())
		}
		m := row.Obj().(*value.OrderedMap)
		if !lenient { // every record must have exactly the header's fields
			for _, rk := range m.Keys() {
				if !headerSet[rk.Display()] {
					return nil, fmt.Errorf("row %d has field %q not present in row 1", i+1, rk.Display())
				}
			}
		}
		rec := make([]string, len(keys))
		for j, k := range keys {
			v, present := m.Get(k)
			if !present {
				if lenient {
					rec[j] = ""
					continue
				}
				return nil, fmt.Errorf("row %d is missing field %q", i+1, header[j])
			}
			s, err := cellString(v)
			if err != nil {
				return nil, fmt.Errorf("row %d field %q: %v", i+1, header[j], err)
			}
			rec[j] = s
		}
		out = append(out, rec)
	}
	return out, nil
}

// cellString renders one scalar drang value as a CSV cell. nil is an empty cell; a
// non-scalar (array/map/etc.) cannot be a cell.
func cellString(v value.Value) (string, error) {
	switch v.Tag() {
	case value.Str:
		return v.AsStr(), nil
	case value.Int:
		return strconv.FormatInt(v.AsInt(), 10), nil
	case value.Float:
		return strconv.FormatFloat(v.AsFloat(), 'g', -1, 64), nil
	case value.Bool:
		if v.AsBool() {
			return "true", nil
		}
		return "false", nil
	case value.Nil:
		return "", nil
	default:
		return "", fmt.Errorf("cannot write %s as a CSV cell", v.TypeName())
	}
}

// --- option-map helpers -----------------------------------------------------

// csvOpts returns the optional trailing options map (or nil), after rejecting an
// options argument that is not a map or that carries an unknown key (both misuse).
func csvOpts(name string, args []value.Value, allowed map[string]bool) (*value.OrderedMap, error) {
	if len(args) < 2 {
		return nil, nil
	}
	if args[1].Tag() != value.Map {
		return nil, fmt.Errorf("%s options must be a map, got %s", name, args[1].TypeName())
	}
	m := args[1].Obj().(*value.OrderedMap)
	for _, k := range m.Keys() {
		if !allowed[k.Display()] {
			return nil, fmt.Errorf("%s: unknown option %q", name, k.Display())
		}
	}
	return m, nil
}

// optBool reads a boolean option. A present-but-non-bool value is misuse (aborts),
// so a stray string like "false" can't be silently read as truthy.
func optBool(m *value.OrderedMap, key string, def bool) (bool, error) {
	if m == nil {
		return def, nil
	}
	v, ok := m.Get(value.MakeStr(key))
	if !ok {
		return def, nil
	}
	if v.Tag() != value.Bool {
		return false, fmt.Errorf("%s option must be true or false, got %s", key, v.TypeName())
	}
	return v.AsBool(), nil
}

// optRune reads a single-character string option as a delimiter rune. A non-string,
// a multi- or zero-rune value, or a rune encoding/csv rejects is misuse (aborts).
func optRune(m *value.OrderedMap, key string, def rune) (rune, error) {
	if m == nil {
		return def, nil
	}
	v, ok := m.Get(value.MakeStr(key))
	if !ok {
		return def, nil
	}
	if v.Tag() != value.Str {
		return 0, fmt.Errorf("%s option must be a single-character string, got %s", key, v.TypeName())
	}
	rs := []rune(v.AsStr())
	if len(rs) != 1 {
		return 0, fmt.Errorf("%s option must be exactly one character, got %q", key, v.AsStr())
	}
	r := rs[0]
	if r == '"' || r == '\r' || r == '\n' || r == utf8.RuneError {
		return 0, fmt.Errorf("%s option is not a valid delimiter: %q", key, v.AsStr())
	}
	return r, nil
}
