package eval

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/anafalanx/drang/internal/value"
)

// Format-spec support for the format() builtin. A placeholder is either {} (render
// like say) or {:spec}, where spec is a Python/Rust-inspired subset:
//
//	[[fill]align][sign][#][0][width][.precision][type]
//
// align is < (left) > (right) ^ (center); sign is + or space; # is the alternate
// form; 0 is sign-aware zero-padding; type is one of d b o x X f F e E g G s %.
// The numeric core is produced via Go's fmt; alignment, fill, and centering (which
// Go's fmt cannot do) are applied on top.
type fspec struct {
	fill  rune
	align byte // '<' '>' '^', or 0 (default: right for numbers, left otherwise)
	sign  byte // '+' or ' ', or 0 ('-', the default, is a no-op)
	alt   bool // '#'
	zero  bool // '0' — sign-aware zero pad
	width int  // -1 = unset
	prec  int  // -1 = unset
	typ   byte // d b o x X f F e E g G s % or 0
}

func isAlignRune(r rune) bool { return r == '<' || r == '>' || r == '^' }

func parseSpec(s string) (fspec, error) {
	sp := fspec{fill: ' ', width: -1, prec: -1}
	r := []rune(s)
	i := 0
	switch {
	case len(r) >= 2 && isAlignRune(r[1]):
		sp.fill, sp.align, i = r[0], byte(r[1]), 2
	case len(r) >= 1 && isAlignRune(r[0]):
		sp.align, i = byte(r[0]), 1
	}
	if i < len(r) && (r[i] == '+' || r[i] == '-' || r[i] == ' ') {
		if r[i] != '-' {
			sp.sign = byte(r[i])
		}
		i++
	}
	if i < len(r) && r[i] == '#' {
		sp.alt, i = true, i+1
	}
	if i < len(r) && r[i] == '0' {
		sp.zero, i = true, i+1
	}
	ws := i
	for i < len(r) && r[i] >= '0' && r[i] <= '9' {
		i++
	}
	if i > ws {
		sp.width, _ = strconv.Atoi(string(r[ws:i]))
	}
	if i < len(r) && r[i] == '.' {
		i++
		ps := i
		for i < len(r) && r[i] >= '0' && r[i] <= '9' {
			i++
		}
		if i == ps {
			return sp, fmt.Errorf("spec %q: '.' must be followed by a precision", s)
		}
		sp.prec, _ = strconv.Atoi(string(r[ps:i]))
	}
	if i < len(r) {
		sp.typ, i = byte(r[i]), i+1
	}
	if i != len(r) {
		return sp, fmt.Errorf("invalid format spec %q", s)
	}
	return sp, nil
}

// formatArg renders v per a {:spec} spec, or returns an error (which format()
// surfaces as a catchable Err value).
func formatArg(spec string, v value.Value) (string, error) {
	sp, err := parseSpec(spec)
	if err != nil {
		return "", err
	}

	signFlag := ""
	switch sp.sign {
	case '+':
		signFlag = "+"
	case ' ':
		signFlag = " "
	}
	altFlag := ""
	if sp.alt {
		altFlag = "#"
	}

	var core string
	numeric := false
	switch sp.typ {
	case 'd', 'b', 'o', 'x', 'X':
		if v.Tag() != value.Int {
			return "", fmt.Errorf("format type %q needs an int, got %s", string(sp.typ), v.TypeName())
		}
		if sp.prec >= 0 {
			return "", fmt.Errorf("precision is not allowed with integer type %q", string(sp.typ))
		}
		numeric = true
		core = fmt.Sprintf("%"+signFlag+altFlag+string(sp.typ), v.AsInt())
	case 'f', 'F', 'e', 'E', 'g', 'G':
		if !v.IsNumber() {
			return "", fmt.Errorf("format type %q needs a number, got %s", string(sp.typ), v.TypeName())
		}
		numeric = true
		prec := ""
		if sp.prec >= 0 {
			prec = "." + strconv.Itoa(sp.prec)
		}
		core = fmt.Sprintf("%"+signFlag+altFlag+prec+string(sp.typ), v.Num())
	case '%':
		if !v.IsNumber() {
			return "", fmt.Errorf("format type '%%' needs a number, got %s", v.TypeName())
		}
		numeric = true
		prec := 6
		if sp.prec >= 0 {
			prec = sp.prec
		}
		core = fmt.Sprintf("%"+signFlag+"."+strconv.Itoa(prec)+"f", v.Num()*100) + "%"
	case 's':
		if sp.sign != 0 || sp.alt {
			return "", fmt.Errorf("sign/# is not valid with string type 's'")
		}
		core = truncate(v.Display(), sp.prec)
	case 0:
		switch v.Tag() {
		case value.Int:
			if sp.prec >= 0 {
				return "", fmt.Errorf("precision is not allowed for an int (use a float type like 'f')")
			}
			numeric = true
			core = fmt.Sprintf("%"+signFlag+"d", v.AsInt())
		case value.Float:
			numeric = true
			prec := ""
			if sp.prec >= 0 {
				prec = "." + strconv.Itoa(sp.prec)
			}
			core = fmt.Sprintf("%"+signFlag+prec+"g", v.AsFloat())
		default:
			if sp.sign != 0 || sp.alt {
				return "", fmt.Errorf("sign/# is only valid for numbers, not %s", v.TypeName())
			}
			core = truncate(v.Display(), sp.prec)
		}
	default:
		return "", fmt.Errorf("unknown format type %q", string(sp.typ))
	}

	if sp.width < 0 || runeLen(core) >= sp.width {
		return core, nil
	}
	if sp.zero && sp.align == 0 && sp.fill == ' ' {
		return zeroPad(core, sp.width), nil
	}
	align := sp.align
	if align == 0 {
		if numeric {
			align = '>'
		} else {
			align = '<'
		}
	}
	return padAlign(core, sp.width, sp.fill, align), nil
}

func runeLen(s string) int { return len([]rune(s)) }

func truncate(s string, prec int) string {
	if prec < 0 {
		return s
	}
	if r := []rune(s); prec < len(r) {
		return string(r[:prec])
	}
	return s
}

// zeroPad inserts zeros after a leading sign, so "-7" padded to width 4 is "-007".
func zeroPad(s string, width int) string {
	n := width - runeLen(s)
	if n <= 0 {
		return s
	}
	if len(s) > 0 && (s[0] == '-' || s[0] == '+' || s[0] == ' ') {
		return s[:1] + strings.Repeat("0", n) + s[1:]
	}
	return strings.Repeat("0", n) + s
}

func padAlign(s string, width int, fill rune, align byte) string {
	n := width - runeLen(s)
	if n <= 0 {
		return s
	}
	f := string(fill)
	switch align {
	case '<':
		return s + strings.Repeat(f, n)
	case '^':
		left := n / 2
		return strings.Repeat(f, left) + s + strings.Repeat(f, n-left)
	default: // '>'
		return strings.Repeat(f, n) + s
	}
}
