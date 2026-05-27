package routing

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/config"
)

// --- Fakes ---

type fakeHistory struct {
	data map[string]string
}

func (f *fakeHistory) LookupByQueryID(_ context.Context, queryID string) (string, error) {
	url, _ := f.data[queryID]
	return url, nil
}

type fakeBackends struct {
	backends []ActiveBackend
}

func (f *fakeBackends) ListActive(_ context.Context) ([]ActiveBackend, error) {
	return f.backends, nil
}

// discardLogger returns a slog.Logger that discards all output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// buildRouter builds a Router with fake dependencies for unit testing.
func buildRouter(t *testing.T, routerClient *http.Client, history HistoryLookup, bList BackendLister) *Router {
	t.Helper()
	r, err := New(Config{
		Routing: config.RoutingConfig{
			DefaultGroup: "default",
			Type:         "EXTERNAL",
		},
		ExternalClient: routerClient,
		ProbeClient:    &http.Client{},
		History:        history,
		Backends:       bList,
		Log:            discardLogger(),
	})
	require.NoError(t, err)
	return r
}

// --- Cache tests ---

func TestCache_HitAndMiss(t *testing.T) {
	c, err := newQueryCache(16)
	require.NoError(t, err)

	_, ok := c.get("q1")
	assert.False(t, ok, "expected cache miss")

	c.set("q1", "http://backend-a:8080")
	url, ok := c.get("q1")
	assert.True(t, ok)
	assert.Equal(t, "http://backend-a:8080", url)
}

func TestCache_Remove(t *testing.T) {
	c, err := newQueryCache(16)
	require.NoError(t, err)

	c.set("q2", "http://backend-b:8080")
	c.remove("q2")
	_, ok := c.get("q2")
	assert.False(t, ok)
}

// --- KillQuery regex ---

func TestExtractKillQueryID(t *testing.T) {
	tests := []struct {
		body string
		want string
	}{
		{`KILL QUERY '20240101_000000_00001_xxxxx'`, "20240101_000000_00001_xxxxx"},
		{`kill query '20240101_000000_00002_aaaaa'`, "20240101_000000_00002_aaaaa"},
		{`SELECT 1`, ""},
		{``, ""},
	}
	for _, tc := range tests {
		got := extractKillQueryID(tc.body)
		assert.Equal(t, tc.want, got, "body=%q", tc.body)
	}
}

// --- External HTTP selector ---

func TestExternalHTTP_SuccessPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		var body routingGroupExternalBody
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "application/json", body.ContentType)

		resp := externalRouterResponse{
			RoutingGroup:    strPtr("etl"),
			Errors:          []string{},
			ExternalHeaders: map[string]string{"X-Trino-Session": "optimize=true"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	sel := newExternalHTTPSelector(config.ExternalConfig{URL: srv.URL}, srv.Client())
	req := &RouteInput{Method: "POST", RequestURI: "/v1/statement", headers: make(http.Header)}
	group, headers, errs, err := sel.selectGroup(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, "etl", group)
	assert.Equal(t, "optimize=true", headers["X-Trino-Session"])
	assert.Empty(t, errs)
}

func TestExternalHTTP_Non200FallsThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	sel := newExternalHTTPSelector(config.ExternalConfig{URL: srv.URL}, srv.Client())
	req := &RouteInput{Method: "POST", RequestURI: "/v1/statement", headers: make(http.Header)}
	group, _, _, err := sel.selectGroup(context.Background(), req)

	assert.Error(t, err)
	assert.Equal(t, "", group)
}

