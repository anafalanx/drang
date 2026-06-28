package eval

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anafalanx/drang/internal/value"
)

func httpTestServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "hi")
		io.WriteString(w, "hello")
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		io.WriteString(w, "ct="+r.Header.Get("Content-Type")+" body="+string(b))
	})
	mux.HandleFunc("/404", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		io.WriteString(w, "nope")
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		io.WriteString(w, "late")
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 1000))
	})
	mux.HandleFunc("/redir", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ok", 302)
	})
	mux.HandleFunc("/hdr", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.Header.Get("X-Custom"))
	})
	mux.HandleFunc("/dribble", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200) // headers arrive immediately...
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(500 * time.Millisecond) // ...then the body stalls past a short deadline
		io.WriteString(w, "late body")
	})
	return httptest.NewServer(mux)
}

func mkMap(kv ...value.Value) value.Value {
	m := value.MakeMap()
	om := m.Obj().(*value.OrderedMap)
	for i := 0; i+1 < len(kv); i += 2 {
		om.Set(kv[i], kv[i+1])
	}
	return m
}

func field(t *testing.T, v value.Value, key string) value.Value {
	t.Helper()
	if v.Tag() != value.Map {
		t.Fatalf("expected a response map, got %s: %s", v.TypeName(), v.Display())
	}
	got, ok := v.Obj().(*value.OrderedMap).Get(value.MakeStr(key))
	if !ok {
		t.Fatalf("response has no field %q: %s", key, v.Display())
	}
	return got
}

func errCode(r value.Value) int64 {
	c, _ := builtinErrCode([]value.Value{r})
	return c.AsInt()
}

func mustGet(t *testing.T, url string, opts ...value.Value) value.Value {
	t.Helper()
	args := []value.Value{value.MakeStr(url)}
	args = append(args, opts...)
	r, err := builtinHTTPGet(args)
	if err != nil {
		t.Fatalf("http_get arity error: %v", err)
	}
	return r
}

func TestHTTPGetBasic(t *testing.T) {
	srv := httpTestServer()
	defer srv.Close()
	r := mustGet(t, srv.URL+"/ok")
	if s := field(t, r, "status"); s.AsInt() != 200 {
		t.Errorf("status = %d, want 200", s.AsInt())
	}
	if !field(t, r, "ok").Truthy() {
		t.Errorf("ok should be true")
	}
	if b := field(t, r, "body"); b.AsStr() != "hello" {
		t.Errorf("body = %q, want hello", b.AsStr())
	}
	if h := field(t, field(t, r, "headers"), "x-test"); h.AsStr() != "hi" {
		t.Errorf("header x-test = %q, want hi", h.AsStr())
	}
	if u := field(t, r, "url"); !strings.HasSuffix(u.AsStr(), "/ok") {
		t.Errorf("url = %q, want .../ok", u.AsStr())
	}
}

func TestHTTP404IsData(t *testing.T) {
	srv := httpTestServer()
	defer srv.Close()
	r := mustGet(t, srv.URL+"/404")
	if r.Tag() == value.Err {
		t.Fatalf("a 404 must be data, not an Err: %s", r.Display())
	}
	if field(t, r, "status").AsInt() != 404 || field(t, r, "ok").Truthy() {
		t.Errorf("want status 404 ok=false, got %s", r.Display())
	}
	if field(t, r, "body").AsStr() != "nope" {
		t.Errorf("body = %q", field(t, r, "body").AsStr())
	}
}

func TestHTTPPostBodyAndJSON(t *testing.T) {
	srv := httpTestServer()
	defer srv.Close()
	// http_post with a string body
	r, _ := builtinHTTPPost([]value.Value{value.MakeStr(srv.URL + "/echo"), value.MakeStr("payload")})
	if b := field(t, r, "body").AsStr(); !strings.Contains(b, "body=payload") {
		t.Errorf("post body not echoed: %q", b)
	}
	// http POST with a json opt (serialized + content-type set)
	jr, _ := builtinHTTP([]value.Value{
		value.MakeStr("POST"), value.MakeStr(srv.URL + "/echo"),
		mkMap(value.MakeStr("json"), mkMap(value.MakeStr("a"), value.MakeInt(1))),
	})
	b := field(t, jr, "body").AsStr()
	if !strings.Contains(b, "application/json") || !strings.Contains(b, `"a":1`) {
		t.Errorf("json post not sent correctly: %q", b)
	}
	// body + json together is an error
	er, _ := builtinHTTP([]value.Value{
		value.MakeStr("POST"), value.MakeStr(srv.URL + "/echo"),
		mkMap(value.MakeStr("body"), value.MakeStr("x"), value.MakeStr("json"), value.MakeInt(1)),
	})
	if er.Tag() != value.Err {
		t.Errorf("body+json should be an Err")
	}
}

func TestHTTPTimeoutIsCode124(t *testing.T) {
	srv := httpTestServer()
	defer srv.Close()
	r := mustGet(t, srv.URL+"/slow", mkMap(value.MakeStr("timeout"), value.MakeInt(50)))
	if r.Tag() != value.Err {
		t.Fatalf("a timeout must be an Err, got %s", r.Display())
	}
	if c := errCode(r); c != 124 {
		t.Errorf("timeout err_code = %d, want 124", c)
	}
}

