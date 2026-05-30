//go:build e2e

package e2e_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/e2e/harness"
)

// TestE2E_PostStatement_RoutesToBackend verifies that a POST /v1/statement is
// forwarded to the single registered backend and that the gateway echoes back
// a valid Trino response JSON.
func TestE2E_PostStatement_RoutesToBackend(t *testing.T) {
	h := harness.New(t)
	fake := h.AddBackend(t, "trino-1", "default")

	resp, body := postStatement(t, h, "SELECT 1", nil)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode, "gateway POST /v1/statement: body=%s", string(body))

	var parsed struct {
		ID      string `json:"id"`
		NextURI string `json:"nextUri"`
	}
	require.NoError(t, json.Unmarshal(body, &parsed), "parse Trino JSON: %s", string(body))
	assert.NotEmpty(t, parsed.ID, "response must include id")
	assert.NotEmpty(t, parsed.NextURI, "response must include nextUri")

	ids := fake.QueryIDs()
	require.Len(t, ids, 1, "fake backend should have received exactly one POST")
	assert.Equal(t, parsed.ID, ids[0], "queryId returned to client must match backend-generated id")
}

// TestE2E_PostStatement_StickyRouting verifies that after the gateway routes a
// POST /v1/statement to one backend, the subsequent sticky GET for the same
// queryId lands on the same backend (cache hit), not on the other registered
// backend.
func TestE2E_PostStatement_StickyRouting(t *testing.T) {
	h := harness.New(t)
	fake1 := h.AddBackend(t, "trino-1", "default")
	fake2 := h.AddBackend(t, "trino-2", "default")

	postResp, body := postStatement(t, h, "SELECT 1", nil)
	defer postResp.Body.Close()
	require.Equal(t, http.StatusOK, postResp.StatusCode, "POST /v1/statement: body=%s", string(body))

	var parsed struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(body, &parsed))
	require.NotEmpty(t, parsed.ID)
	queryID := parsed.ID

	// Identify which fake actually received the POST.
	var owner, other = fake1, fake2
	if contains(fake2.QueryIDs(), queryID) {
		owner, other = fake2, fake1
	}
	require.Contains(t, owner.QueryIDs(), queryID, "exactly one fake should have generated the queryId")

	// Sticky GET on the same queryId.
	getResp := doGet(t, h, h.ProxyURL+"/v1/query/"+queryID+"/1", nil)
	defer getResp.Body.Close()
	require.Equal(t, http.StatusOK, getResp.StatusCode)

	assert.GreaterOrEqual(t, owner.HitCount(queryID), 1, "owner fake should receive the sticky GET")
	assert.Equal(t, 0, other.HitCount(queryID), "other fake should NOT receive the sticky GET")
}

// TestE2E_PostStatement_ResponseBufferingCap verifies the proxy enforces
// proxy.responseSize on /v1/statement responses (Hard Invariant #4 — bounded
// buffering on the buffering path).
func TestE2E_PostStatement_ResponseBufferingCap(t *testing.T) {
	oversized := strings.Repeat("x", 2048)
	oversizedBody := `{"id":"q_oversize","nextUri":"http://x/v1/statement/q_oversize/1","stats":{"state":"QUEUED"},"pad":"` + oversized + `"}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/info":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"starting":false}`)
		case "/v1/statement":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, oversizedBody)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	h := harness.New(t, harness.WithResponseSize(512))
	registerBackend(t, h, "trino-big", "default", upstream.URL)

	resp, body := postStatement(t, h, "SELECT 1", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode,
		"oversized backend response must produce 502, got %d body=%s", resp.StatusCode, string(body))
}

// TestE2E_PostStatement_NoBackendAvailable verifies a POST /v1/statement with
// zero registered backends returns 502 instead of hanging or 500.
func TestE2E_PostStatement_NoBackendAvailable(t *testing.T) {
	h := harness.New(t)

	resp, body := postStatement(t, h, "SELECT 1", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode,
		"no-backend POST should return 502, got %d body=%s", resp.StatusCode, string(body))
}