func TestExternalHTTP_PropagateErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := externalRouterResponse{
			Errors: []string{"unauthorized table access"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	sel := newExternalHTTPSelector(config.ExternalConfig{URL: srv.URL}, srv.Client())
	req := &RouteInput{Method: "POST", RequestURI: "/v1/statement", headers: make(http.Header)}
	_, _, errs, err := sel.selectGroup(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, []string{"unauthorized table access"}, errs)
}

// --- Router.Route integration ---

func TestRouter_CacheHitSkipsExternal(t *testing.T) {
	externalCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		externalCalled = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"routingGroup":"etl","errors":[],"externalHeaders":{}}`))
	}))
	defer srv.Close()

	hist := &fakeHistory{data: map[string]string{}}
	bl := &fakeBackends{backends: []ActiveBackend{
		{Name: "a", URL: "http://trino-a:8080", RoutingGroup: "default"},
	}}
	r := buildRouter(t, srv.Client(), hist, bl)

	const queryID = "20240101_000000_00001_xxxxx"
	r.WriteCache(queryID, "http://trino-a:8080")

	req := &RouteInput{
		Method:     "GET",
		RequestURI: "/v1/query/" + queryID,
		headers:    make(http.Header),
	}
	result, err := r.Route(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, "http://trino-a:8080", result.BackendURL)
	assert.False(t, externalCalled, "external selector must not be called on cache hit")
}

func TestRouter_KillQueryRoutesToHistory(t *testing.T) {
	hist := &fakeHistory{data: map[string]string{
		"20240101_000000_00003_zzzzz": "http://trino-b:8080",
	}}
	bl := &fakeBackends{backends: []ActiveBackend{
		{Name: "b", URL: "http://trino-b:8080", RoutingGroup: "default"},
	}}
	r := buildRouter(t, &http.Client{}, hist, bl)

	req := &RouteInput{
		Method:     "POST",
		RequestURI: "/v1/statement",
		Body:       `KILL QUERY '20240101_000000_00003_zzzzz'`,
		headers:    make(http.Header),
	}
	result, err := r.Route(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, "http://trino-b:8080", result.BackendURL)
}

func TestRouter_FallsBackToFirstActive(t *testing.T) {
	bl := &fakeBackends{backends: []ActiveBackend{
		{Name: "a", URL: "http://trino-a:8080", RoutingGroup: "default"},
	}}
	r := buildRouter(t, &http.Client{}, &fakeHistory{data: map[string]string{}}, bl)

	req := &RouteInput{Method: "GET", RequestURI: "/v1/info", headers: make(http.Header)}
	result, err := r.Route(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, "http://trino-a:8080", result.BackendURL)
}

// --- ExtractQueryID ---

func TestExtractQueryID(t *testing.T) {
	tests := []struct {
		uri  string
		want string
	}{
		{"/v1/query/20240101_000000_00001_xxxxx", "20240101_000000_00001_xxxxx"},
		{"/v1/query/20240101_000000_00001_xxxxx/", "20240101_000000_00001_xxxxx"},
		{"/v1/statement", ""},
		{"/v1/info", ""},
	}
	for _, tc := range tests {
		req := &RouteInput{RequestURI: tc.uri}
		got := extractQueryID(req)
		assert.Equal(t, tc.want, got, "uri=%q", tc.uri)
	}
}

// TestExternalHTTP_ForwardsInboundHeaders verifies that inbound request headers are
// forwarded to the routing service, excluding configured excludeHeaders and Content-Length.
func TestExternalHTTP_ForwardsInboundHeaders(t *testing.T) {
	var receivedHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"routingGroup":"etl","errors":[],"externalHeaders":{}}`))
	}))
	defer srv.Close()

	cfg := config.ExternalConfig{
		URL:            srv.URL,
		ExcludeHeaders: []string{"X-Secret", "Authorization"},
	}
	sel := newExternalHTTPSelector(cfg, srv.Client())

	inboundHeaders := make(http.Header)
	inboundHeaders.Set("X-Trino-User", "alice")
	inboundHeaders.Set("X-Secret", "should-be-stripped")
	inboundHeaders.Set("Authorization", "Bearer token")
	inboundHeaders.Set("Content-Length", "42") // always excluded

	req := &RouteInput{Method: "POST", RequestURI: "/v1/statement", headers: inboundHeaders}
	_, _, _, err := sel.selectGroup(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "alice", receivedHeaders.Get("X-Trino-User"), "X-Trino-User must be forwarded")
	assert.Empty(t, receivedHeaders.Get("X-Secret"), "X-Secret must be excluded")
	assert.Empty(t, receivedHeaders.Get("Authorization"), "Authorization must be excluded")
	// Content-Length is excluded from forwarding; the HTTP transport computes its own
	// value for the outgoing POST body, so we only verify the inbound "42" was not forwarded.
	assert.NotEqual(t, "42", receivedHeaders.Get("Content-Length"), "inbound Content-Length must not be forwarded")
}

// TestRouter_FilterExcludedHeaders verifies that keys in ExcludeHeaders are stripped
// from the externalHeaders map returned by the routing service before being applied
// to the upstream request.
func TestRouter_FilterExcludedHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"routingGroup":"etl","errors":[],"externalHeaders":{"X-Keep":"yes","X-Remove":"no"}}`))
	}))
	defer srv.Close()

	hist := &fakeHistory{data: map[string]string{}}
	bl := &fakeBackends{backends: []ActiveBackend{
		{Name: "a", URL: "http://trino-a:8080", RoutingGroup: "etl"},
	}}
	r, err := New(Config{
		Routing: config.RoutingConfig{
			DefaultGroup: "etl",
			Type:         "EXTERNAL",
			External: config.ExternalConfig{
				URL:            srv.URL,
				ExcludeHeaders: []string{"X-Remove"},
			},
		},
		ExternalClient: srv.Client(),
		ProbeClient:    &http.Client{},
		History:        hist,
		Backends:       bl,
		Log:            discardLogger(),
	})
	require.NoError(t, err)

	req := &RouteInput{Method: "POST", RequestURI: "/v1/statement", headers: make(http.Header)}
	result, err := r.Route(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, "yes", result.ExternalHeaders["X-Keep"], "X-Keep must be present")
	assert.NotContains(t, result.ExternalHeaders, "X-Remove", "X-Remove must be filtered out")
}

func strPtr(s string) *string { return &s }
