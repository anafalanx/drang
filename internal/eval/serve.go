package eval

// serve() — a local, single-user htmx GUI server.
//
// It binds 127.0.0.1 on an (ephemeral by default) port, gates every request on a
// per-launch random token (so no *other* local process can drive it), routes
// request paths to drang handler functions that return HTML — a full page or an
// htmx fragment — serves the embedded htmx runtime and either a static asset
// directory (dev) or the web/ tree bundled into a `drang build` standalone, and
// (by default) opens the page in a clamped system browser window. Calls into the
// drang VM are serialized (one handler at a time), which is correct and ample for
// a single browser cockpit; a handler panic becomes a 500, never a crashed server.
// Closing the browser window shuts the server down and wipes its throwaway profile.
//
// The clamped browser launch lives in serve_browser.go; this file is the server.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/anafalanx/drang/internal/value"
)

// htmxJS is the htmx runtime, baked into the binary so a GUI tool is fully
// self-contained and offline. Served at htmxPath; reference it from a page with
// <script src="/_/htmx.js"></script>.
//
//go:embed htmx.min.js
var htmxJS []byte

const htmxPath = "/_/htmx.js"

// embeddedWeb holds the web/ asset tree bundled into a `drang build` standalone
// (forward-slash path -> bytes), or nil for a plain run. When present, serve()
// serves it in place of a disk static: directory.
var embeddedWeb map[string][]byte

// SetEmbeddedWeb registers assets embedded in a standalone build, so serve() can
// serve them from memory. Called once at startup by the standalone loader.
func SetEmbeddedWeb(m map[string][]byte) { embeddedWeb = m }

// builtinServe implements serve({routes, static?, port?, open?}). It blocks,
// running the server until the browser window closes (open:true) or the process
// is interrupted. Bad options and bind failures are catchable Errs.
func builtinServe(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return value.MakeNil(), fmt.Errorf("serve expects 1 argument (an options map), got %d", len(args))
	}
	if args[0].Tag() != value.Map {
		return value.MakeErr("serve: options must be a map, got "+args[0].TypeName(), 1), nil
	}
	g, ev := buildGUIServer(args[0].Obj().(*value.OrderedMap))
	if g == nil {
		return ev, nil
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", g.port))
	if err != nil {
		return value.MakeErr("serve: "+err.Error(), 1), nil
	}
	g.url = fmt.Sprintf("http://127.0.0.1:%d/?t=%s", ln.Addr().(*net.TCPAddr).Port, g.token)
	fmt.Fprintf(stdout, "drang: serving on %s\n", g.url)

	httpSrv := &http.Server{Handler: g}
	if g.open {
		go g.openAndWatch(httpSrv) // launch the clamped browser; shut down when it closes
	}
	if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return value.MakeErr("serve: "+err.Error(), 1), nil
	}
	return value.MakeNil(), nil
}

// guiServer is the http.Handler built from the drang options: the route table
// (path -> drang handler), an optional static file server, and the access token.
// mu serializes VM re-entry so exactly one handler runs at a time.
type guiServer struct {
	routes     map[string]*Function
	fileServer http.Handler
	token      string
	port       int
	open       bool
	url        string
	mu         sync.Mutex
}

