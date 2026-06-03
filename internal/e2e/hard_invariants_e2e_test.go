//go:build e2e

package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/e2e/harness"
)

// TestE2E_Inv1_NoBodyRewriting verifies the gateway forwards the upstream
// /v1/statement response body byte-for-byte to the client, with no mutation
// of payload bytes. Hard Invariant #1 — never rewrite Trino's response body.
func TestE2E_Inv1_NoBodyRewriting(t *testing.T) {
	// Pin the backend response so we can assert byte-equality at the client.
	// `id` is needed so the gateway succeeds at cache extraction; everything
	// else is opaque payload that must survive intact.
	payload := []byte(`{"id":"q_inv1_test_id","nextUri":"http://x/v1/statement/q_inv1/1","stats":{"state":"QUEUED"},"opaque":"hellö-wörld-` +
		"\xe2\x98\x83" + `-snowman","preserve":[1,2,3,null,true,"end"]}`)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/info":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"starting":false}`)
		case "/v1/statement":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	h := harness.New(t)
	registerBackend(t, h, "trino-inv1", "default", upstream.URL)

	resp, body := postStatement(t, h, "SELECT 1", nil)
	defer resp.Body.Close()
	require.Equalf(t, http.StatusOK, resp.StatusCode,
		"gateway must return 200 for a well-formed backend response; body=%s", string(body))

	assert.Equal(t, payload, body,
		"Hard Invariant #1: response body bytes must be byte-identical to the backend's")
	assert.Equal(t, len(payload), len(body), "body length must match exactly")
}

// TestE2E_Inv2_NoRedirectFollowing verifies that an upstream 3xx is forwarded
// unchanged to the client — the gateway never follows the redirect.
// Hard Invariant #2 — redirect following is disabled on the proxy client.
func TestE2E_Inv2_NoRedirectFollowing(t *testing.T) {
	const redirectTarget = "http://example.invalid/somewhere-else"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/info":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"starting":false}`)
		case "/v1/statement":
			w.Header().Set("Location", redirectTarget)
			w.WriteHeader(http.StatusMovedPermanently)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	h := harness.New(t)
	registerBackend(t, h, "trino-inv2", "default", upstream.URL)

	resp, _ := postStatement(t, h, "SELECT 1", nil)
	defer resp.Body.Close()

	assert.Equalf(t, http.StatusMovedPermanently, resp.StatusCode,
		"Hard Invariant #2: gateway must forward 301 unchanged, not follow it (got %d)",
		resp.StatusCode)
	assert.Equal(t, redirectTarget, resp.Header.Get("Location"),
		"Location header must be forwarded verbatim from the upstream response")
}

// TestE2E_Inv3_CacheWriteBeforeFlush verifies the queryId→backend cache is
// populated BEFORE the /v1/statement response is flushed to the client.
// Hard Invariant #3 — write-before-flush is what makes sticky GETs work without
// races; an immediate follow-up /v1/query/<id> must land on the same backend.
//
// With two backends in the default group, the cache write must happen before
// the response leaves the gateway, so the follow-up GET must hit the SAME
// backend. If the cache write happened post-flush, the GET could race and
// fall through to the recovery chain's default-group fallback (potentially
// hitting the other backend).
func TestE2E_Inv3_CacheWriteBeforeFlush(t *testing.T) {
	h := harness.New(t)
	fake1 := h.AddBackend(t, "trino-cache-1", "default")
	fake2 := h.AddBackend(t, "trino-cache-2", "default")

	postResp, body := postStatement(t, h, "SELECT 1", nil)
	defer postResp.Body.Close()
	require.Equalf(t, http.StatusOK, postResp.StatusCode,
		"POST /v1/statement must succeed; body=%s", string(body))

	var parsed struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(body, &parsed))
	require.NotEmpty(t, parsed.ID, "response must include queryId")

	// Find the owner backend (the one that generated the queryId).
	var owner, other = fake1, fake2
	if contains(fake2.QueryIDs(), parsed.ID) {
		owner, other = fake2, fake1
	}
	require.Contains(t, owner.QueryIDs(), parsed.ID, "exactly one fake owns the queryId")

	// Issue the sticky GET immediately — no delay, racing the cache write.
	getResp := doGet(t, h, h.ProxyURL+"/v1/query/"+parsed.ID+"/1", nil)
	defer getResp.Body.Close()
	require.Equal(t, http.StatusOK, getResp.StatusCode)

	// The cache must have been written before the POST response was flushed,
	// otherwise the GET could land on the other backend.
	assert.GreaterOrEqualf(t, owner.HitCount(parsed.ID), 1,
		"Hard Invariant #3: immediate sticky GET must hit owner backend (cache must be written before /v1/statement flush)")
	assert.Equalf(t, 0, other.HitCount(parsed.ID),
		"other backend must NOT have received the sticky GET — cache write raced response flush")
}

