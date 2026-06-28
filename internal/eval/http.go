package eval

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/anafalanx/drang/internal/value"
)

// A very small, very robust HTTP client. The whole surface is http() plus get/post sugar;
// everything else (retry, cookies, auth, pagination) is composed in drang. The design
// follows drang's subprocess split: like capture_all, a completed exchange is DATA — an
// HTTP 4xx/5xx is a normal {status,...} value, never an Err — while a failure to complete
// the exchange (DNS, refused, timeout, TLS, oversized body) is a catchable Err. A timeout
// carries code 124, matching the subprocess convention (err_code == 124 means "timed out"
// everywhere). The robust defaults are the point: Go's zero-value client has no timeout,
// an unbounded body, and easy TLS foot-guns.

const (
	httpDefaultTimeout   = 30 * time.Second
	httpDefaultMaxBody   = int64(32 << 20) // 32 MiB
	httpDefaultRedirects = 10
	httpUserAgent        = "drang" // identifiable + overridable via opts.headers
)

// httpTransport is shared (connection pooling + keep-alive) and never mutated after init,
// so it is safe to reuse across pmap workers. The insecure variant skips TLS verification
// for the opt-in {insecure: true} path only.
var (
	httpTransport         = &http.Transport{Proxy: http.ProxyFromEnvironment, MaxIdleConns: 100, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ExpectContinueTimeout: time.Second, ForceAttemptHTTP2: true}
	httpInsecureTransport = newInsecureTransport()
)

func newInsecureTransport() *http.Transport {
	t := httpTransport.Clone()
	t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 — opt-in {insecure:true}
	return t
}

func builtinHTTP(args []value.Value) (value.Value, error) {
	if len(args) < 2 || len(args) > 3 {
		return value.MakeNil(), fmt.Errorf("http expects 2 or 3 arguments (method, url, opts?), got %d", len(args))
	}
	if args[0].Tag() != value.Str {
		return value.MakeErr("http: method must be a string, got "+args[0].TypeName(), 1), nil
	}
	opts := value.MakeMap()
	if len(args) == 3 {
		opts = args[2]
	}
	return httpDo(args[0].AsStr(), args[1], opts)
}

func builtinHTTPGet(args []value.Value) (value.Value, error) {
	if len(args) < 1 || len(args) > 2 {
		return value.MakeNil(), fmt.Errorf("http_get expects 1 or 2 arguments (url, opts?), got %d", len(args))
	}
	opts := value.MakeMap()
	if len(args) == 2 {
		opts = args[1]
	}
	return httpDo("GET", args[0], opts)
}

func builtinHTTPPost(args []value.Value) (value.Value, error) {
	if len(args) < 2 || len(args) > 3 {
		return value.MakeNil(), fmt.Errorf("http_post expects 2 or 3 arguments (url, body, opts?), got %d", len(args))
	}
	merged, errv, bad := optsWithBody(args)
	if bad {
		return errv, nil
	}
	return httpDo("POST", args[0], merged)
}

// optsWithBody copies the optional opts map (args[2]) and sets its body to args[1], so the
// caller's map is never mutated. A non-map opts or a non-string body is a catchable Err.
func optsWithBody(args []value.Value) (value.Value, value.Value, bool) {
	if args[1].Tag() != value.Str {
		return value.MakeNil(), value.MakeErr("http_post: body must be a string (use http(\"POST\", url, {json: ...}) for JSON)", 1), true
	}
	out := value.MakeMap()
	om := out.Obj().(*value.OrderedMap)
	if len(args) == 3 {
		src, ok := optsMap(args[2])
		if !ok {
			return value.MakeNil(), value.MakeErr("http_post: opts must be a map, got "+args[2].TypeName(), 1), true
		}
		for i, k := range src.Keys() {
			om.Set(k, src.Vals()[i])
		}
	}
	om.Set(value.MakeStr("body"), args[1])
	return out, value.MakeNil(), false
}