// buildGUIServer validates the options map into a ready server, or returns a
// catchable Err describing what was wrong.
func buildGUIServer(opts *value.OrderedMap) (*guiServer, value.Value) {
	g := &guiServer{routes: map[string]*Function{}, open: true}

	routesVal, ok := opts.Get(value.MakeStr("routes"))
	if !ok || routesVal.Tag() != value.Map {
		return nil, value.MakeErr("serve: options.routes must be a map of {path: handler}", 1)
	}
	rm := routesVal.Obj().(*value.OrderedMap)
	for i, k := range rm.Keys() {
		if k.Tag() != value.Str {
			return nil, value.MakeErr("serve: route paths must be strings, got "+k.TypeName(), 1)
		}
		p := k.AsStr()
		if p == "" || p[0] != '/' {
			return nil, value.MakeErr(fmt.Sprintf("serve: route path %q must start with '/'", p), 1)
		}
		if p == htmxPath {
			return nil, value.MakeErr(fmt.Sprintf("serve: %q is reserved for the embedded htmx runtime", htmxPath), 1)
		}
		fn, ok := asFunction(rm.Vals()[i])
		if !ok {
			return nil, value.MakeErr(fmt.Sprintf("serve: route %q handler must be a function, got %s", p, rm.Vals()[i].TypeName()), 1)
		}
		g.routes[p] = fn
	}

	// A built standalone serves its embedded web/ tree from memory; a plain run
	// serves the static: directory from disk (http.Dir is traversal-safe).
	if len(embeddedWeb) > 0 {
		g.fileServer = memFileServer(embeddedWeb)
	} else if sv, ok := opts.Get(value.MakeStr("static")); ok && sv.Tag() == value.Str && sv.AsStr() != "" {
		g.fileServer = http.FileServer(http.Dir(sv.AsStr()))
	}

	if p, ok := optInt(opts, "port"); ok {
		if p < 0 || p > 65535 {
			return nil, value.MakeErr("serve: port must be 0..65535", 1)
		}
		g.port = int(p)
	}
	if o, ok := httpOptBool(opts, "open"); ok {
		g.open = o
	}
	g.token = randToken()
	return g, value.MakeNil()
}

