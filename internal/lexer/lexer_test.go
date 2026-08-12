package lexer

import (
	"strings"
	"testing"

	"github.com/anafalanx/drang/internal/token"
)

func drain(l *Lexer) []token.Token {
	var toks []token.Token
	for {
		tk := l.Next()
		toks = append(toks, tk)
		if tk.Kind == token.EOF {
			return toks
		}
	}
}

func TestCommentSideTableHasExplicitCeiling(t *testing.T) {
	old := maxLexerComments
	maxLexerComments = 2
	t.Cleanup(func() { maxLexerComments = old })

	l := New("# one\n# two\n# three\n$x := 1\n")
	toks := drain(l)
	if len(l.Comments()) != 2 {
		t.Fatalf("captured %d comments, want ceiling 2", len(l.Comments()))
	}
	if len(toks) == 0 || toks[0].Kind != token.ILLEGAL || !strings.Contains(toks[0].Lit, "2-comment limit") {
		t.Fatalf("first token = %#v, want comment-limit error", toks)
	}
	if toks[0].Line != 3 || toks[0].Col != 1 {
		t.Fatalf("overflow position = %d:%d, want 3:1", toks[0].Line, toks[0].Col)
	}
}

func TestCommentsCaptured(t *testing.T) {
	src := "# leading\n$x := 1  # trailing\n# another\n"
	l := New(src)
	drain(l)
	cs := l.Comments()
	if len(cs) != 3 {
		t.Fatalf("got %d comments, want 3: %+v", len(cs), cs)
	}
	want := []Comment{
		{Text: "# leading", Line: 1},
		{Text: "# trailing", Line: 2},
		{Text: "# another", Line: 3},
	}
	for i, w := range want {
		if cs[i].Text != w.Text || cs[i].Line != w.Line {
			t.Errorf("comment %d = %+v, want text %q line %d", i, cs[i], w.Text, w.Line)
		}
	}
}

func TestCommentsAreTriviaNotTokens(t *testing.T) {
	// A comment must not change the token stream: these two sources tokenize identically.
	withC := drain(New("$x := 1  # a comment\nsay($x)\n"))
	noC := drain(New("$x := 1\nsay($x)\n"))
	if len(withC) != len(noC) {
		t.Fatalf("comment changed token count: %d vs %d", len(withC), len(noC))
	}
	for i := range withC {
		if withC[i].Kind != noC[i].Kind || withC[i].Lit != noC[i].Lit {
			t.Errorf("token %d differs: %v %q vs %v %q", i, withC[i].Kind, withC[i].Lit, noC[i].Kind, noC[i].Lit)
		}
	}
}

func TestDelimiterStackHasExplicitCeiling(t *testing.T) {
	toks := drain(New(strings.Repeat("[", maxLexerBrackets+1)))
	found := false
	for _, tok := range toks {
		if tok.Kind == token.ILLEGAL && strings.Contains(tok.Lit, "delimiter nesting") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("tokens lack delimiter nesting diagnostic: %#v", toks[len(toks)-3:])
	}
	if len(toks) > maxLexerBrackets+3 {
		t.Fatalf("overflow returned %d tokens, want at most cap + diagnostic + EOF", len(toks))
	}
	l := New(strings.Repeat("[", maxLexerBrackets+100_000))
	for i := 0; i <= maxLexerBrackets; i++ {
		_ = l.Next()
	}
	if len(l.brackets) != maxLexerBrackets {
		t.Fatalf("bracket stack grew to %d, want cap %d", len(l.brackets), maxLexerBrackets)
	}
	if tok := l.Next(); tok.Kind != token.EOF {
		t.Fatalf("token after overflow = %#v, want terminal EOF", tok)
	}
}

func TestHeredocBodyCoordinatesAndDedent(t *testing.T) {
	l := New("<<~$TXT\n    one\n    two\nTXT\n")
	tok := l.Next()
	if tok.Kind != token.ISTRING || tok.Lit != "one\ntwo\n" {
		t.Fatalf("token = %#v", tok)
	}
	if tok.BodyLine != 2 || tok.BodyCol != 5 || tok.BodyNext != 5 {
		t.Fatalf("body position = %d:%d next=%d, want 2:5 next=5", tok.BodyLine, tok.BodyCol, tok.BodyNext)
	}
}