func optsMap(v value.Value) (*value.OrderedMap, bool) {
	if v.Tag() == value.Map {
		return v.Obj().(*value.OrderedMap), true
	}
	return nil, false
}

// httpDo performs one request. Robust by construction: a deadline so it never hangs, a
// bounded response body, TLS verification on, transparent gzip, and a shared pooled
// transport. Transport failures become a catchable Err; any completed response (any
// status) becomes a {status, ok, body, headers, url} value.
func httpDo(method string, urlVal, optsVal value.Value) (value.Value, error) {
	if urlVal.Tag() != value.Str {
		return value.MakeErr("http: url must be a string, got "+urlVal.TypeName(), 1), nil
	}
	url := urlVal.AsStr()

	var opts *value.OrderedMap
	if optsVal.Tag() != value.Nil {
		m, ok := optsMap(optsVal)
		if !ok {
			return value.MakeErr("http: opts must be a map, got "+optsVal.TypeName(), 1), nil
		}
		opts = m
	}

	timeout := httpDefaultTimeout
	if ms, ok := optInt(opts, "timeout"); ok {
		switch {
		case ms < 0:
			return value.MakeErr("http: timeout must be >= 0 (ms; 0 = unlimited)", 1), nil
		case ms > int64(time.Duration(math.MaxInt64)/time.Millisecond):
			timeout = 0 // absurdly large -> unlimited (avoid int64-nanosecond overflow)
		default:
			timeout = time.Duration(ms) * time.Millisecond // 0 -> unlimited
		}
	}
	maxBody := httpDefaultMaxBody
	if mb, ok := optInt(opts, "max_body"); ok {
		maxBody = mb // 0 -> unlimited
	}
	redirects := int64(httpDefaultRedirects)
	if r, ok := optInt(opts, "redirects"); ok {
		redirects = r
	}

	// request body: body (string) or json (serialized); both is an error
	var bodyReader io.Reader
	contentType := ""
	bodyVal, hasBody := optGet(opts, "body")
	jsonVal, hasJSON := optGet(opts, "json")
	switch {
	case hasBody && hasJSON:
		return value.MakeErr("http: pass body or json, not both", 1), nil
	case hasJSON:
		jv, _ := builtinToJSON([]value.Value{jsonVal})
		if jv.Tag() == value.Err {
			return jv, nil
		}
		bodyReader = strings.NewReader(jv.AsStr())
		contentType = "application/json"
	case hasBody:
		if bodyVal.Tag() != value.Str {
			return value.MakeErr("http: body must be a string, got "+bodyVal.TypeName(), 1), nil
		}
		bodyReader = strings.NewReader(bodyVal.AsStr())
	}

	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), url, bodyReader)
	if err != nil {
		return value.MakeErr("http: "+err.Error(), 1), nil
	}
	req.Header.Set("User-Agent", httpUserAgent)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if err := applyHeaders(req, opts); err != nil {
		return value.MakeErr("http: "+err.Error(), 1), nil
	}

	transport := httpTransport
	if b, ok := httpOptBool(opts, "insecure"); ok && b {
		transport = httpInsecureTransport
	}
	client := &http.Client{
		Transport:     transport,
		CheckRedirect: redirectPolicy(redirects),
	}

	resp, err := client.Do(req)
	if err != nil {
		code := int64(1)
		var ne net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
			code = 124
		}
		return value.MakeErr("http: "+err.Error(), code), nil
	}
	defer resp.Body.Close()

	body, rerr := readLimited(resp.Body, maxBody)
	if rerr != nil {
		if errors.Is(rerr, errBodyTooLarge) {
			return value.MakeErr(fmt.Sprintf("http: response body exceeds max_body (%d bytes)", maxBody), 1), nil
		}
		// A deadline or an aborted/truncated stream during the body read is a failure to
		// complete the exchange — an Err, never a partial body returned as success.
		code := int64(1)
		var ne net.Error
		if errors.Is(rerr, context.DeadlineExceeded) || (errors.As(rerr, &ne) && ne.Timeout()) {
			code = 124
		}
		return value.MakeErr("http: "+rerr.Error(), code), nil
	}
	return httpResponse(resp, body), nil
}

