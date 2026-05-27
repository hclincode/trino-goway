package proxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/config"
	"github.com/hclincode/trino-goway/internal/routing"
	"github.com/hclincode/trino-goway/internal/testutil"
)

// TestMain enforces the no-goroutine-leak invariant for the proxy package.
// Per rubric: proxy-core requires concurrency safety evidence (race + leak + shutdown).
func TestMain(m *testing.M) {
	testutil.VerifyTestMain(m)
}

// --- Degradation: backend down (connection refused) ---

// TestProxy_Degradation_BackendConnectionRefused verifies that when the upstream
// backend cannot be reached, the proxy returns 502 rather than crashing or hanging.
func TestProxy_Degradation_BackendConnectionRefused(t *testing.T) {
	t.Parallel()

	// Bind a listener, capture its addr, then immediately close it so dialing fails.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	deadAddr := "http://" + ln.Addr().String()
	require.NoError(t, ln.Close())

	router := &fakeRouter{backendURL: deadAddr}
	proxy := buildProxy(t, router, &http.Client{Timeout: 2 * time.Second})

	req := httptest.NewRequest(http.MethodPost, "/v1/statement", strings.NewReader("SELECT 1"))
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadGateway, w.Code, "unreachable backend must surface as 502")
}

// --- Degradation: backend slow / timeout ---

// TestProxy_Degradation_BackendTimeout verifies that an upstream slower than the
// client timeout produces a 502 (rather than hanging or panicking).
func TestProxy_Degradation_BackendTimeout(t *testing.T) {
	t.Parallel()

	// Backend that blocks long enough for the client to time out, then exits.
	// A hard ceiling keeps the handler goroutine bounded so goleak stays clean
	// even if the connection-close cancellation is delayed.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(500 * time.Millisecond):
		}
	}))
	defer upstream.Close()

	router := &fakeRouter{backendURL: upstream.URL}
	// Aggressive client timeout to keep the test fast.
	slowClient := &http.Client{Timeout: 50 * time.Millisecond}
	proxy := buildProxy(t, router, slowClient)

	req := httptest.NewRequest(http.MethodPost, "/v1/statement", strings.NewReader("SELECT 1"))
	w := httptest.NewRecorder()

	start := time.Now()
	proxy.ServeHTTP(w, req)
	elapsed := time.Since(start)

	assert.Equal(t, http.StatusBadGateway, w.Code, "client timeout must surface as 502")
	assert.Less(t, elapsed, 500*time.Millisecond, "proxy must not hang past the client timeout")
}

// --- Degradation: oversize response body ---

// TestProxy_Degradation_OversizeResponseBody verifies that an upstream response
// larger than Proxy.ResponseSize.Bytes is rejected with 502 rather than forwarded.
// This exercises the `len(buf) > limit` branch in forward.go:54.
func TestProxy_Degradation_OversizeResponseBody(t *testing.T) {
	t.Parallel()

	const limit int64 = 128
	oversize := strings.Repeat("a", int(limit*4))

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, oversize)
	}))
	defer upstream.Close()

	router := &fakeRouter{backendURL: upstream.URL}
	proxy := New(Config{
		Proxy:  config.ProxyConfig{ResponseSize: config.DataSize{Bytes: limit}},
		Cookie: config.CookieConfig{WireCompat: true},
		Client: upstream.Client(),
		Router: router,
		Log:    discardLogger(),
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/statement", strings.NewReader("SELECT 1"))
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadGateway, w.Code, "oversize body must surface as 502")
	assert.NotContains(t, w.Body.String(), oversize, "oversize body must not be forwarded to client")
}

// --- Degradation: malformed JSON in /v1/statement response ---

