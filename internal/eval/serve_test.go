package eval

import (
	"errors"
	"io"
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

func TestServeScrubsBootstrapTokenFromURL(t *testing.T) {
	g := &guiServer{token: "secret-token", fileServer: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "served")
	})}
	r := httptest.NewRequest(http.MethodGet, "/page?q=kept&t=secret-token", nil)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("bootstrap request got status %d, want redirect", w.Code)
	}
	if location := w.Header().Get("Location"); location != "/page?q=kept" {
		t.Fatalf("bootstrap redirect location = %q, want token-free URL", location)
	}
	if len(w.Result().Cookies()) == 0 || w.Result().Cookies()[0].Value != "secret-token" {
		t.Fatal("bootstrap redirect did not issue the authentication cookie")
	}
	if strings.Contains(w.Body.String(), "served") {
		t.Fatal("bootstrap request was served before its token was scrubbed")
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
	if err := writeResult(w, value.MakeErr("boom", 1)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	if w.Code != 500 {
		t.Errorf("err result got code %d, want 500", w.Code)
	}
}

func TestBuildGUIServerRejectsEntropyFailure(t *testing.T) {
	oldReadRandom := readRandom
	readRandom = func([]byte) (int, error) { return 0, errors.New("entropy unavailable") }
	defer func() { readRandom = oldReadRandom }()

	opts := mkMap(value.MakeStr("routes"), value.MakeMap())
	g, errv := buildGUIServer(opts.Obj().(*value.OrderedMap))
	if g != nil || !errv.IsErr() || !strings.Contains(errv.ErrMsg(), "entropy unavailable") {
		t.Fatalf("buildGUIServer = (%v, %s), want entropy Err", g, errv.Display())
	}
}

func TestBuildGUIServerRejectsShortEntropyRead(t *testing.T) {
	oldReadRandom := readRandom
	readRandom = func(p []byte) (int, error) { return len(p) - 1, nil }
	defer func() { readRandom = oldReadRandom }()

	opts := mkMap(value.MakeStr("routes"), value.MakeMap())
	g, errv := buildGUIServer(opts.Obj().(*value.OrderedMap))
	if g != nil || !errv.IsErr() || !strings.Contains(errv.ErrMsg(), "unexpected EOF") {
		t.Fatalf("buildGUIServer = (%v, %s), want short-read Err", g, errv.Display())
	}
}

func TestServeRejectsOversizedFormBeforeHandler(t *testing.T) {
	called := false
	fn := &Function{Builtin: func([]value.Value) (value.Value, error) {
		called = true
		return value.MakeStr("unexpected"), nil
	}}
	body := strings.NewReader("x=" + strings.Repeat("a", int(maxServeRequestBodyBytes)))
	r := httptest.NewRequest(http.MethodPost, "/", body)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	(&guiServer{}).runHandler(w, r, fn)

	if called {
		t.Fatal("handler ran after request body exceeded its limit")
	}
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized form got status %d, want %d", w.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestServeMalformedHandlerResponseIsControlled500(t *testing.T) {
	bad := value.MakeMap()
	bad.Obj().(*value.OrderedMap).Set(value.MakeStr("status"), value.MakeInt(999))
	fn := &Function{Builtin: func([]value.Value) (value.Value, error) { return bad, nil }}
	w := httptest.NewRecorder()
	(&guiServer{}).runHandler(w, httptest.NewRequest(http.MethodGet, "/", nil), fn)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("malformed response got status %d, want 500", w.Code)
	}
	if !strings.Contains(w.Body.String(), "response status") {
		t.Fatalf("malformed response body %q does not explain the validation failure", w.Body.String())
	}
}

func TestServeStaticRootCannotFollowEscapingSymlink(t *testing.T) {
	oldEmbeddedWeb := embeddedWeb
	embeddedWeb = nil
	defer func() { embeddedWeb = oldEmbeddedWeb }()

	base := t.TempDir()
	root := filepath.Join(base, "public")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(base, "secret.txt")
	if err := os.WriteFile(secret, []byte("must-not-escape"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "escape.txt")); err != nil {
		t.Skipf("symlinks unavailable on this Windows host: %v", err)
	}

	opts := mkMap(
		value.MakeStr("routes"), value.MakeMap(),
		value.MakeStr("static"), value.MakeStr(root),
		value.MakeStr("open"), value.MakeBool(false),
	)
	g, errv := buildGUIServer(opts.Obj().(*value.OrderedMap))
	if g == nil {
		t.Fatalf("buildGUIServer: %s", errv.Display())
	}
	defer g.staticRoot.Close()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/escape.txt", nil)
	r.AddCookie(&http.Cookie{Name: "drang_token", Value: g.token})
	g.ServeHTTP(w, r)
	if strings.Contains(w.Body.String(), "must-not-escape") {
		t.Fatal("static server followed a symlink outside its root")
	}
}

type failingHTTPWriter struct {
	header http.Header
	status int
}

func (w *failingHTTPWriter) Header() http.Header  { return w.header }
func (w *failingHTTPWriter) WriteHeader(code int) { w.status = code }
func (*failingHTTPWriter) Write([]byte) (int, error) {
	return 0, errors.New("connection failed")
}

func TestServeResultPropagatesResponseWriteFailure(t *testing.T) {
	w := &failingHTTPWriter{header: make(http.Header)}
	err := writeResult(w, value.MakeStr("body"))
	if err == nil || !strings.Contains(err.Error(), "connection failed") {
		t.Fatalf("writeResult error = %v, want connection failure", err)
	}

	m := value.MakeMap()
	m.Obj().(*value.OrderedMap).Set(value.MakeStr("body"), value.MakeStr("body"))
	w = &failingHTTPWriter{header: make(http.Header)}
	err = writeResult(w, m)
	if err == nil || !strings.Contains(err.Error(), "connection failed") {
		t.Fatalf("map writeResult error = %v, want connection failure", err)
	}
}
