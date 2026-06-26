// Package lexer turns lang3 source text into a token stream.
//
// It performs Go-style automatic terminator insertion: a newline yields a
// NEWLINE token only when the previous token could end a statement (a literal,
// $var, ident, ')', '}', ']', or '?') and we are not inside '(' or '[' (so long
// expressions and |> pipelines wrap across lines freely). ';' also terminates.
//
// Supported: string interpolation ("$x"/"${expr}"); the q/qq/qw quote operators
// (raw / interpolated / word-list, with ( [ { / | delimiters); and heredocs
// (<<TAG, <<"TAG", <<'TAG', <<~TAG). Deliberately deferred (TODO): regex literals
// (a dedicated /.../ or qr// — the existing regex builtins + raw q// patterns and
// lenient string escapes cover the need without a new compiled-regex value type).
package lexer

import (
	"strings"

	"github.com/anafalanx/lang3/internal/token"
)

// Lexer scans source text one token at a time.
type Lexer struct {
	src          string
	pos          int        // offset of l.ch within src
	rd           int        // offset of the next byte to read
	ch           byte       // current byte; 0 means end of input
	line         int
	col          int
	lastKind token.Kind   // kind of the previous emitted token (for terminator insertion)
	brackets []token.Kind // open-bracket stack: a newline terminates only when the innermost is '{' (or none)
}

// New returns a Lexer positioned at the first token of src.
func New(src string) *Lexer {
	l := &Lexer{src: src, line: 1, lastKind: token.ILLEGAL}
	l.advance()
	return l
}

func (l *Lexer) advance() {
	if l.rd >= len(l.src) {
		l.pos = len(l.src)
		l.ch = 0
		return
	}
	l.ch = l.src[l.rd]
	l.pos = l.rd
	l.rd++
	if l.ch == '\n' {
		l.line++
		l.col = 0
	} else {
		l.col++
	}
}

func (l *Lexer) peek() byte {
	if l.rd >= len(l.src) {
		return 0
	}
	return l.src[l.rd]
}

func (l *Lexer) peek2() byte {
	if l.rd+1 >= len(l.src) {
		return 0
	}
	return l.src[l.rd+1]
}

// Next returns the next token, inserting NEWLINE terminators where appropriate.
func (l *Lexer) Next() token.Token {
	sawNL := l.skipTrivia()
	if sawNL && l.newlinesSignificant() && terminates(l.lastKind) {
		l.lastKind = token.NEWLINE
		return token.Token{Kind: token.NEWLINE, Line: l.line, Col: l.col}
	}
	tok := l.scan()
	switch tok.Kind {
	case token.LPAREN, token.LBRACKET, token.LBRACE:
		l.brackets = append(l.brackets, tok.Kind)
	case token.RPAREN, token.RBRACKET, token.RBRACE:
		if len(l.brackets) > 0 {
			l.brackets = l.brackets[:len(l.brackets)-1]
		}
	}
	l.lastKind = tok.Kind
	return tok
}

// newlinesSignificant reports whether a newline can terminate a statement at the
// current nesting: true at top level or inside a '{' block, false inside '(' or
// '[' so long expressions and |> pipelines wrap across lines freely.
func (l *Lexer) newlinesSignificant() bool {
	if len(l.brackets) == 0 {
		return true
	}
	return l.brackets[len(l.brackets)-1] == token.LBRACE
}

