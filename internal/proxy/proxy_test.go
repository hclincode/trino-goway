package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/config"
	"github.com/hclincode/trino-goway/internal/routing"
)

// --- Fakes ---

// fakeRouter is a test double for the Router interface.
type fakeRouter struct {
	mu         sync.Mutex
	backendURL string
	callOrder  []string // records "WriteCache" or "Route" in call order
}

func (f *fakeRouter) Route(_ context.Context, _ *routing.RouteInput) (*routing.RouteResult, error) {
	f.mu.Lock()
	f.callOrder = append(f.callOrder, "Route")
	f.mu.Unlock()
	return &routing.RouteResult{BackendURL: f.backendURL}, nil
}

func (f *fakeRouter) WriteCache(queryID, _ string) {
	f.mu.Lock()
	f.callOrder = append(f.callOrder, "WriteCache:"+queryID)
	f.mu.Unlock()
}

func (f *fakeRouter) CallOrder() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.callOrder))
	copy(out, f.callOrder)
	return out
}

// writeCaptureRecorder wraps httptest.ResponseRecorder and records Write calls.
type writeCaptureRecorder struct {
	*httptest.ResponseRecorder
	writes [][]byte
	mu     sync.Mutex
}

func newWriteCapture() *writeCaptureRecorder {
	return &writeCaptureRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (r *writeCaptureRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	cp := make([]byte, len(b))
	copy(cp, b)
	r.writes = append(r.writes, cp)
	r.mu.Unlock()
	return r.ResponseRecorder.Write(b)
}

// discardLogger returns a slog.Logger that discards all output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// buildProxy builds a Proxy for unit testing.
func buildProxy(t *testing.T, router *fakeRouter, client *http.Client) *Proxy {
	t.Helper()
	return New(Config{
		Proxy: config.ProxyConfig{
			ResponseSize: config.DataSize{Bytes: 1_048_576},
		},
		Cookie: config.CookieConfig{
			Secret:     "",
			WireCompat: true,
		},
		Client: client,
		Router: router,
		Log:    discardLogger(),
	})
}

// --- Seam 1: Never rewrite response body ---

// TestProxy_Seam1_NeverRewriteResponseBody verifies that the response body from upstream
// is forwarded to the client byte-for-byte without modification.
func TestProxy_Seam1_NeverRewriteResponseBody(t *testing.T) {
	t.Parallel()

	const queryID = "20240101_000000_00001_xxxxx"
	upstreamBody := `{"id":"` + queryID + `","nextUri":"http://trino:8080/v1/statement/executing/` + queryID + `/1","columns":[]}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, upstreamBody)
	}))
	defer upstream.Close()

	router := &fakeRouter{backendURL: upstream.URL}
	proxy := buildProxy(t, router, upstream.Client())

	req := httptest.NewRequest(http.MethodPost, "/v1/statement", strings.NewReader("SELECT 1"))
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, upstreamBody, w.Body.String(), "response body must be forwarded byte-for-byte")
}

// --- Seam 2: Redirect following disabled ---

// TestProxy_Seam2_RedirectFollowingDisabled verifies that when upstream returns a 302,
// the proxy forwards the 302 to the client rather than following it.
func TestProxy_Seam2_RedirectFollowingDisabled(t *testing.T) {
	t.Parallel()

	redirectTarget := "http://example.com/redirected"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget, http.StatusFound)
	}))
	defer upstream.Close()

	// Build client that does NOT follow redirects — per Hard Invariant #2.
	noFollowClient := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	router := &fakeRouter{backendURL: upstream.URL}
	proxy := buildProxy(t, router, noFollowClient)

	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	assert.Equal(t, http.StatusFound, w.Code, "proxy must forward 302, not follow it")
	assert.Equal(t, redirectTarget, w.Header().Get("Location"))
}

// --- Seam 3: Cache write before response flush ---

// TestProxy_Seam3_CacheWriteBeforeResponseFlush verifies that WriteCache is called
// synchronously before the response body is written to the client.
func TestProxy_Seam3_CacheWriteBeforeResponseFlush(t *testing.T) {
	t.Parallel()

	const queryID = "20240101_000000_00002_seam3"
	upstreamBody, _ := json.Marshal(map[string]string{
		"id":      queryID,
		"nextUri": "http://trino:8080/v1/statement/executing/" + queryID + "/1",
	})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(upstreamBody)
	}))
	defer upstream.Close()

	// orderRecorder records the sequence of WriteCache and body Write events.
	var mu sync.Mutex
	var order []string

	router := &fakeRouter{backendURL: upstream.URL}

	// Intercept WriteCache to record order.
	writeCacheInterceptRouter := &writeCacheOrderRouter{
		inner: router,
		onWriteCache: func(qid string) {
			mu.Lock()
			order = append(order, "WriteCache:"+qid)
			mu.Unlock()
		},
	}

	proxy := New(Config{
		Proxy:  config.ProxyConfig{ResponseSize: config.DataSize{Bytes: 1_048_576}},
		Cookie: config.CookieConfig{WireCompat: true},
		Client: upstream.Client(),
		Router: writeCacheInterceptRouter,
		Log:    discardLogger(),
	})

	// Use a recorder that captures when Write is called.
	recorder := newOrderRecorder(func() {
		mu.Lock()
		order = append(order, "ClientWrite")
		mu.Unlock()
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/statement", strings.NewReader("SELECT 1"))
	proxy.ServeHTTP(recorder, req)

	mu.Lock()
	captured := make([]string, len(order))
	copy(captured, order)
	mu.Unlock()

	require.GreaterOrEqual(t, len(captured), 2, "expected at least WriteCache and ClientWrite events")
	writeCacheIdx := -1
	clientWriteIdx := -1
	for i, e := range captured {
		if strings.HasPrefix(e, "WriteCache:") {
			writeCacheIdx = i
		}
		if e == "ClientWrite" {
			clientWriteIdx = i
		}
	}
	assert.NotEqual(t, -1, writeCacheIdx, "WriteCache must be called")
	assert.NotEqual(t, -1, clientWriteIdx, "ClientWrite must be called")
	assert.Less(t, writeCacheIdx, clientWriteIdx, "WriteCache must be called before body Write")
}

// writeCacheOrderRouter wraps fakeRouter and invokes onWriteCache before delegating.
type writeCacheOrderRouter struct {
	inner        *fakeRouter
	onWriteCache func(string)
}

func (r *writeCacheOrderRouter) Route(ctx context.Context, req *routing.RouteInput) (*routing.RouteResult, error) {
	return r.inner.Route(ctx, req)
}

func (r *writeCacheOrderRouter) WriteCache(queryID, backendURL string) {
	r.onWriteCache(queryID)
	r.inner.WriteCache(queryID, backendURL)
}

// orderRecorder is an http.ResponseWriter that calls onFirstWrite when Write is called.
type orderRecorder struct {
	*httptest.ResponseRecorder
	onFirstWrite func()
	once         sync.Once
}

func newOrderRecorder(onFirstWrite func()) *orderRecorder {
	return &orderRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		onFirstWrite:     onFirstWrite,
	}
}

func (r *orderRecorder) Write(b []byte) (int, error) {
	r.once.Do(r.onFirstWrite)
	return r.ResponseRecorder.Write(b)
}

// --- Seam 4: Router-result handling (recovery chain lives in internal/routing) ---

// TestProxy_Seam4_RouterResultHandling verifies the proxy forwards to whatever
// BackendURL the Router returns. The proxy has no visibility into which recovery
// step produced the URL — the three-step chain (cache → history → HEAD probe →
// first-active) is owned and tested by internal/routing. See
// internal/proxy/proxy_qa_test.go for the empty-BackendURL fail-closed companion.
func TestProxy_Seam4_RouterResultHandling(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"recovery_query","nextUri":"http://trino:8080/v1/statement/executing/recovery_query/1"}`)
	}))
	defer upstream.Close()

	router := &fakeRouter{backendURL: upstream.URL}
	proxy := buildProxy(t, router, upstream.Client())

	req := httptest.NewRequest(http.MethodPost, "/v1/statement", strings.NewReader("SELECT 1"))
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "proxy must forward to the Router-supplied BackendURL")
}