// redirectPolicy follows up to `cap` redirects (cap 0 = don't follow, returning the 3xx as
// the response), dropping Authorization on a cross-host hop. Exceeding the cap is an Err.
func redirectPolicy(cap int64) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if cap <= 0 {
			return http.ErrUseLastResponse
		}
		if int64(len(via)) >= cap {
			return fmt.Errorf("stopped after %d redirects", cap)
		}
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			req.Header.Del("Authorization") // belt-and-suspenders; Go also strips on cross-host
		}
		return nil
	}
}

// applyHeaders sets request headers from opts.headers (a {name: value} map of strings).
func applyHeaders(req *http.Request, opts *value.OrderedMap) error {
	hv, ok := optGet(opts, "headers")
	if !ok {
		return nil
	}
	hm, ok := optsMap(hv)
	if !ok {
		return fmt.Errorf("headers must be a map, got %s", hv.TypeName())
	}
	for i, k := range hm.Keys() {
		v := hm.Vals()[i]
		if k.Tag() != value.Str || v.Tag() != value.Str {
			return fmt.Errorf("header names and values must be strings")
		}
		req.Header.Set(k.AsStr(), v.AsStr())
	}
	return nil
}

// errBodyTooLarge marks a body that exceeded max_body (distinct from a transport read
// error). The body cap counts DECOMPRESSED bytes (the transport inflates gzip before we
// read), so it is gzip-bomb safe.
var errBodyTooLarge = errors.New("response body exceeds max_body")

// readLimited reads up to max bytes (max <= 0 = unlimited). It returns errBodyTooLarge if
// the body exceeds max, or the underlying read error (a deadline firing mid-body, or a
// truncated/aborted stream) — it never returns a silently-truncated body as success.
func readLimited(r io.Reader, max int64) (string, error) {
	if max <= 0 {
		data, err := io.ReadAll(r)
		return string(data), err
	}
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return string(data), err
	}
	if int64(len(data)) > max {
		return "", errBodyTooLarge
	}
	return string(data), nil
}

func httpResponse(resp *http.Response, body string) value.Value {
	out := value.MakeMap()
	om := out.Obj().(*value.OrderedMap)
	om.Set(value.MakeStr("status"), value.MakeInt(int64(resp.StatusCode)))
	om.Set(value.MakeStr("ok"), value.MakeBool(resp.StatusCode >= 200 && resp.StatusCode <= 299))
	om.Set(value.MakeStr("body"), value.MakeStr(body))
	om.Set(value.MakeStr("headers"), headerMap(resp.Header))
	final := resp.Request.URL.String()
	om.Set(value.MakeStr("url"), value.MakeStr(final))
	return out
}

// headerMap renders response headers as a drang map with lowercased keys; multi-value
// headers are joined with ", " (a map cannot hold duplicate keys).
func headerMap(h http.Header) value.Value {
	out := value.MakeMap()
	om := out.Obj().(*value.OrderedMap)
	for k, vs := range h {
		om.Set(value.MakeStr(strings.ToLower(k)), value.MakeStr(strings.Join(vs, ", ")))
	}
	return out
}

// --- opts accessors (nil-safe; absent or wrong-typed yields ok=false) ---

func optGet(opts *value.OrderedMap, key string) (value.Value, bool) {
	if opts == nil {
		return value.MakeNil(), false
	}
	return opts.Get(value.MakeStr(key))
}

func optInt(opts *value.OrderedMap, key string) (int64, bool) {
	v, ok := optGet(opts, key)
	if !ok {
		return 0, false
	}
	switch v.Tag() {
	case value.Int:
		return v.AsInt(), true
	case value.Float:
		return int64(v.AsFloat()), true
	}
	return 0, false
}

func httpOptBool(opts *value.OrderedMap, key string) (bool, bool) {
	v, ok := optGet(opts, key)
	if !ok {
		return false, false
	}
	return v.Truthy(), true
}