// TestE2E_Inv4_BoundedBuffering_OnlyStatement verifies that only POST
// /v1/statement is buffered against proxy.responseSize. Streaming paths
// (GET /v1/query/<id>/<token>) forward upstream bodies of any size verbatim.
// Hard Invariant #4 — bounded buffering applies only to the statement
// initiation buffer.
func TestE2E_Inv4_BoundedBuffering_OnlyStatement(t *testing.T) {
	// 2 MiB body served on the streaming path. With proxy.responseSize at the
	// 1 MiB default, this would 502 if /v1/query/* were buffered.
	large := bytes.Repeat([]byte("y"), 2*1024*1024)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/info":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"starting":false}`)
		case strings.HasPrefix(r.URL.Path, "/v1/query/") && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(large)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(upstream.Close)

	h := harness.New(t) // responseSize stays at the 1 MiB default
	registerBackend(t, h, "trino-inv4", "default", upstream.URL)

	resp := doGet(t, h, h.ProxyURL+"/v1/query/q_inv4_stream/1", nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"Hard Invariant #4: streaming path must NOT be bounded by responseSize")

	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equalf(t, len(large), len(got),
		"streamed body must reach the client intact, not truncated (expected %d bytes, got %d)",
		len(large), len(got))
}

// TestE2E_Inv7_HopByHopStripped_BothDirections verifies hop-by-hop headers are
// stripped on BOTH the request and response directions. Hard Invariant #7 —
// the proxy is RFC 7230 §6.1 compliant for hop-by-hop semantics.
//
// Cross-reference: TestE2E_HopByHopStripped_RequestDirection and
// TestE2E_HopByHopStripped_ResponseDirection in proxy_protocol_e2e_test.go
// (Task 39) cover each direction in isolation. This test exercises both at
// once so the invariant-traceability matrix maps to a TestE2E_Inv7_* symbol
// AND so the go-qa "both directions" gap is closed under a single name.
func TestE2E_Inv7_HopByHopStripped_BothDirections(t *testing.T) {
	// Custom upstream sets hop-by-hop headers on its response and echoes any
	// inbound ones into X-Echo-* so we can also assert the request direction.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/info":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"starting":false}`)
		case "/v1/statement":
			if v := r.Header.Get("Connection"); v != "" {
				w.Header().Set("X-Echo-Connection", v)
			}
			if v := r.Header.Get("Keep-Alive"); v != "" {
				w.Header().Set("X-Echo-Keep-Alive", v)
			}
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("Keep-Alive", "timeout=5")
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w,
				`{"id":"q_inv7","nextUri":"http://x/v1/statement/q_inv7/1","stats":{"state":"QUEUED"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	h := harness.New(t)
	registerBackend(t, h, "trino-inv7", "default", upstream.URL)

	req := newPostRequest(t, h, "SELECT 1")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Keep-Alive", "timeout=5")
	req.Header.Set("Te", "trailers")

	resp, err := h.ProxyClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Equalf(t, http.StatusOK, resp.StatusCode, "body=%s", string(body))

	// Request direction: gateway must have stripped Connection/Keep-Alive
	// before forwarding to upstream. Upstream echoes them via X-Echo-* so we
	// can confirm absence.
	assert.Empty(t, resp.Header.Get("X-Echo-Connection"),
		"Hard Invariant #7 (request direction): Connection must be stripped before upstream")
	assert.Empty(t, resp.Header.Get("X-Echo-Keep-Alive"),
		"Hard Invariant #7 (request direction): Keep-Alive must be stripped before upstream")

	// Response direction: gateway must have stripped Connection/Keep-Alive
	// set by the backend before sending the response to the client.
	assert.Empty(t, resp.Header.Get("Connection"),
		"Hard Invariant #7 (response direction): Connection must be stripped on client-bound response")
	assert.Empty(t, resp.Header.Get("Keep-Alive"),
		"Hard Invariant #7 (response direction): Keep-Alive must be stripped on client-bound response")
	assert.Empty(t, resp.Header.Get("Transfer-Encoding"),
		"Hard Invariant #7 (response direction): Transfer-Encoding must be stripped on client-bound response")
}

// TestE2E_Inv8_XForwardedForAppends is a thin cross-reference test for Hard
// Invariant #8. The substantive assertion lives in
// TestE2E_ForwardedHeaders_XForwardedForAppends in proxy_protocol_e2e_test.go
// (Task 39): a pre-existing X-Forwarded-For value must be appended-to, not
// replaced. This minimal re-run keeps the invariant-traceability matrix
// (docs/USE_STORIES.md §Hard Invariants) mapped to a TestE2E_Inv8_* symbol.
func TestE2E_Inv8_XForwardedForAppends(t *testing.T) {
	h := harness.New(t)
	fake := h.AddBackend(t, "trino-inv8", "default")

	req := newPostRequest(t, h, "SELECT 1")
	req.Header.Set("X-Forwarded-For", "203.0.113.7")

	resp, err := h.ProxyClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body=%s", string(body))

	var parsed struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(body, &parsed))
	require.NotEmpty(t, parsed.ID)

	headers := fake.ReceivedHeaders(parsed.ID)
	require.NotNil(t, headers)

	xff := headers.Get("X-Forwarded-For")
	assert.Containsf(t, xff, "203.0.113.7",
		"Hard Invariant #8: original X-Forwarded-For entry must be preserved (got %q)", xff)
	assert.Containsf(t, xff, ",",
		"Hard Invariant #8: client IP must be appended after the existing value (got %q)", xff)
}

// TestE2E_Inv9_ExternalHeadersReplace verifies that headers returned in the
// external router's externalHeaders map REPLACE (not merge with) headers sent
// by the client. Hard Invariant #9 — externalHeaders use Set, not Add.
//
// Setup: a mock external router returns {"X-Custom":"router-value"} for every
// request. The client sends X-Custom: client-value. The backend must observe
// X-Custom: router-value ONLY, with no trace of client-value.
func TestE2E_Inv9_ExternalHeadersReplace(t *testing.T) {
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w,
			`{"routingGroup":"default","errors":[],"externalHeaders":{"X-Custom":"router-value"}}`)
	}))
	t.Cleanup(router.Close)

	h := harness.New(t, harness.WithExternalHTTPRouter(router.URL))
	fake := h.AddBackend(t, "trino-inv9", "default")

	req := newPostRequest(t, h, "SELECT 1")
	req.Header.Set("X-Custom", "client-value")

	resp, err := h.ProxyClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Equalf(t, http.StatusOK, resp.StatusCode, "body=%s", string(body))

	var parsed struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(body, &parsed))
	require.NotEmpty(t, parsed.ID)

	headers := fake.ReceivedHeaders(parsed.ID)
	require.NotNil(t, headers)

	// http.Header preserves all values; with REPLACE semantics there must be
	// exactly one, equal to the router-supplied value.
	got := headers.Values("X-Custom")
	require.Lenf(t, got, 1,
		"Hard Invariant #9: X-Custom must have exactly one value after REPLACE (got %v)", got)
	assert.Equal(t, "router-value", got[0],
		"Hard Invariant #9: external router value must REPLACE client value (no merge)")
}

// TestE2E_Inv11_ReadyzRequiresProbe is a cross-reference test. The substantive
// assertion lives in TestE2E_Readyz_503BeforeFirstProbe in probes_e2e_test.go
// (Task 46). This test runs a minimal version inline so the invariant
// traceability matrix maps to a TestE2E_Inv11_* symbol.
func TestE2E_Inv11_ReadyzRequiresProbe(t *testing.T) {
	h := harness.New(t,
		harness.WithSkipReadyzWait(),
		harness.WithMonitorInterval(5*time.Minute),
	)

	resp, err := http.Get(h.AdminURL + "/trino-gateway/readyz")
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	assert.Equalf(t, http.StatusServiceUnavailable, resp.StatusCode,
		"Hard Invariant #11: /trino-gateway/readyz must return 503 before the first probe completes (got %d)",
		resp.StatusCode)
}

// TestE2E_Inv12_ThreeHTTPClients_BehavioralSaturation verifies that the proxy,
// router, and monitor http.Clients are isolated such that saturating one does
// not starve the others. Hard Invariant #12 — three distinct client pools.
//
// Saturation strategy: a slow external router (sleeps 500ms per call) is wired
// in. We fire N concurrent POST /v1/statement requests through the proxy; each
// blocks for ~500ms inside the router selector. While those are in flight, we
// poll /trino-gateway/livez on the admin port and assert it responds promptly
// (<200ms per call). A shared transport/connection pool between proxy and
// admin-side clients would have admin requests piling up behind router calls.
//
// Caveat: the admin port is a separate http.Server in the same process, so
// this test would also pass if the gateway used a single shared http.Client —
// the stronger isolation property (proxy vs router transport sharing) is
// verified in TestProxy_Seam7_ThreeClientPoolIsolation (DI check) and by the
// architectural invariant that main.go wires three distinct *http.Client
// instances. This E2E test guards against process-wide pool contention only.
func TestE2E_Inv12_ThreeHTTPClients_BehavioralSaturation(t *testing.T) {
	const (
		routerLatency   = 500 * time.Millisecond
		concurrentPosts = 20
		livezBudget     = 200 * time.Millisecond
	)

	var inFlight atomic.Int64

	slowRouter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		inFlight.Add(1)
		defer inFlight.Add(-1)
		time.Sleep(routerLatency)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"routingGroup":"default","errors":[],"externalHeaders":{}}`)
	}))
	t.Cleanup(slowRouter.Close)

	// External router timeout must exceed routerLatency so the proxy waits.
	h := harness.New(t,
		harness.WithExternalHTTPRouter(slowRouter.URL),
		harness.WithExternalTimeout(2*time.Second),
	)
	h.AddBackend(t, "trino-inv12", "default")

	// Fire concurrent POSTs in the background; goroutines exit when the
	// gateway returns (router latency dominates, ~500ms each).
	var wg sync.WaitGroup
	wg.Add(concurrentPosts)
	for i := 0; i < concurrentPosts; i++ {
		go func() {
			defer wg.Done()
			req := newPostRequest(t, h, "SELECT 1")
			resp, err := h.ProxyClient().Do(req)
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}()
	}
	// Block test exit until in-flight requests drain.
	t.Cleanup(wg.Wait)

	// Wait briefly for in-flight router calls to ramp up, then sample livez.
	deadline := time.Now().Add(routerLatency / 2)
	for time.Now().Before(deadline) && inFlight.Load() < concurrentPosts/2 {
		time.Sleep(10 * time.Millisecond)
	}
	require.GreaterOrEqualf(t, inFlight.Load(), int64(1),
		"expected at least one concurrent router call in flight before sampling livez (got %d)",
		inFlight.Load())

	// Take several livez samples while saturation is in progress.
	const samples = 5
	livezClient := &http.Client{Timeout: 2 * time.Second}
	for i := 0; i < samples; i++ {
		start := time.Now()
		resp, err := livezClient.Get(h.AdminURL + "/trino-gateway/livez")
		elapsed := time.Since(start)
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()

		require.Equal(t, http.StatusOK, resp.StatusCode,
			"livez must always be 200 even under router saturation")
		assert.Lessf(t, elapsed, livezBudget,
			"Hard Invariant #12: livez must respond <%s under router saturation; got %s on sample %d (router in-flight=%d)",
			livezBudget, elapsed, i, inFlight.Load())
	}
}