// --- Seam 6: KILL QUERY regex routing ---

// TestProxy_Seam6_KillQueryRegexRouting verifies that a KILL QUERY statement body is
// passed to the router so the routing package can extract and route it to the history backend.
func TestProxy_Seam6_KillQueryRegexRouting(t *testing.T) {
	t.Parallel()

	const killQueryBody = `KILL QUERY '20240101_000000_00003_zzzzz'`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// The upstream must receive the KILL QUERY body verbatim.
		assert.Equal(t, killQueryBody, string(body))
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()

	// captureRouter captures the RouteInput.Body passed by the proxy.
	captureRouter := &captureInputRouter{backendURL: upstream.URL}

	proxy := New(Config{
		Proxy:  config.ProxyConfig{ResponseSize: config.DataSize{Bytes: 1_048_576}},
		Cookie: config.CookieConfig{WireCompat: true},
		Client: upstream.Client(),
		Router: captureRouter,
		Log:    discardLogger(),
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/statement", strings.NewReader(killQueryBody))
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	require.NotNil(t, captureRouter.LastInput(), "router.Route must be called")
	assert.Equal(t, killQueryBody, captureRouter.LastInput().Body,
		"proxy must pass buffered request body to router so KILL QUERY can be detected")
}

// captureInputRouter captures the last RouteInput passed to Route.
type captureInputRouter struct {
	mu         sync.Mutex
	backendURL string
	lastInput  *routing.RouteInput
}

func (r *captureInputRouter) Route(_ context.Context, req *routing.RouteInput) (*routing.RouteResult, error) {
	r.mu.Lock()
	r.lastInput = req
	r.mu.Unlock()
	return &routing.RouteResult{BackendURL: r.backendURL}, nil
}

func (r *captureInputRouter) WriteCache(_, _ string) {}

func (r *captureInputRouter) LastInput() *routing.RouteInput {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastInput
}

// --- Seam 7: Three client pool isolation ---

// TestProxy_Seam7_ThreeClientPoolIsolation verifies that the proxy uses the client
// passed in via Config and does not create its own.
func TestProxy_Seam7_ThreeClientPoolIsolation(t *testing.T) {
	t.Parallel()

	var usedClient *http.Client

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()

	// trackingTransport records that it was used.
	called := false
	transport := &trackingTransport{
		inner: upstream.Client().Transport,
		onDo: func() { called = true },
	}
	injectedClient := &http.Client{Transport: transport}
	usedClient = injectedClient

	router := &fakeRouter{backendURL: upstream.URL}
	proxy := New(Config{
		Proxy:  config.ProxyConfig{ResponseSize: config.DataSize{Bytes: 1_048_576}},
		Cookie: config.CookieConfig{WireCompat: true},
		Client: usedClient,
		Router: router,
		Log:    discardLogger(),
	})

	// Verify proxy.client is the injected client.
	assert.Same(t, injectedClient, proxy.client, "proxy must use the passed-in client")

	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	assert.True(t, called, "proxy must use the injected client's transport")
}

// trackingTransport records calls to RoundTrip.
type trackingTransport struct {
	inner http.RoundTripper
	onDo  func()
}

func (t *trackingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.onDo()
	return t.inner.RoundTrip(r)
}

// --- Unit tests for cookie and header helpers ---

func TestExtractQueryIDFromBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "valid id field",
			body: `{"id":"20240101_000000_00001_xxxxx","nextUri":"http://trino:8080/v1/statement/executing/20240101_000000_00001_xxxxx/1"}`,
			want: "20240101_000000_00001_xxxxx",
		},
		{
			name: "missing id",
			body: `{"nextUri":"http://trino:8080/v1/statement/executing/q1/1"}`,
			want: "",
		},
		{
			name: "invalid json",
			body: `not json`,
			want: "",
		},
		{
			name: "empty body",
			body: ``,
			want: "",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := extractQueryIDFromBody([]byte(tc.body))
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestIsHopByHop(t *testing.T) {
	t.Parallel()

	assert.True(t, isHopByHop("Connection"))
	assert.True(t, isHopByHop("connection")) // case-insensitive
	assert.True(t, isHopByHop("Transfer-Encoding"))
	assert.False(t, isHopByHop("Content-Type"))
	assert.False(t, isHopByHop("X-Trino-User"))
}

func TestAirliftDurationString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input time.Duration
		want  string
	}{
		{10 * time.Minute, "10.00m"},
		{1 * time.Hour, "1.00h"},
		{500 * time.Millisecond, "500.00ms"},
		{1 * time.Second, "1.00s"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			got := airliftDurationString(tc.input)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestCopyHeaders_SkipsHopByHop(t *testing.T) {
	t.Parallel()

	src := http.Header{
		"Content-Type":      {"application/json"},
		"X-Trino-User":      {"alice"},
		"Transfer-Encoding": {"chunked"},
		"Connection":        {"keep-alive"},
	}
	dst := http.Header{}
	copyHeaders(dst, src)

	assert.Equal(t, "application/json", dst.Get("Content-Type"))
	assert.Equal(t, "alice", dst.Get("X-Trino-User"))
	assert.Empty(t, dst.Get("Transfer-Encoding"))
	assert.Empty(t, dst.Get("Connection"))
}

func TestInjectHeaders_XForwarded(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	router := &fakeRouter{backendURL: upstream.URL}
	proxy := buildProxy(t, router, upstream.Client())

	inbound := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	inbound.RemoteAddr = "192.168.1.1:12345"

	result := &routing.RouteResult{
		BackendURL:      upstream.URL,
		ExternalHeaders: map[string]string{"X-Custom-Header": "custom-value"},
	}

	target, _ := buildUpstreamRequestFromProxy(proxy, inbound, result)
	proxy.injectHeaders(target, inbound, result)

	assert.Equal(t, "192.168.1.1", target.Header.Get("X-Forwarded-For"))
	assert.Equal(t, "http", target.Header.Get("X-Forwarded-Proto"))
	assert.Equal(t, inbound.Host, target.Header.Get("X-Forwarded-Host"))
	assert.Equal(t, "custom-value", target.Header.Get("X-Custom-Header"))
}

// buildUpstreamRequestFromProxy is a test helper that calls buildUpstreamRequest on a Proxy.
func buildUpstreamRequestFromProxy(p *Proxy, r *http.Request, result *routing.RouteResult) (*http.Request, error) {
	return p.buildUpstreamRequest(r.Context(), result.BackendURL, r, bytes.NewReader(nil)), nil
}
