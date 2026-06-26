// Package token defines the lexical tokens of drang.
package token

import "strconv"

// Kind is the category of a lexical token.
type Kind int

const (
	ILLEGAL Kind = iota
	EOF
	NEWLINE // statement terminator: inserted at significant newlines; ';' also terminates

	IDENT  // bare identifier: say, foo, build (builtins & function names)
	VAR    // $name — the single sigil; Lit holds the name without '$'
	INT    // 123
	FLOAT  // 1.5
	STRING // "..."  and qq{...} / interpolating heredocs (parser interpolates)
	RAWSTR // q{...} and '...'-style heredocs: literal, no interpolation
	QW     // qw{...}: whitespace-split word list (parser builds an array)
	QR     // qr{...}: compiled-regex literal (Lit = pattern, inline flags baked in)

	// keywords
	FN
	RETURN
	IF
	ELSE
	UNLESS
	FOR
	IN
	WHILE
	UNTIL
	BREAK
	NEXT
	TRUE
	FALSE
	OR  // boolean or  (recovery uses //)
	AND // boolean and
	NOT // boolean not

	// operators & punctuation
	ASSIGN    // =
	COLONEQ   // :=  mutable declaration
	CONSTDECL // ::= constant declaration
	PLUS      // +
	MINUS     // -
	STAR      // *
	SLASH     // /
	PERCENT   // %
	TILDE     // ~  string concat
	QUESTION  // ?  error propagate
	BANG      // !  logical not
	DOT       // .
	COMMA     // ,
	COLON     // :
	SEMI      // ;
	PIPE      // |> pipeline
	BAR       // |   lambda delimiter
	DOTDOT    // ..  range
	DEFOR     // //  defined-or
	PLUSEQ    // +=  compound assign
	MINUSEQ   // -=
	STAREQ    // *=
	SLASHEQ   // /=

	EQ        // ==
	NE        // !=
	LT        // <
	LE        // <=
	GT        // >
	GE        // >=
	SPACESHIP // <=> three-way compare

	LPAREN   // (
	RPAREN   // )
	LBRACE   // {
	RBRACE   // }
	LBRACKET // [
	RBRACKET // ]
)

var names = [...]string{
	ILLEGAL:   "ILLEGAL",
	EOF:       "EOF",
	NEWLINE:   "NEWLINE",
	IDENT:     "IDENT",
	VAR:       "VAR",
	INT:       "INT",
	FLOAT:     "FLOAT",
	STRING:    "STRING",
	RAWSTR:    "RAWSTR",
	QW:        "QW",
	QR:        "QR",
	FN:        "FN",
	RETURN:    "RETURN",
	IF:        "IF",
	ELSE:      "ELSE",
	UNLESS:    "UNLESS",
	FOR:       "FOR",
	IN:        "IN",
	WHILE:     "WHILE",
	UNTIL:     "UNTIL",
	BREAK:     "BREAK",
	NEXT:      "NEXT",
	TRUE:      "TRUE",
	FALSE:     "FALSE",
	OR:        "OR",
	AND:       "AND",
	NOT:       "NOT",
	ASSIGN:    "ASSIGN",
	COLONEQ:   "COLONEQ",
	CONSTDECL: "CONSTDECL",
	PLUS:      "PLUS",
	MINUS:     "MINUS",
	STAR:      "STAR",
	SLASH:     "SLASH",
	PERCENT:   "PERCENT",
	TILDE:     "TILDE",
	QUESTION:  "QUESTION",
	BANG:      "BANG",
	DOT:       "DOT",
	COMMA:     "COMMA",
	COLON:     "COLON",
	SEMI:      "SEMI",
	PIPE:      "PIPE",
	BAR:       "BAR",
	DOTDOT:    "DOTDOT",
	DEFOR:     "DEFOR",
	PLUSEQ:    "PLUSEQ",
	MINUSEQ:   "MINUSEQ",
	STAREQ:    "STAREQ",
	SLASHEQ:   "SLASHEQ",
	EQ:        "EQ",
	NE:        "NE",
	LT:        "LT",
	LE:        "LE",
	GT:        "GT",
	GE:        "GE",
	SPACESHIP: "SPACESHIP",
	LPAREN:    "LPAREN",
	RPAREN:    "RPAREN",
	LBRACE:    "LBRACE",
	RBRACE:    "RBRACE",
	LBRACKET:  "LBRACKET",
	RBRACKET:  "RBRACKET",
}

// String returns the token kind's name (for debugging and token dumps).
func (k Kind) String() string {
	if int(k) >= 0 && int(k) < len(names) && names[k] != "" {
		return names[k]
	}
	return "Kind(" + strconv.Itoa(int(k)) + ")"
}

// Token is a single lexical token with its source position.
type Token struct {
	Kind Kind
	Lit  string // literal text: the string value for STRING, the name for VAR/IDENT
	Line int
	Col  int
}

var keywords = map[string]Kind{
	"fn":     FN,
	"return": RETURN,
	"if":     IF,
	"else":   ELSE,
	"unless": UNLESS,
	"for":    FOR,
	"in":     IN,
	"while":  WHILE,
	"until":  UNTIL,
	"break":  BREAK,
	"next":   NEXT,
	"true":   TRUE,
	"false":  FALSE,
	"or":     OR,
	"and":    AND,
	"not":    NOT,
}

// Lookup maps an identifier to its keyword Kind, or IDENT if it is not a keyword.
func Lookup(ident string) Kind {
	if k, ok := keywords[ident]; ok {
		return k
	}
	return IDENT
}
