//go:build e2e

package e2e_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/e2e/harness"
)

// httpRouterRecorder is a configurable mock of the external HTTP routing service.
// Each request is captured under a mutex so tests can assert on what the gateway
// forwarded.
type httpRouterRecorder struct {
	server *httptest.Server

	mu              sync.Mutex
	calls           int
	lastHeaders     http.Header
	lastBody        []byte
	routingGroup    string
	externalHeaders map[string]string
	errors          []string
	delay           time.Duration
}

func newHTTPRouter(t *testing.T, routingGroup string, externalHeaders map[string]string, errs []string) *httpRouterRecorder {
	t.Helper()
	rec := &httpRouterRecorder{
		routingGroup:    routingGroup,
		externalHeaders: externalHeaders,
		errors:          errs,
	}
	rec.server = httptest.NewServer(http.HandlerFunc(rec.handle))
	t.Cleanup(rec.server.Close)
	return rec
}

func (r *httpRouterRecorder) URL() string { return r.server.URL }

func (r *httpRouterRecorder) handle(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	delay := r.delay
	r.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}

	body, _ := io.ReadAll(req.Body)

	r.mu.Lock()
	r.calls++
	r.lastHeaders = req.Header.Clone()
	r.lastBody = body
	resp := map[string]any{
		"routingGroup":    r.routingGroup,
		"errors":          r.errors,
		"externalHeaders": r.externalHeaders,
	}
	r.mu.Unlock()

	if resp["errors"] == nil {
		resp["errors"] = []string{}
	}
	if resp["externalHeaders"] == nil {
		resp["externalHeaders"] = map[string]string{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (r *httpRouterRecorder) setDelay(d time.Duration) {
	r.mu.Lock()
	r.delay = d
	r.mu.Unlock()
}

func (r *httpRouterRecorder) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *httpRouterRecorder) LastHeaders() http.Header {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lastHeaders == nil {
		return nil
	}
	return r.lastHeaders.Clone()
}

// TestE2E_ExternalHTTP_RoutingGroupUsed asserts that the routing group returned
// by the external HTTP service steers the request to a backend in that group,
// not to backends in other groups.
func TestE2E_ExternalHTTP_RoutingGroupUsed(t *testing.T) {
	router := newHTTPRouter(t, "etl", nil, nil)
	h := harness.New(t, harness.WithExternalHTTPRouter(router.URL()))

	etl := h.AddBackend(t, "etl-backend", "etl")
	def := h.AddBackend(t, "default-backend", "default")

	resp, body := postStatement(t, h, "SELECT 1", nil)
	defer resp.Body.Close()
	require.Equalf(t, http.StatusOK, resp.StatusCode, "status=%d body=%s", resp.StatusCode, string(body))

	assert.Len(t, etl.QueryIDs(), 1, "etl-backend must receive the POST")
	assert.Empty(t, def.QueryIDs(), "default-backend must not receive the POST")
}

// TestE2E_ExternalHTTP_ExternalHeadersReplace asserts that headers returned in
// externalHeaders REPLACE the inbound client value rather than appending — only
// the router-supplied value reaches the backend.
func TestE2E_ExternalHTTP_ExternalHeadersReplace(t *testing.T) {
	router := newHTTPRouter(t, "default",
		map[string]string{"X-Custom-Header": "from-router"}, nil)
	h := harness.New(t, harness.WithExternalHTTPRouter(router.URL()))

	backend := h.AddBackend(t, "default-backend", "default")

	hdr := http.Header{}
	hdr.Set("X-Custom-Header", "original-value")
	resp, body := postStatement(t, h, "SELECT 1", hdr)
	defer resp.Body.Close()
	require.Equalf(t, http.StatusOK, resp.StatusCode, "status=%d body=%s", resp.StatusCode, string(body))

	ids := backend.QueryIDs()
	require.Len(t, ids, 1)

	received := backend.ReceivedHeaders(ids[0])
	require.NotNil(t, received)
	values := received.Values("X-Custom-Header")
	assert.Equal(t, []string{"from-router"}, values,
		"externalHeaders must REPLACE the inbound value, got %v", values)
}

// TestE2E_ExternalHTTP_ExcludeHeaders asserts that headers listed in
// routing.external.excludeHeaders are stripped from BOTH the outgoing routing
// request and the incoming externalHeaders response. Matches Java semantics:
// excludeHeaders filters (a) the headers forwarded to the routing service and
// (b) the externalHeaders response before it is applied to the upstream
// request. It does NOT strip the header from the inbound client request when
// the client itself sent it (the gateway is not a security filter for the
// backend connection).
func TestE2E_ExternalHTTP_ExcludeHeaders(t *testing.T) {
	router := newHTTPRouter(t, "default",
		map[string]string{"X-Secret": "leaked-from-router"}, nil)
	h := harness.New(t,
		harness.WithExternalHTTPRouter(router.URL()),
		harness.WithExcludeHeaders("X-Secret"),
	)

	backend := h.AddBackend(t, "default-backend", "default")

	// Inbound client request sends a Secret header. The gateway forwards it
	// transparently to the backend (excludeHeaders does not gate that), but
	// must NOT forward it to the routing service.
	hdr := http.Header{}
	hdr.Set("X-Secret", "from-client")
	resp, body := postStatement(t, h, "SELECT 1", hdr)
	defer resp.Body.Close()
	require.Equalf(t, http.StatusOK, resp.StatusCode, "status=%d body=%s", resp.StatusCode, string(body))

	// 1. The routing service must not see X-Secret in its inbound request.
	routerHeaders := router.LastHeaders()
	require.NotNil(t, routerHeaders, "router must have received a request")
	assert.Empty(t, routerHeaders.Get("X-Secret"),
		"X-Secret must not be forwarded to the routing service")

	// 2. The router-supplied externalHeaders value for X-Secret must be filtered
	// before it overrides the upstream request. The backend should see the
	// client's original value, not the router's "leaked-from-router".
	ids := backend.QueryIDs()
	require.Len(t, ids, 1)
	received := backend.ReceivedHeaders(ids[0])
	require.NotNil(t, received)
	assert.Equal(t, "from-client", received.Get("X-Secret"),
		"X-Secret in externalHeaders must be filtered, leaving the original client value untouched")
}

// TestE2E_ExternalHTTP_FallbackOnRouterDown asserts that an unreachable
// routing service does not surface as a 502 to the client — the gateway falls
// back to the default routing group.
func TestE2E_ExternalHTTP_FallbackOnRouterDown(t *testing.T) {
	// localhost:1 is a privileged port no test process is listening on —
	// connection is refused immediately.
	h := harness.New(t, harness.WithExternalHTTPRouter("http://127.0.0.1:1"))

	backend := h.AddBackend(t, "default-backend", "default")

	resp, body := postStatement(t, h, "SELECT 1", nil)
	defer resp.Body.Close()
	require.Equalf(t, http.StatusOK, resp.StatusCode,
		"router down must fall back to defaultGroup, got status=%d body=%s",
		resp.StatusCode, string(body))

	assert.Len(t, backend.QueryIDs(), 1)
}

// TestE2E_ExternalHTTP_PropagateErrors asserts that when proxy.propagateErrors
// is true and the router returns non-empty errors, the client receives HTTP 400.
func TestE2E_ExternalHTTP_PropagateErrors(t *testing.T) {
	router := newHTTPRouter(t, "default", nil, []string{"access denied"})
	h := harness.New(t,
		harness.WithExternalHTTPRouter(router.URL()),
		harness.WithPropagateErrors(true),
	)
	h.AddBackend(t, "default-backend", "default")

	resp, body := postStatement(t, h, "SELECT 1", nil)
	defer resp.Body.Close()
	assert.Equalf(t, http.StatusBadRequest, resp.StatusCode,
		"non-empty router errors + propagateErrors=true must return 400, got %d body=%s",
		resp.StatusCode, string(body))
}

// TestE2E_ExternalHTTP_TimeoutFallback asserts that a routing service that
// exceeds the configured timeout triggers fallback to defaultGroup rather than
// hanging or returning a 502 to the client.
func TestE2E_ExternalHTTP_TimeoutFallback(t *testing.T) {
	router := newHTTPRouter(t, "default", nil, nil)
	router.setDelay(2 * time.Second)

	h := harness.New(t,
		harness.WithExternalHTTPRouter(router.URL()),
		harness.WithExternalTimeout(200*time.Millisecond),
	)
	backend := h.AddBackend(t, "default-backend", "default")

	start := time.Now()
	resp, body := postStatement(t, h, "SELECT 1", nil)
	defer resp.Body.Close()
	elapsed := time.Since(start)

	require.Equalf(t, http.StatusOK, resp.StatusCode,
		"timeout must fall back, got status=%d body=%s", resp.StatusCode, string(body))
	assert.Less(t, elapsed, 1500*time.Millisecond,
		"request must not wait for the full router delay (took %s)", elapsed)
	assert.Len(t, backend.QueryIDs(), 1)
}