// TestE2E_StreamingPath_NotBuffered verifies that GET /v1/query/<id> streams
// upstream bodies larger than proxy.responseSize — only POST /v1/statement is
// bounded by responseSize.
func TestE2E_StreamingPath_NotBuffered(t *testing.T) {
	// 2MiB body served by the streaming-path backend; gateway responseSize
	// stays at the default 1MiB. If the streaming path were buffered, this
	// would return 502.
	large := strings.Repeat("y", 2*1024*1024)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/info":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"starting":false}`)
		case strings.HasPrefix(r.URL.Path, "/v1/query/") && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, large)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(upstream.Close)

	h := harness.New(t)
	registerBackend(t, h, "trino-stream", "default", upstream.URL)

	// Use any queryId — the streaming path forwards to the first-active backend
	// when nothing else matches; the recovery chain's first-active fallback
	// guarantees this backend is selected.
	resp := doGet(t, h, h.ProxyURL+"/v1/query/q_stream_test/1", nil)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode, "GET /v1/query path must not be capped by responseSize")

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, len(large), len(body), "streamed body must reach client intact, not truncated")
}

// TestE2E_ForwardedHeaders_XForwardedHost verifies the gateway injects
// X-Forwarded-Host derived from the inbound Host header.
func TestE2E_ForwardedHeaders_XForwardedHost(t *testing.T) {
	h := harness.New(t)
	fake := h.AddBackend(t, "trino-1", "default")

	req := newPostRequest(t, h, "SELECT 1")
	req.Host = "gateway.example.com:9000"

	resp, err := h.ProxyClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body=%s", string(body))

	var parsed struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(body, &parsed))
	require.NotEmpty(t, parsed.ID)

	headers := fake.ReceivedHeaders(parsed.ID)
	require.NotNil(t, headers, "fake should have recorded headers for queryId %s", parsed.ID)

	// hostOnly strips the port: "gateway.example.com:9000" → "gateway.example.com".
	assert.Equal(t, "gateway.example.com", headers.Get("X-Forwarded-Host"),
		"X-Forwarded-Host must equal inbound Host (port stripped)")
}

// TestE2E_ForwardedHeaders_XForwardedForAppends verifies the gateway appends
// the client IP to an existing X-Forwarded-For header rather than replacing it.
func TestE2E_ForwardedHeaders_XForwardedForAppends(t *testing.T) {
	h := harness.New(t)
	fake := h.AddBackend(t, "trino-1", "default")

	req := newPostRequest(t, h, "SELECT 1")
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	resp, err := h.ProxyClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body=%s", string(body))

	var parsed struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(body, &parsed))
	require.NotEmpty(t, parsed.ID)

	headers := fake.ReceivedHeaders(parsed.ID)
	require.NotNil(t, headers)

	xff := headers.Get("X-Forwarded-For")
	assert.Contains(t, xff, "1.2.3.4", "X-Forwarded-For must retain the original entry")
	assert.Contains(t, xff, ",", "X-Forwarded-For must include the appended client IP (comma-separated)")
}

// TestE2E_HopByHopStripped_RequestDirection verifies that hop-by-hop headers
// on the inbound request are NOT forwarded to the upstream backend
// (Hard Invariant #7, client → upstream direction).
func TestE2E_HopByHopStripped_RequestDirection(t *testing.T) {
	h := harness.New(t)
	fake := h.AddBackend(t, "trino-1", "default")

	req := newPostRequest(t, h, "SELECT 1")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Keep-Alive", "timeout=5")
	req.Header.Set("Te", "trailers")

	resp, err := h.ProxyClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body=%s", string(body))

	var parsed struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(body, &parsed))
	require.NotEmpty(t, parsed.ID)

	headers := fake.ReceivedHeaders(parsed.ID)
	require.NotNil(t, headers)

	assert.Empty(t, headers.Get("Connection"), "Connection must be stripped on the upstream hop")
	assert.Empty(t, headers.Get("Keep-Alive"), "Keep-Alive must be stripped on the upstream hop")
	assert.Empty(t, headers.Get("Te"), "Te must be stripped on the upstream hop")
}

// TestE2E_HopByHopStripped_ResponseDirection verifies that hop-by-hop headers
// set by the backend are NOT propagated back to the client
// (Hard Invariant #7, upstream → client direction). Identified as a gap by the
// go-qa analysis: both directions must be covered.
func TestE2E_HopByHopStripped_ResponseDirection(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/info":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"starting":false}`)
		case "/v1/statement":
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("Keep-Alive", "timeout=5")
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"q_hbh","nextUri":"http://x/v1/statement/q_hbh/1","stats":{"state":"QUEUED"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	h := harness.New(t)
	registerBackend(t, h, "trino-hbh", "default", upstream.URL)

	resp, body := postStatement(t, h, "SELECT 1", nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "body=%s", string(body))

	assert.Empty(t, resp.Header.Get("Connection"),
		"gateway response must not echo backend's Connection header")
	assert.Empty(t, resp.Header.Get("Keep-Alive"),
		"gateway response must not echo backend's Keep-Alive header")
}

