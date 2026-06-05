package routing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/config"
)

// fakeRouterMetrics captures routing metric calls for assertions.
type fakeRouterMetrics struct {
	mu            sync.Mutex
	calls         []string // "transport:outcome"
	cacheEvents   []string
	recoverySteps []string
	killQueries   int
}

func (f *fakeRouterMetrics) RouterCall(transport, outcome string, _ float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, transport+":"+outcome)
}

func (f *fakeRouterMetrics) CacheEvent(event string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cacheEvents = append(f.cacheEvents, event)
}

func (f *fakeRouterMetrics) RecoveryStep(step string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recoverySteps = append(f.recoverySteps, step)
}

func (f *fakeRouterMetrics) KillQueryRoute() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killQueries++
}

func buildRouterWithMetrics(t *testing.T, ext config.ExternalConfig, client *http.Client, hist HistoryLookup, bl BackendLister, m RouterMetrics) *Router {
	t.Helper()
	r, err := New(Config{
		Routing: config.RoutingConfig{
			DefaultGroup: "default",
			Type:         "EXTERNAL",
			External:     ext,
		},
		ExternalClient: client,
		ProbeClient:    &http.Client{},
		History:        hist,
		Backends:       bl,
		Metrics:        m,
		Log:            discardLogger(),
	})
	require.NoError(t, err)
	return r
}

func TestRouterMetrics_CacheHitAndMiss(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"routingGroup":"etl","errors":[],"externalHeaders":{}}`))
	}))
	defer srv.Close()

	bl := &fakeBackends{backends: []ActiveBackend{{Name: "a", URL: "http://trino-a:8080", RoutingGroup: "etl"}}}
	m := &fakeRouterMetrics{}
	r := buildRouterWithMetrics(t, config.ExternalConfig{URL: srv.URL}, srv.Client(), &fakeHistory{data: map[string]string{}}, bl, m)

	const queryID = "20240101_000000_00001_xxxxx"

	// Miss: no cache entry, falls through to external (http ok).
	missReq := &RouteInput{Method: "GET", RequestURI: "/v1/query/" + queryID, headers: make(http.Header)}
	_, err := r.Route(context.Background(), missReq)
	require.NoError(t, err)

	// Hit: prime the cache, route again.
	r.WriteCache(queryID, "http://trino-a:8080")
	hitReq := &RouteInput{Method: "GET", RequestURI: "/v1/query/" + queryID, headers: make(http.Header)}
	_, err = r.Route(context.Background(), hitReq)
	require.NoError(t, err)

	assert.Equal(t, []string{CacheEventMiss, CacheEventHit}, m.cacheEvents)
	assert.Contains(t, m.calls, TransportHTTP+":"+RouterOutcomeOK)
}

func TestRouterMetrics_KillQuery(t *testing.T) {
	hist := &fakeHistory{data: map[string]string{"20240101_000000_00003_zzzzz": "http://trino-b:8080"}}
	bl := &fakeBackends{backends: []ActiveBackend{{Name: "b", URL: "http://trino-b:8080", RoutingGroup: "default"}}}
	m := &fakeRouterMetrics{}
	r := buildRouterWithMetrics(t, config.ExternalConfig{}, &http.Client{}, hist, bl, m)

	req := &RouteInput{Method: "POST", RequestURI: "/v1/statement", Body: `KILL QUERY '20240101_000000_00003_zzzzz'`, headers: make(http.Header)}
	_, err := r.Route(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, 1, m.killQueries)
	// Recovery step "history" resolved the kill-query backend.
	assert.Contains(t, m.recoverySteps, RecoveryStepHistory)
}

func TestRouterMetrics_RecoveryDefaultStep(t *testing.T) {
	// No external config, no history match → recovery chain falls to first-active default.
	hist := &fakeHistory{data: map[string]string{}}
	bl := &fakeBackends{backends: []ActiveBackend{{Name: "a", URL: "http://trino-a:8080", RoutingGroup: "other"}}}
	m := &fakeRouterMetrics{}
	r := buildRouterWithMetrics(t, config.ExternalConfig{}, &http.Client{}, hist, bl, m)

	// queryID present so recovery chain is attempted; group "default" has no backend.
	req := &RouteInput{Method: "GET", RequestURI: "/v1/query/20240101_000000_00009_aaaaa", headers: make(http.Header)}
	_, err := r.Route(context.Background(), req)
	require.NoError(t, err)

	assert.Contains(t, m.recoverySteps, RecoveryStepDefault)
}
