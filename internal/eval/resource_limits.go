package eval

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/anafalanx/drang/internal/value"
)

// Whole-value operations need a ceiling just as file/process reads do. Keeping
// strings at 64 MiB and materialized collections at one million elements avoids
// allocation panics and process-wide OOMs while leaving streaming APIs available
// for larger data.
var maxStringBytes int64 = 64 << 20

// Variables only so focused tests can exercise the production paths without
// allocating release-sized values. Production never reassigns them.
var maxCollectionItems = 1_000_000

func sizeFits(total int64, add int) bool {
	return add >= 0 && total >= 0 && int64(add) <= maxStringBytes-total
}

type limitedStringBuilder struct {
	strings.Builder
	limit int64
	err   error
}

func newLimitedStringBuilder(limit int64) *limitedStringBuilder {
	return &limitedStringBuilder{limit: limit}
}

func (b *limitedStringBuilder) Write(p []byte) (int, error) {
	return b.WriteString(string(p))
}

func (b *limitedStringBuilder) WriteString(s string) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	if int64(len(s)) > b.limit-int64(b.Len()) {
		b.err = fmt.Errorf("result exceeds the %d-byte string limit", b.limit)
		return 0, b.err
	}
	n, err := b.Builder.WriteString(s)
	b.err = err
	return n, err
}

func (b *limitedStringBuilder) WriteByte(c byte) error {
	if b.err != nil {
		return b.err
	}
	if int64(b.Len()) >= b.limit {
		b.err = fmt.Errorf("result exceeds the %d-byte string limit", b.limit)
		return b.err
	}
	b.err = b.Builder.WriteByte(c)
	return b.err
}

func (b *limitedStringBuilder) WriteRune(r rune) (int, error) {
	return b.WriteString(string(r))
}

func (b *limitedStringBuilder) Err() error { return b.err }

func displayWithin(v value.Value, limit int64) (string, bool) {
	if limit < 0 {
		return "", false
	}
	// The value already owns an immutable string; returning it at/below the cap
	// avoids an otherwise needless whole-string copy at every output sink.
	if v.Tag() == value.Str && int64(len(v.AsStr())) <= limit {
		return v.AsStr(), true
	}
	if uint64(limit) > uint64(^uint(0)>>1) {
		return "", false
	}
	return value.DisplayWithin(v, int(limit))
}

// DisplayValue renders a value for a whole-value output sink while enforcing
// the interpreter's string ceiling. Embedders such as the REPL should use this
// instead of Value.Display so interactive output cannot bypass the same limit
// as say, interpolation, and other language-visible materializations.
func DisplayValue(v value.Value) (string, error) {
	s, ok := displayWithin(v, maxStringBytes)
	if !ok {
		return "", fmt.Errorf("value exceeds the %d-byte string limit", maxStringBytes)
	}
	return s, nil
}

// quotedWithin is the bounded equivalent of strconv.Quote. It works in small
// rune-boundary chunks so an oversized diagnostic string cannot first allocate
// an even larger escaped copy. Complete results are byte-for-byte identical to
// strconv.Quote; on overflow the returned string is a safe prefix.
func quotedWithin(s string, limit int64) (string, bool) {
	b := newLimitedStringBuilder(limit)
	if err := b.WriteByte('"'); err != nil {
		return b.String(), false
	}
	const chunkBytes = 4096
	for len(s) > 0 {
		end := 0
		for end < len(s) && end < chunkBytes {
			_, width := utf8.DecodeRuneInString(s[end:])
			if width <= 0 {
				width = 1
			}
			if end > 0 && end+width > chunkBytes {
				break
			}
			end += width
		}
		q := strconv.Quote(s[:end])
		if _, err := b.WriteString(q[1 : len(q)-1]); err != nil {
			return b.String(), false
		}
		s = s[end:]
	}
	if err := b.WriteByte('"'); err != nil {
		return b.String(), false
	}
	return b.String(), true
}

// truncatedDiagnostic adds an explicit marker while keeping the final text
// within limit. prefix is expected to end at a decoding boundary, as the
// bounded renderers above guarantee.
func truncatedDiagnostic(prefix string, limit int64) string {
	const marker = "..."
	if limit <= 0 {
		return ""
	}
	if limit < int64(len(marker)) {
		return marker[:int(limit)]
	}
	prefix, _ = displayWithin(value.MakeStr(prefix), limit-int64(len(marker)))
	return prefix + marker
}

func concatValues(l, r value.Value) value.Value {
	ls, ok := displayWithin(l, maxStringBytes)
	if !ok {
		return value.MakeErr(fmt.Sprintf("concatenation exceeds the %d-byte string limit", maxStringBytes), 1)
	}
	rs, ok := displayWithin(r, maxStringBytes-int64(len(ls)))
	if !ok {
		return value.MakeErr(fmt.Sprintf("concatenation exceeds the %d-byte string limit", maxStringBytes), 1)
	}
	return value.MakeStr(ls + rs)
}