// TestProxy_Degradation_MalformedJSONBody verifies that an upstream response with
// invalid JSON is forwarded to the client (Hard Invariant #1 — never rewrite the
// body), but the queryID extraction fails silently and WriteCache is NOT called.
func TestProxy_Degradation_MalformedJSONBody(t *testing.T) {
	t.Parallel()

	const garbage = `this is not json {{{`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, garbage)
	}))
	defer upstream.Close()

	router := &fakeRouter{backendURL: upstream.URL}
	proxy := buildProxy(t, router, upstream.Client())

	req := httptest.NewRequest(http.MethodPost, "/v1/statement", strings.NewReader("SELECT 1"))
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "malformed body must still be forwarded with upstream status")
	assert.Equal(t, garbage, w.Body.String(), "Hard Invariant #1: body must be forwarded byte-for-byte even if unparseable")

	// Router should have been called for Route but NOT for WriteCache.
	calls := router.CallOrder()
	require.NotEmpty(t, calls, "Route must have been called")
	for _, c := range calls {
		assert.False(t, strings.HasPrefix(c, "WriteCache:"),
			"WriteCache must not be called when queryID extraction fails: got %v", calls)
	}
}

// --- Concurrency: many concurrent requests share the proxy safely ---

// TestProxy_Concurrency_ManyConcurrentRequests verifies the proxy correctly handles
// many in-flight requests in parallel under -race. Smoke test for shared-state races
// in the Router interface contract and chi router.
func TestProxy_Concurrency_ManyConcurrentRequests(t *testing.T) {
	t.Parallel()

	const (
		numClients     = 32
		reqsPerClient  = 10
		bodyPerRequest = `{"id":"q_concurrent","nextUri":"http://up/v1/statement/q_concurrent/1"}`
	)

	var upstreamHits int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&upstreamHits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, bodyPerRequest)
	}))
	defer upstream.Close()

	router := &fakeRouter{backendURL: upstream.URL}
	proxy := buildProxy(t, router, upstream.Client())

	var wg sync.WaitGroup
	wg.Add(numClients)
	for i := 0; i < numClients; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < reqsPerClient; j++ {
				req := httptest.NewRequest(http.MethodPost, "/v1/statement", strings.NewReader("SELECT 1"))
				w := httptest.NewRecorder()
				proxy.ServeHTTP(w, req)
				if w.Code != http.StatusOK {
					t.Errorf("expected 200, got %d", w.Code)
					return
				}
			}
		}()
	}
	wg.Wait()

	want := int64(numClients * reqsPerClient)
	assert.Equal(t, want, atomic.LoadInt64(&upstreamHits),
		"every concurrent request must reach the upstream exactly once")
}

// --- Routing-result handling: BackendURL forwarding (formerly mis-named Seam4) ---

// TestProxy_Routing_NonEmptyBackendURLForwards verifies the proxy forwards to whatever
// backend URL the Router returns, regardless of whether the result came from the external
// selector or the recovery chain.
//
// NOTE: This is a *routing-result handling* test, not a recovery-chain test. The actual
// three-step recovery chain (cache → history → HEAD probe → first-active) is covered in
// internal/routing/routing_test.go. The proxy's only responsibility here is "use the
// BackendURL the Router gave me". See the prior TestProxy_Seam4_ThreeStepRecoveryChain
// for the originally-named version; QA found that name misleading because the proxy has
// no visibility into which recovery step produced the URL.
func TestProxy_Routing_NonEmptyBackendURLForwards(t *testing.T) {
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

	assert.Equal(t, http.StatusOK, w.Code)
}

// TestProxy_Routing_EmptyBackendURLFailsClosed verifies that if Router returns an empty
// BackendURL (recovery chain exhausted, all backends down), the proxy fails closed with
// 502 rather than dialing the empty string or panicking.
func TestProxy_Routing_EmptyBackendURLFailsClosed(t *testing.T) {
	t.Parallel()

	proxy := New(Config{
		Proxy:  config.ProxyConfig{ResponseSize: config.DataSize{Bytes: 1_048_576}},
		Cookie: config.CookieConfig{WireCompat: true},
		Client: &http.Client{Timeout: time.Second},
		Router: &emptyResultRouter{},
		Log:    discardLogger(),
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/statement", strings.NewReader("SELECT 1"))
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadGateway, w.Code,
		"empty BackendURL from Router (recovery exhausted) must fail closed with 502")
}

// emptyResultRouter returns a RouteResult with an empty BackendURL, simulating
// "external router + 3-step recovery chain all came back empty".
type emptyResultRouter struct{}

func (*emptyResultRouter) Route(_ context.Context, _ *routing.RouteInput) (*routing.RouteResult, error) {
	return &routing.RouteResult{}, nil
}

func (*emptyResultRouter) WriteCache(_, _ string) {}