func TestHTTPBodyReadTimeout(t *testing.T) {
	// A deadline firing during the body read must be an Err (code 124), not a partial body
	// returned as a successful 200 (the review's HIGH finding).
	srv := httpTestServer()
	defer srv.Close()
	r := mustGet(t, srv.URL+"/dribble", mkMap(value.MakeStr("timeout"), value.MakeInt(100)))
	if r.Tag() != value.Err {
		t.Fatalf("body-read deadline must be an Err, got %s", r.Display())
	}
	if c := errCode(r); c != 124 {
		t.Errorf("body-read timeout err_code = %d, want 124", c)
	}
}

func TestHTTPNegativeTimeout(t *testing.T) {
	srv := httpTestServer()
	defer srv.Close()
	r := mustGet(t, srv.URL+"/ok", mkMap(value.MakeStr("timeout"), value.MakeInt(-1)))
	if r.Tag() != value.Err {
		t.Errorf("a negative timeout must be a catchable Err, got %s", r.Display())
	}
}

func TestHTTPMaxBody(t *testing.T) {
	srv := httpTestServer()
	defer srv.Close()
	r := mustGet(t, srv.URL+"/big", mkMap(value.MakeStr("max_body"), value.MakeInt(100)))
	if r.Tag() != value.Err {
		t.Fatalf("oversized body must be an Err, got %s", r.Display())
	}
	// under the cap is fine
	ok := mustGet(t, srv.URL+"/big", mkMap(value.MakeStr("max_body"), value.MakeInt(10000)))
	if ok.Tag() == value.Err {
		t.Errorf("1000-byte body under a 10000 cap should succeed: %s", ok.Display())
	}
}

func TestHTTPRedirects(t *testing.T) {
	srv := httpTestServer()
	defer srv.Close()
	// default: follow -> 200 at /ok
	r := mustGet(t, srv.URL+"/redir")
	if field(t, r, "status").AsInt() != 200 || field(t, r, "body").AsStr() != "hello" {
		t.Errorf("redirect not followed: %s", r.Display())
	}
	if !strings.HasSuffix(field(t, r, "url").AsStr(), "/ok") {
		t.Errorf("final url should be /ok: %q", field(t, r, "url").AsStr())
	}
	// redirects:0 -> the 302 is returned as data
	nr := mustGet(t, srv.URL+"/redir", mkMap(value.MakeStr("redirects"), value.MakeInt(0)))
	if field(t, nr, "status").AsInt() != 302 {
		t.Errorf("redirects:0 should return the 302, got %s", nr.Display())
	}
}

func TestHTTPRequestHeaders(t *testing.T) {
	srv := httpTestServer()
	defer srv.Close()
	r := mustGet(t, srv.URL+"/hdr", mkMap(
		value.MakeStr("headers"), mkMap(value.MakeStr("X-Custom"), value.MakeStr("abc")),
	))
	if field(t, r, "body").AsStr() != "abc" {
		t.Errorf("custom header not sent: %q", field(t, r, "body").AsStr())
	}
}

func TestHTTPTransportErrorIsErr(t *testing.T) {
	// connection refused on a closed port -> catchable Err, not a crash or a value
	r := mustGet(t, "http://127.0.0.1:1/", mkMap(value.MakeStr("timeout"), value.MakeInt(2000)))
	if r.Tag() != value.Err {
		t.Errorf("connection refused must be an Err, got %s", r.Display())
	}
}

// TestHTTPConcurrent exercises the shared pooled transport from many goroutines (the
// pmap fan-out case); run under -race to confirm goroutine-safety.
func TestHTTPConcurrent(t *testing.T) {
	srv := httpTestServer()
	defer srv.Close()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := builtinHTTPGet([]value.Value{value.MakeStr(srv.URL + "/ok")})
			if err != nil || r.Tag() == value.Err {
				t.Errorf("concurrent get failed: %v / %s", err, r.Display())
				return
			}
			if s, _ := r.Obj().(*value.OrderedMap).Get(value.MakeStr("status")); s.AsInt() != 200 {
				t.Errorf("concurrent status = %d, want 200", s.AsInt())
			}
		}()
	}
	wg.Wait()
}

func TestHTTPTLSVerify(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "secure")
	}))
	defer srv.Close()
	// default: TLS verification on -> the test cert is untrusted -> Err
	r := mustGet(t, srv.URL)
	if r.Tag() != value.Err {
		t.Errorf("untrusted TLS cert must be an Err by default, got %s", r.Display())
	}
	// insecure:true -> succeeds
	ir := mustGet(t, srv.URL, mkMap(value.MakeStr("insecure"), value.MakeBool(true)))
	if ir.Tag() == value.Err {
		t.Errorf("insecure:true should skip verification: %s", ir.Display())
	} else if field(t, ir, "body").AsStr() != "secure" {
		t.Errorf("body = %q", field(t, ir, "body").AsStr())
	}
}