// randToken returns a 256-bit hex token, unguessable per launch.
func randToken() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (g *guiServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !g.authorize(w, r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.URL.Path == htmxPath {
		g.serveHTMX(w)
		return
	}
	if fn, ok := g.routes[r.URL.Path]; ok {
		g.runHandler(w, r, fn)
		return
	}
	if g.fileServer != nil {
		g.fileServer.ServeHTTP(w, r)
		return
	}
	http.NotFound(w, r)
}

func (g *guiServer) serveHTMX(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(htmxJS)
}

// authorize accepts a request carrying the launch token either as the ?t= query
// parameter (the initial navigation from the launched browser) or the drang_token
// cookie (every subsequent htmx request). A valid query token (re)issues the
// cookie. Constant-time compare. 127.0.0.1 binding already excludes remote
// clients; the token excludes other local processes.
func (g *guiServer) authorize(w http.ResponseWriter, r *http.Request) bool {
	if t := r.URL.Query().Get("t"); t != "" && tokenEqual(t, g.token) {
		http.SetCookie(w, &http.Cookie{
			Name:     "drang_token",
			Value:    g.token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
		return true
	}
	if c, err := r.Cookie("drang_token"); err == nil && tokenEqual(c.Value, g.token) {
		return true
	}
	return false
}

func tokenEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// runHandler builds the request value, calls the drang handler under the VM lock,
// and writes its result.
func (g *guiServer) runHandler(w http.ResponseWriter, r *http.Request, fn *Function) {
	req := requestValue(r)
	g.mu.Lock()
	result, err := callHandlerSafely(fn, req)
	g.mu.Unlock()
	if err != nil {
		http.Error(w, "drang handler error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeResult(w, result)
}

// callHandlerSafely calls a drang handler with a panic guard, so a bug in a
// handler yields a 500 rather than tearing down the cockpit (mirrors applyPmap).
// A handler may declare 0 params (ignore the request) or 1 (receive it).
func callHandlerSafely(fn *Function, req value.Value) (result value.Value, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			result = value.MakeNil()
			err = fmt.Errorf("handler panicked: %v", rec)
		}
	}()
	var callArgs []value.Value
	switch {
	case fn.Builtin != nil, len(fn.Params) == 1:
		callArgs = []value.Value{req}
	case len(fn.Params) == 0:
		// handler ignores the request
	default:
		return value.MakeNil(), fmt.Errorf("handler must take 0 or 1 parameter, got %d", len(fn.Params))
	}
	return callFunction(fn, callArgs, 0)
}

// writeResult renders a handler's return value: a string is text/html; a map is
// {status?, headers?, body}; nil is 204; an Err (or anything else) is a 500.
func writeResult(w http.ResponseWriter, v value.Value) {
	switch {
	case v.IsErr():
		http.Error(w, "drang: "+v.ErrMsg(), http.StatusInternalServerError)
	case v.Tag() == value.Str:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, v.AsStr())
	case v.Tag() == value.Map:
		writeMapResult(w, v.Obj().(*value.OrderedMap))
	case v.Tag() == value.Nil:
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "drang: a handler must return a string, a map, or nil (got "+v.TypeName()+")", http.StatusInternalServerError)
	}
}

func writeMapResult(w http.ResponseWriter, m *value.OrderedMap) {
	status := http.StatusOK
	if s, ok := m.Get(value.MakeStr("status")); ok && s.Tag() == value.Int {
		status = int(s.AsInt())
	}
	ct := "text/html; charset=utf-8"
	if h, ok := m.Get(value.MakeStr("headers")); ok && h.Tag() == value.Map {
		hm := h.Obj().(*value.OrderedMap)
		for i, k := range hm.Keys() {
			vv := hm.Vals()[i]
			if k.Tag() != value.Str || vv.Tag() != value.Str {
				continue
			}
			if strings.EqualFold(k.AsStr(), "content-type") {
				ct = vv.AsStr()
			} else {
				w.Header().Set(k.AsStr(), vv.AsStr())
			}
		}
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(status)
	if b, ok := m.Get(value.MakeStr("body")); ok && b.Tag() == value.Str {
		_, _ = io.WriteString(w, b.AsStr())
	}
}

// requestValue converts an HTTP request into the drang map a handler receives:
// {method, path, query, form, headers}. ParseForm merges URL query and (for form
// content-types) the POST body — which is what htmx sends by default.
func requestValue(r *http.Request) value.Value {
	_ = r.ParseForm()
	out := value.MakeMap()
	om := out.Obj().(*value.OrderedMap)
	om.Set(value.MakeStr("method"), value.MakeStr(r.Method))
	om.Set(value.MakeStr("path"), value.MakeStr(r.URL.Path))
	om.Set(value.MakeStr("query"), firstValuesMap(r.URL.Query()))
	om.Set(value.MakeStr("form"), firstValuesMap(r.Form))
	om.Set(value.MakeStr("headers"), headerMap(r.Header)) // shared with the http client
	return out
}

// firstValuesMap renders url.Values as a drang map, taking the first value per key.
func firstValuesMap(vals url.Values) value.Value {
	out := value.MakeMap()
	om := out.Obj().(*value.OrderedMap)
	for k, vs := range vals {
		if len(vs) > 0 {
			om.Set(value.MakeStr(k), value.MakeStr(vs[0]))
		}
	}
	return out
}

// memFileServer serves an in-memory asset map (forward-slash path -> bytes) as a
// static file server, mapping "/" to index.html. Content types come from the file
// extension, falling back to content sniffing.
type memFileServer map[string][]byte

func (m memFileServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	if p == "" {
		p = "index.html"
	}
	data, ok := m[p]
	if !ok {
		http.NotFound(w, r)
		return
	}
	ct := mime.TypeByExtension(path.Ext(p))
	if ct == "" {
		ct = http.DetectContentType(data)
	}
	w.Header().Set("Content-Type", ct)
	_, _ = w.Write(data)
}

// openAndWatch launches the clamped browser at g.url in a throwaway profile, then
// blocks until its window closes and shuts the server down, wiping the profile. If
// the clamped launch is unavailable it falls back to the default browser (which
// can't be watched, so the server then runs until the process is interrupted).
func (g *guiServer) openAndWatch(httpSrv *http.Server) {
	profileDir, err := os.MkdirTemp("", "drang-gui-")
	if err != nil {
		profileDir = ""
	}
	cmd, launchErr := launchClampedBrowser(g.url, profileDir)
	if launchErr != nil {
		fmt.Fprintf(stdout, "drang: clamped Edge unavailable (%v); opening the default browser (unclamped)\n", launchErr)
		openDefaultBrowser(g.url)
		if profileDir != "" {
			os.RemoveAll(profileDir)
		}
		return
	}
	_ = cmd.Wait() // returns when the isolated app window closes
	_ = httpSrv.Shutdown(context.Background())
	if profileDir != "" {
		os.RemoveAll(profileDir)
	}
}