// scan produces the next real token, assuming trivia has been skipped.
func (l *Lexer) scan() token.Token {
	line, col := l.line, l.col

	switch {
	case l.ch == 0:
		return token.Token{Kind: token.EOF, Line: line, Col: col}
	case isLetter(l.ch):
		id := l.readIdent()
		// q/qq/qw quote operators: only when the keyword is immediately followed by
		// a delimiter (no space), so `qw` as a plain identifier is unaffected.
		if isQuoteOp(id) {
			if _, ok := quoteOpener(l.ch); ok {
				return l.readQuoteLike(id, line, col)
			}
		}
		return token.Token{Kind: token.Lookup(id), Lit: id, Line: line, Col: col}
	case l.ch == '$':
		l.advance()
		if !isLetter(l.ch) {
			return token.Token{Kind: token.ILLEGAL, Lit: "$", Line: line, Col: col}
		}
		name := l.readIdent()
		return token.Token{Kind: token.VAR, Lit: name, Line: line, Col: col}
	case isDigit(l.ch):
		return l.readNumber(line, col)
	case l.ch == '"':
		return l.readString(line, col)
	}

	mk := func(k token.Kind, lit string) token.Token {
		return token.Token{Kind: k, Lit: lit, Line: line, Col: col}
	}
	switch l.ch {
	case '=':
		if l.peek() == '=' {
			l.advance()
			l.advance()
			return mk(token.EQ, "==")
		}
		l.advance()
		return mk(token.ASSIGN, "=")
	case '!':
		if l.peek() == '=' {
			l.advance()
			l.advance()
			return mk(token.NE, "!=")
		}
		l.advance()
		return mk(token.BANG, "!")
	case '<':
		if l.peek() == '=' {
			l.advance() // at '='
			l.advance() // past '='
			if l.ch == '>' {
				l.advance()
				return mk(token.SPACESHIP, "<=>")
			}
			return mk(token.LE, "<=")
		}
		if l.peek() == '<' {
			return l.readHeredoc(line, col) // <<TAG, <<'TAG', <<~TAG
		}
		l.advance()
		return mk(token.LT, "<")
	case '>':
		if l.peek() == '=' {
			l.advance()
			l.advance()
			return mk(token.GE, ">=")
		}
		l.advance()
		return mk(token.GT, ">")
	case '|':
		if l.peek() == '>' {
			l.advance()
			l.advance()
			return mk(token.PIPE, "|>")
		}
		l.advance()
		return mk(token.BAR, "|")
	case '+':
		if l.peek() == '=' {
			l.advance()
			l.advance()
			return mk(token.PLUSEQ, "+=")
		}
		l.advance()
		return mk(token.PLUS, "+")
	case '-':
		if l.peek() == '=' {
			l.advance()
			l.advance()
			return mk(token.MINUSEQ, "-=")
		}
		l.advance()
		return mk(token.MINUS, "-")
	case '*':
		if l.peek() == '=' {
			l.advance()
			l.advance()
			return mk(token.STAREQ, "*=")
		}
		l.advance()
		return mk(token.STAR, "*")
	case '/':
		if l.peek() == '/' {
			l.advance()
			l.advance()
			return mk(token.DEFOR, "//")
		}
		if l.peek() == '=' {
			l.advance()
			l.advance()
			return mk(token.SLASHEQ, "/=")
		}
		l.advance()
		return mk(token.SLASH, "/")
	case '%':
		l.advance()
		return mk(token.PERCENT, "%")
	case '~':
		l.advance()
		return mk(token.TILDE, "~")
	case '?':
		l.advance()
		return mk(token.QUESTION, "?")
	case '.':
		if l.peek() == '.' {
			l.advance()
			l.advance()
			return mk(token.DOTDOT, "..")
		}
		l.advance()
		return mk(token.DOT, ".")
	case ',':
		l.advance()
		return mk(token.COMMA, ",")
	case ':':
		if l.peek() == ':' && l.peek2() == '=' {
			l.advance()
			l.advance()
			l.advance()
			return mk(token.CONSTDECL, "::=")
		}
		if l.peek() == '=' {
			l.advance()
			l.advance()
			return mk(token.COLONEQ, ":=")
		}
		l.advance()
		return mk(token.COLON, ":")
	case ';':
		l.advance()
		return mk(token.SEMI, ";")
	case '(':
		l.advance()
		return mk(token.LPAREN, "(")
	case ')':
		l.advance()
		return mk(token.RPAREN, ")")
	case '{':
		l.advance()
		return mk(token.LBRACE, "{")
	case '}':
		l.advance()
		return mk(token.RBRACE, "}")
	case '[':
		l.advance()
		return mk(token.LBRACKET, "[")
	case ']':
		l.advance()
		return mk(token.RBRACKET, "]")
	}

	bad := l.ch
	l.advance()
	return mk(token.ILLEGAL, string(bad))
}

// skipTrivia consumes spaces, tabs, CRs, '#' comments, and newlines, returning
// whether at least one newline was seen.
func (l *Lexer) skipTrivia() bool {
	sawNL := false
	for {
		switch l.ch {
		case ' ', '\t', '\r':
			l.advance()
		case '\n':
			sawNL = true
			l.advance()
		case '#':
			for l.ch != '\n' && l.ch != 0 {
				l.advance()
			}
		default:
			return sawNL
		}
	}
}

