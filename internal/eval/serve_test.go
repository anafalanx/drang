package eval

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anafalanx/drang/internal/value"
)

func TestRemoveBrowserProfileWaitsForStableAbsence(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "profile")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := time.Now().Add(350 * time.Millisecond)
		for time.Now().Before(deadline) {
			_ = os.MkdirAll(dir, 0o755)
			_ = os.WriteFile(filepath.Join(dir, "late"), []byte("x"), 0o600)
			time.Sleep(20 * time.Millisecond)
		}
	}()
	if err := removeBrowserProfile(dir); err != nil {
		t.Fatal(err)
	}
	<-done
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("profile still exists after cleanup: %v", err)
	}
}

// The token gate is the security boundary: 127.0.0.1 excludes remote clients, the
// token excludes other local processes. Verify all four paths.
func TestServeAuthorizeTokenGate(t *testing.T) {
	g := &guiServer{token: "secret-token"}

	// no token -> denied
	r := httptest.NewRequest("GET", "/", nil)
	if g.authorize(httptest.NewRecorder(), r) {
		t.Error("request without a token must be denied")
	}

	// query token -> allowed, and issues the cookie
	r = httptest.NewRequest("GET", "/?t=secret-token", nil)
	w := httptest.NewRecorder()
	if !g.authorize(w, r) {
		t.Fatal("valid query token must be allowed")
	}
	var cookie string
	for _, c := range w.Result().Cookies() {
		if c.Name == "drang_token" {
			cookie = c.Value
		}
	}
	if cookie != "secret-token" {
		t.Errorf("expected drang_token cookie to be set, got %q", cookie)
	}

	// cookie token -> allowed (every subsequent htmx request)
	r = httptest.NewRequest("GET", "/frag", nil)
	r.AddCookie(&http.Cookie{Name: "drang_token", Value: "secret-token"})
	if !g.authorize(httptest.NewRecorder(), r) {
		t.Error("valid cookie token must be allowed")
	}

	// wrong token -> denied
	r = httptest.NewRequest("GET", "/?t=nope", nil)
	if g.authorize(httptest.NewRecorder(), r) {
		t.Error("wrong token must be denied")
	}
}

func TestServeHTMXServed(t *testing.T) {
	w := httptest.NewRecorder()
	(&guiServer{}).serveHTMX(w)
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("htmx content-type got %q, want javascript", ct)
	}
	if w.Body.Len() == 0 || w.Body.Len() != len(htmxJS) {
		t.Errorf("htmx body length got %d, want %d", w.Body.Len(), len(htmxJS))
	}
}

func TestMemFileServer(t *testing.T) {
	m := memFileServer{
		"index.html":  []byte("<h1>home</h1>"),
		"css/app.css": []byte(".x{}"),
	}
	// "/" maps to index.html
	w := httptest.NewRecorder()
	m.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "home") {
		t.Errorf("/ got code=%d body=%q", w.Code, w.Body.String())
	}
	// css gets the right content type from its extension
	w = httptest.NewRecorder()
	m.ServeHTTP(w, httptest.NewRequest("GET", "/css/app.css", nil))
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "css") {
		t.Errorf("css content-type got %q, want css", ct)
	}
	// a missing asset is a 404
	w = httptest.NewRecorder()
	m.ServeHTTP(w, httptest.NewRequest("GET", "/missing", nil))
	if w.Code != 404 {
		t.Errorf("missing asset got code %d, want 404", w.Code)
	}
}

func TestServeWriteResult(t *testing.T) {
	// a string handler result -> 200 text/html
	w := httptest.NewRecorder()
	writeResult(w, value.MakeStr("<b>hi</b>"))
	if w.Code != 200 || w.Body.String() != "<b>hi</b>" {
		t.Errorf("string result got code=%d body=%q", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "html") {
		t.Errorf("string result content-type got %q, want html", ct)
	}

	// a {status, body} map -> that status + body
	m := value.MakeMap()
	om := m.Obj().(*value.OrderedMap)
	om.Set(value.MakeStr("status"), value.MakeInt(404))
	om.Set(value.MakeStr("body"), value.MakeStr("nope"))
	w = httptest.NewRecorder()
	writeResult(w, m)
	if w.Code != 404 || w.Body.String() != "nope" {
		t.Errorf("map result got code=%d body=%q", w.Code, w.Body.String())
	}

	// an Err result -> 500
	w = httptest.NewRecorder()
	writeResult(w, value.MakeErr("boom", 1))
	if w.Code != 500 {
		t.Errorf("err result got code %d, want 500", w.Code)
	}
}
