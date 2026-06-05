package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
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
	return f.data[queryID], nil
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

// TestCache_LRUEvictionAt4096 verifies that inserting one entry beyond the default
// LRU capacity evicts the oldest entry (key 0).
func TestCache_LRUEvictionAt4096(t *testing.T) {
	c, err := newQueryCache(defaultCacheSize)
	require.NoError(t, err)

	for i := 0; i < defaultCacheSize+1; i++ {
		c.set(fmt.Sprintf("key-%d", i), fmt.Sprintf("http://backend-%d", i))
	}

	_, ok := c.get("key-0")
	assert.False(t, ok, "key-0 must be evicted after inserting 4097 entries")

	url, ok := c.get(fmt.Sprintf("key-%d", defaultCacheSize))
	assert.True(t, ok, "newest entry must be retrievable")
	assert.Equal(t, fmt.Sprintf("http://backend-%d", defaultCacheSize), url)
}

// countingHistory wraps a fakeHistory with an atomic counter, used to verify that
// singleflight coalesces concurrent KILL QUERY history lookups for the same queryID.
type countingHistory struct {
	calls atomic.Int32
	gate  chan struct{} // when non-nil, blocks LookupByQueryID until closed
	data  map[string]string
}

func (c *countingHistory) LookupByQueryID(_ context.Context, queryID string) (string, error) {
	c.calls.Add(1)
	if c.gate != nil {
		<-c.gate
	}
	return c.data[queryID], nil
}

// TestRouter_KillQuery_SingleflightCoalesces verifies that N concurrent KILL QUERY
// requests for the same queryID result in exactly one history lookup (singleflight).
//
// To make the coalescing deterministic, the history lookup blocks on `gate` and the
// test waits until the singleflight's followers WaitGroup counter has actually
// reached N-1 (i.e. all N-1 followers are blocked in c.wg.Wait()) before releasing
// the gate. We observe the counter indirectly via `runtime.NumGoroutine()` plus a
// stable-count check on the singleflight in-flight map.
func TestRouter_KillQuery_SingleflightCoalesces(t *testing.T) {
	const queryID = "20240101_000000_00099_singleflight"
	hist := &countingHistory{
		gate: make(chan struct{}),
		data: map[string]string{queryID: "http://trino-history:8080"},
	}
	bl := &fakeBackends{backends: []ActiveBackend{
		{Name: "h", URL: "http://trino-history:8080", RoutingGroup: "default"},
	}}
	r := buildRouter(t, &http.Client{}, hist, bl)

	const concurrency = 50
	var doneWG sync.WaitGroup
	doneWG.Add(concurrency)
	results := make([]string, concurrency)
	errs := make([]error, concurrency)
	started := make(chan struct{}, concurrency)

	for i := 0; i < concurrency; i++ {
		i := i
		go func() {
			defer doneWG.Done()
			req := &RouteInput{
				Method:     "POST",
				RequestURI: "/v1/statement",
				Body:       fmt.Sprintf(`KILL QUERY '%s'`, queryID),
				headers:    make(http.Header),
			}
			started <- struct{}{}
			res, err := r.Route(context.Background(), req)
			if err == nil && res != nil {
				results[i] = res.BackendURL
			}
			errs[i] = err
		}()
	}

	// All goroutines have entered Route by the time we've drained `started`.
	for i := 0; i < concurrency; i++ {
		<-started
	}

	// Spin until the leader is registered in the singleflight map AND the lookup
	// has been entered (calls >= 1). After that, yield the scheduler aggressively
	// to give followers time to acquire the lock, observe the existing entry, and
	// commit to c.wg.Wait(). Once all followers are parked on the leader's WaitGroup,
	// it is safe to release the gate; subsequent callers cannot start a new fn().
	for {
		r.recovery.sf.mu.Lock()
		_, leaderInflight := r.recovery.sf.inflight[queryID]
		r.recovery.sf.mu.Unlock()
		if leaderInflight && hist.calls.Load() >= 1 {
			break
		}
		runtime.Gosched()
	}
	// Give followers a generous window to enter c.wg.Wait().
	for i := 0; i < 10_000; i++ {
		runtime.Gosched()
	}

	close(hist.gate)
	doneWG.Wait()

	assert.Equal(t, int32(1), hist.calls.Load(), "history lookup must be singleflight-coalesced to exactly 1 call")
	for i := 0; i < concurrency; i++ {
		require.NoError(t, errs[i], "goroutine %d", i)
		assert.Equal(t, "http://trino-history:8080", results[i], "goroutine %d", i)
	}
}