// readHeredoc reads a heredoc introduced by <<. Forms: <<TAG and <<"TAG"
// interpolate (like qq/"..."); <<'TAG' is literal; a leading ~ (<<~TAG) strips the
// common leading indentation of the body. The <<TAG must be the last thing on its
// line; the body runs on the following lines up to a line equal to TAG (TAG may be
// indented for <<~). Each body line carries a trailing newline (so a non-empty body
// ends in "\n"); a body with no lines is "" (matching Perl/Ruby). The result is a
// STRING (the parser interpolates it) or a RAWSTR.
func (l *Lexer) readHeredoc(line, col int) token.Token {
	bad := func(msg string) token.Token {
		return token.Token{Kind: token.ILLEGAL, Lit: msg, Line: line, Col: col}
	}
	l.advance() // first '<'
	l.advance() // second '<'
	dedent := false
	if l.ch == '~' {
		dedent = true
		l.advance()
	}
	raw := false
	var tag string
	switch {
	case l.ch == '"':
		l.advance()
		tag = l.readUntilByte('"')
		if l.ch != '"' {
			return bad("unterminated heredoc tag")
		}
		l.advance()
	case l.ch == '\'':
		l.advance()
		tag = l.readUntilByte('\'')
		if l.ch != '\'' {
			return bad("unterminated heredoc tag")
		}
		l.advance()
		raw = true
	case isLetter(l.ch):
		tag = l.readIdent()
	default:
		return bad("expected a heredoc tag after <<")
	}
	if tag == "" {
		return bad("empty heredoc tag")
	}
	// The opener must be the last thing on its line.
	for l.ch == ' ' || l.ch == '\t' || l.ch == '\r' {
		l.advance()
	}
	if l.ch != '\n' && l.ch != 0 {
		return bad("heredoc <<" + tag + " must be the last thing on its line")
	}
	if l.ch == '\n' {
		l.advance() // first body line
	}
	var lines []string
	terminated := false
	for l.ch != 0 {
		text := l.readLineRaw() // leaves l.ch at the '\n' (or 0)
		match := text
		if dedent {
			match = strings.TrimLeft(text, " \t")
		}
		if match == tag {
			terminated = true // leave l.ch at the terminator's '\n' -> next scan emits NEWLINE
			break
		}
		lines = append(lines, text)
		if l.ch == '\n' {
			l.advance()
		}
	}
	if !terminated {
		return bad("unterminated heredoc <<" + tag)
	}
	body := assembleHeredoc(lines, dedent)
	if raw {
		return token.Token{Kind: token.RAWSTR, Lit: body, Line: line, Col: col}
	}
	return token.Token{Kind: token.STRING, Lit: body, Line: line, Col: col}
}

// readUntilByte reads up to (not including) end or a newline/EOF.
func (l *Lexer) readUntilByte(end byte) string {
	var b strings.Builder
	for l.ch != 0 && l.ch != end && l.ch != '\n' {
		b.WriteByte(l.ch)
		l.advance()
	}
	return b.String()
}

// readLineRaw reads to (not including) the next newline or EOF, dropping a trailing
// carriage return so CRLF sources match heredoc terminators. l.ch is left at '\n'/0.
func (l *Lexer) readLineRaw() string {
	var b strings.Builder
	for l.ch != 0 && l.ch != '\n' {
		b.WriteByte(l.ch)
		l.advance()
	}
	return strings.TrimRight(b.String(), "\r")
}

