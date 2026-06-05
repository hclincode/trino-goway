package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hclincode/trino-goway/internal/config"
	"github.com/hclincode/trino-goway/internal/routing"
)

// fakeMetricsRecorder captures proxy metric calls for assertions.
type fakeMetricsRecorder struct {
	mu          sync.Mutex
	requests    []requestRecord
	durations   []string // backends observed
	oversized   int
	cacheWrites int
}

type requestRecord struct {
	backend, routingGroup, outcome string
}

func (f *fakeMetricsRecorder) RequestHandled(backend, routingGroup, outcome string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, requestRecord{backend, routingGroup, outcome})
}

func (f *fakeMetricsRecorder) UpstreamDuration(backend string, _ float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.durations = append(f.durations, backend)
}

func (f *fakeMetricsRecorder) OversizedResponse() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.oversized++
}

func (f *fakeMetricsRecorder) StatementCacheWrite() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cacheWrites++
}

// buildProxyWithMetrics builds a Proxy wired with a metrics recorder and a fixed
// response size limit.
func buildProxyWithMetrics(t *testing.T, router Router, client *http.Client, rec MetricsRecorder, limit int64) *Proxy {
	t.Helper()
	return New(Config{
		Proxy:           config.ProxyConfig{ResponseSize: config.DataSize{Bytes: limit}},
		Cookie:          config.CookieConfig{WireCompat: true},
		Client:          client,
		Router:          router,
		MetricsRecorder: rec,
		Log:             discardLogger(),
	})
}

// outcomeRouter returns a fixed RouteResult, letting tests drive the outcome label.
type outcomeRouter struct {
	result *routing.RouteResult
}

func (r *outcomeRouter) Route(_ context.Context, _ *routing.RouteInput) (*routing.RouteResult, error) {
	return r.result, nil
}
func (r *outcomeRouter) WriteCache(string, string) {}

func TestProxy_Metrics_SuccessRecordsOutcomeAndCacheWrite(t *testing.T) {
	t.Parallel()

	const queryID = "20240101_000000_00001_aaaaa"
	body := `{"id":"` + queryID + `","nextUri":"http://x/y"}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	defer upstream.Close()

	rec := &fakeMetricsRecorder{}
	router := &outcomeRouter{result: &routing.RouteResult{
		BackendURL:   upstream.URL,
		RoutingGroup: "etl",
		Outcome:      routing.OutcomeFallback,
	}}
	p := buildProxyWithMetrics(t, router, upstream.Client(), rec, 1_048_576)

	req := httptest.NewRequest(http.MethodPost, "/v1/statement", strings.NewReader("SELECT 1"))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, []requestRecord{{upstream.URL, "etl", routing.OutcomeFallback}}, rec.requests)
	assert.Equal(t, []string{upstream.URL}, rec.durations)
	assert.Equal(t, 1, rec.cacheWrites, "cache write must be recorded for a queryId response")
	assert.Equal(t, 0, rec.oversized)
}

func TestProxy_Metrics_OversizedRecordsErrorAndOversized(t *testing.T) {
	t.Parallel()

	// Upstream returns a body larger than the 16-byte limit.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, strings.Repeat("x", 1024))
	}))
	defer upstream.Close()

	rec := &fakeMetricsRecorder{}
	router := &outcomeRouter{result: &routing.RouteResult{
		BackendURL: upstream.URL, RoutingGroup: "g", Outcome: routing.OutcomeOK,
	}}
	p := buildProxyWithMetrics(t, router, upstream.Client(), rec, 16)

	req := httptest.NewRequest(http.MethodPost, "/v1/statement", strings.NewReader("SELECT 1"))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadGateway, w.Code)
	assert.Equal(t, 1, rec.oversized)
	assert.Equal(t, []requestRecord{{upstream.URL, "g", routing.OutcomeError}}, rec.requests)
	assert.Equal(t, 0, rec.cacheWrites)
}

func TestProxy_Metrics_NoBackendRecordsError(t *testing.T) {
	t.Parallel()

	rec := &fakeMetricsRecorder{}
	router := &outcomeRouter{result: &routing.RouteResult{BackendURL: ""}}
	p := buildProxyWithMetrics(t, router, http.DefaultClient, rec, 1_048_576)

	req := httptest.NewRequest(http.MethodPost, "/v1/statement", strings.NewReader("SELECT 1"))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadGateway, w.Code)
	assert.Equal(t, []requestRecord{{"", "", routing.OutcomeError}}, rec.requests)
	assert.Empty(t, rec.durations)
}

func TestProxy_Metrics_NilRecorderIsNoOp(t *testing.T) {
	t.Parallel()

	const queryID = "20240101_000000_00002_bbbbb"
	body := `{"id":"` + queryID + `"}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	defer upstream.Close()

	router := &outcomeRouter{result: &routing.RouteResult{BackendURL: upstream.URL, Outcome: routing.OutcomeOK}}
	// nil recorder explicitly.
	p := buildProxyWithMetrics(t, router, upstream.Client(), nil, 1_048_576)

	req := httptest.NewRequest(http.MethodPost, "/v1/statement", strings.NewReader("SELECT 1"))
	w := httptest.NewRecorder()
	// Must not panic.
	p.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}
