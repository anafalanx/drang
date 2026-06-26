// Package lexer turns lang3 source text into a token stream.
//
// It performs Go-style automatic terminator insertion: a newline yields a
// NEWLINE token only when the previous token could end a statement (a literal,
// $var, ident, ')', '}', ']', or '?') and we are not inside '(' or '[' (so long
// expressions and |> pipelines wrap across lines freely). ';' also terminates.
//
// String interpolation ("$x"/"${expr}") is supported: readString returns the raw
// body and the parser decodes escapes + interpolation together. Deliberately
// deferred (TODO): regex literals, qw// and q//qq// quote operators, heredocs.
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

// terminates reports whether a token of kind k can end a statement, and thus
// whether a following newline should be turned into a NEWLINE terminator.
func terminates(k token.Kind) bool {
	switch k {
	case token.IDENT, token.VAR, token.INT, token.FLOAT, token.STRING,
		token.TRUE, token.FALSE, token.RETURN, token.BREAK, token.NEXT,
		token.RPAREN, token.RBRACE, token.RBRACKET, token.QUESTION:
		return true
	}
	return false
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