// assembleHeredoc joins body lines, each terminated by a newline (so a non-empty
// body ends in "\n"; no body lines yields ""). For <<~ it strips the smallest
// leading indentation shared by the non-blank lines.
func assembleHeredoc(lines []string, dedent bool) string {
	if dedent {
		indent := -1
		for _, ln := range lines {
			t := strings.TrimLeft(ln, " \t")
			if t == "" {
				continue // blank lines don't constrain the indent
			}
			if lead := len(ln) - len(t); indent < 0 || lead < indent {
				indent = lead
			}
		}
		if indent > 0 {
			for i, ln := range lines {
				if len(ln) >= indent {
					lines[i] = ln[indent:]
				} else {
					lines[i] = strings.TrimLeft(ln, " \t")
				}
			}
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// terminates reports whether a token of kind k can end a statement, and thus
// whether a following newline should be turned into a NEWLINE terminator.
func terminates(k token.Kind) bool {
	switch k {
	case token.IDENT, token.VAR, token.INT, token.FLOAT, token.STRING, token.RAWSTR, token.QW, token.QR,
		token.TRUE, token.FALSE, token.RETURN, token.BREAK, token.NEXT,
		token.RPAREN, token.RBRACE, token.RBRACKET, token.QUESTION:
		return true
	}
	return false
}

// isQuoteOp reports whether id is a q/qq/qw/qr quote operator keyword.
func isQuoteOp(id string) bool {
	return id == "q" || id == "qq" || id == "qw" || id == "qr"
}

// readRegexFlags reads trailing regex flag letters after qr/.../ — i (case
// insensitive), m (multi-line ^$), s (dotall), U (ungreedy). ok=false on an
// unknown flag letter.
func (l *Lexer) readRegexFlags() (string, bool) {
	var b strings.Builder
	for isLetter(l.ch) {
		switch l.ch {
		case 'i', 'm', 's', 'U':
			b.WriteByte(l.ch)
		default:
			return "", false
		}
		l.advance()
	}
	return b.String(), true
}

// quoteOpener reports whether c can open a quote body and returns the matching
// closing delimiter (the same char for non-paired delimiters).
func quoteOpener(c byte) (close byte, ok bool) {
	switch c {
	case '(':
		return ')', true
	case '[':
		return ']', true
	case '{':
		return '}', true
	case '/':
		return '/', true
	case '|':
		return '|', true
	}
	return 0, false
}

// readQuoteLike reads a q/qq/qw body delimited by the char at l.ch. Paired
// delimiters ( [ { nest; same-char delimiters / | run to the next occurrence. The
// body is taken literally (no backslash escaping of the delimiter) — choose a
// delimiter the content avoids, or use a nesting paired one. qq bodies are
// interpolated by the parser like "..."; q is literal; qw is split into words;
// qr is a literal regex pattern (with trailing flags baked in as Go inline flags).
//
// Nesting counts raw delimiter bytes only; it is NOT aware of strings or ${...}
// inside a qq body, so a closing brace inside an interpolation (qq{ ${ "}" } })
// needs a non-brace delimiter (qq[ ${ "}" } ] works).
func (l *Lexer) readQuoteLike(id string, line, col int) token.Token {
	open := l.ch
	closeCh, _ := quoteOpener(open) // caller verified it opens
	paired := open != closeCh
	l.advance() // past the opener
	var b strings.Builder
	depth := 1
	for l.ch != 0 {
		if paired && l.ch == open {
			depth++
		} else if l.ch == closeCh {
			if depth--; depth == 0 {
				break
			}
		}
		b.WriteByte(l.ch)
		l.advance()
	}
	if l.ch == 0 {
		return token.Token{Kind: token.ILLEGAL, Lit: "unterminated " + id + "// quote", Line: line, Col: col}
	}
	l.advance() // past the closer
	content := b.String()
	switch id {
	case "qw":
		return token.Token{Kind: token.QW, Lit: content, Line: line, Col: col}
	case "q":
		return token.Token{Kind: token.RAWSTR, Lit: content, Line: line, Col: col}
	case "qr":
		flags, ok := l.readRegexFlags()
		if !ok {
			return token.Token{Kind: token.ILLEGAL, Lit: "invalid regex flag after qr//", Line: line, Col: col}
		}
		if flags != "" {
			content = "(?" + flags + ")" + content // bake flags as Go inline flags
		}
		return token.Token{Kind: token.QR, Lit: content, Line: line, Col: col}
	default: // qq
		return token.Token{Kind: token.STRING, Lit: content, Line: line, Col: col}
	}
}

func (l *Lexer) readIdent() string {
	start := l.pos
	for isLetter(l.ch) || isDigit(l.ch) {
		l.advance()
	}
	return l.src[start:l.pos]
}

func (l *Lexer) readNumber(line, col int) token.Token {
	start := l.pos
	for isDigit(l.ch) {
		l.advance()
	}
	kind := token.INT
	if l.ch == '.' && isDigit(l.peek()) {
		kind = token.FLOAT
		l.advance()
		for isDigit(l.ch) {
			l.advance()
		}
	}
	return token.Token{Kind: kind, Lit: l.src[start:l.pos], Line: line, Col: col}
}

// readString captures the RAW string body (backslashes intact, just tracking
// escapes enough to find the real closing quote). Escape processing AND $-string
// interpolation are done together in the parser, which needs the raw form to tell
// a literal "\$" from an interpolated "$x".
func (l *Lexer) readString(line, col int) token.Token {
	l.advance() // consume opening quote
	var b strings.Builder
	for l.ch != '"' && l.ch != 0 {
		if l.ch == '\\' {
			b.WriteByte('\\')
			l.advance()
			if l.ch == 0 {
				return token.Token{Kind: token.ILLEGAL, Lit: "unterminated string", Line: line, Col: col}
			}
			b.WriteByte(l.ch) // keep the escaped char raw; the parser decodes it
			l.advance()
			continue
		}
		b.WriteByte(l.ch)
		l.advance()
	}
	if l.ch == 0 {
		return token.Token{Kind: token.ILLEGAL, Lit: "unterminated string", Line: line, Col: col}
	}
	l.advance() // consume closing quote
	return token.Token{Kind: token.STRING, Lit: b.String(), Line: line, Col: col}
}

func isLetter(ch byte) bool {
	return ch == '_' || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')
}

func isDigit(ch byte) bool {
	return ch >= '0' && ch <= '9'
}