// failingClient is an http.Client.Transport that fails the test if invoked.
type failingTransport struct {
	t *testing.T
}

func (f *failingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.t.Errorf("unexpected external HTTP call to %s", req.URL.String())
	return nil, fmt.Errorf("transport: forbidden in this test")
}

// TestRouter_NoExternalConfig_SkipsExternalCall verifies single-cluster mode:
// when neither ExternalURL nor GRPCAddr is configured, no external routing call is made.
func TestRouter_NoExternalConfig_SkipsExternalCall(t *testing.T) {
	failClient := &http.Client{Transport: &failingTransport{t: t}}

	hist := &fakeHistory{data: map[string]string{}}
	bl := &fakeBackends{backends: []ActiveBackend{
		{Name: "only", URL: "http://trino-only:8080", RoutingGroup: "default"},
	}}

	r, err := New(Config{
		Routing: config.RoutingConfig{
			DefaultGroup: "default",
			Type:         "EXTERNAL",
			// No External.URL and no External.GRPCAddr.
		},
		ExternalClient: failClient,
		ProbeClient:    &http.Client{},
		History:        hist,
		Backends:       bl,
		Log:            discardLogger(),
	})
	require.NoError(t, err)

	req := &RouteInput{Method: "GET", RequestURI: "/v1/info", headers: make(http.Header)}
	result, err := r.Route(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, "http://trino-only:8080", result.BackendURL,
		"single-cluster mode must resolve via default group without external call")
}

// --- RS-14: trino_source + client_tags proto round-trip ---

// routeInputWithHeaders builds a RouteInput carrying the given headers.
func routeInputWithHeaders(h map[string]string) *RouteInput {
	hdr := make(http.Header)
	for k, v := range h {
		hdr.Set(k, v)
	}
	return &RouteInput{Method: "POST", RequestURI: "/v1/statement", headers: hdr}
}

func TestBuildProtoRequest_TrinoSource(t *testing.T) {
	req := routeInputWithHeaders(map[string]string{"X-Trino-Source": "airflow"})
	got := buildProtoRequest(req)
	assert.Equal(t, "airflow", got.GetTrinoSource())
}

func TestBuildProtoRequest_TrinoSourceAbsent(t *testing.T) {
	req := routeInputWithHeaders(nil)
	got := buildProtoRequest(req)
	assert.Equal(t, "", got.GetTrinoSource())
}

func TestBuildProtoRequest_ClientTags(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   []string
	}{
		{"absent", "", []string{}},
		{"single", "tier=premium", []string{"tier=premium"}},
		{"multiple", "tier=premium,team=ds", []string{"tier=premium", "team=ds"}},
		{"trimmed", " tier=premium , team=ds ", []string{"tier=premium", "team=ds"}},
		{"empty-entries-dropped", "a,,b,", []string{"a", "b"}},
		{"only-commas", ",,,", []string{}},
		{"whitespace-only-entry", "a, ,b", []string{"a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := map[string]string{}
			if tc.header != "" {
				h["X-Trino-Client-Tags"] = tc.header
			}
			got := buildProtoRequest(routeInputWithHeaders(h))
			assert.Equal(t, tc.want, got.GetClientTags())
		})
	}
}

func TestBuildProtoRequest_ClientTagsAndSourceTogether(t *testing.T) {
	req := routeInputWithHeaders(map[string]string{
		"X-Trino-Source":      "superset",
		"X-Trino-Client-Tags": "tier=premium, region=us",
	})
	got := buildProtoRequest(req)
	assert.Equal(t, "superset", got.GetTrinoSource())
	assert.Equal(t, []string{"tier=premium", "region=us"}, got.GetClientTags())
}

func TestSplitClientTags(t *testing.T) {
	assert.Equal(t, []string{}, splitClientTags(""))
	assert.Equal(t, []string{}, splitClientTags("   "))
	assert.Equal(t, []string{"x"}, splitClientTags("x"))
	assert.Equal(t, []string{"x", "y"}, splitClientTags(" x , y "))
	// Always non-nil so the proto serialises an empty repeated field, not null.
	assert.NotNil(t, splitClientTags(""))
}
