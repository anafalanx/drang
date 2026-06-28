package lexer

import (
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
