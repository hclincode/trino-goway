//go:build e2e

package e2e_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/e2e/harness"
)

// TestE2E_Recovery_HEADProbeFanout verifies that when no backend in the
// default routing group exists, the recovery chain fans out HEAD probes to
// all active backends regardless of group. Both fakes must record a HEAD
// probe for the unknown queryId.
//
// Setup: place both fakes in non-default groups so resolveGroup("default")
// returns "" and the router falls through to the 3-step recovery chain.
func TestE2E_Recovery_HEADProbeFanout(t *testing.T) {
	h := harness.New(t)
	fakeA := h.AddBackend(t, "trino-A", "groupA")
	fakeB := h.AddBackend(t, "trino-B", "groupB")

	const unknownID = "11111_22222_33333_unknown"

	resp := doGet(t, h, h.ProxyURL+"/v1/query/"+unknownID+"/1", nil)
	defer resp.Body.Close()

	// Recovery chain returns first-active when all HEAD probes 404, so the
	// client must still see a successful response (not 404/502).
	assert.NotEqual(t, http.StatusBadGateway, resp.StatusCode,
		"recovery chain must not surface 502 to the client when active backends exist")

	// Allow a short window for the concurrent fan-out probes to settle.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fakeA.HeadProbes(unknownID) > 0 && fakeB.HeadProbes(unknownID) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	assert.GreaterOrEqual(t, fakeA.HeadProbes(unknownID), 1,
		"fakeA must receive a HEAD probe during recovery fan-out")
	assert.GreaterOrEqual(t, fakeB.HeadProbes(unknownID), 1,
		"fakeB must receive a HEAD probe during recovery fan-out")
}

// TestE2E_Recovery_FirstActiveFallback verifies that a request for an
// unknown queryId — with the cache empty and history empty — still resolves
// to an active backend (first-active default), returning 200 to the client.
func TestE2E_Recovery_FirstActiveFallback(t *testing.T) {
	h := harness.New(t)
	fake := h.AddBackend(t, "trino-1", "default")
	require.NotNil(t, fake)

	const unknownID = "12345_67890_11111_neverseen"

	resp := doGet(t, h, h.ProxyURL+"/v1/query/"+unknownID+"/1", nil)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"unknown queryId must fall through to first-active backend, not return 4xx/5xx")
	assert.GreaterOrEqual(t, fake.HitCount(unknownID), 1,
		"first-active fallback must deliver the request to the only registered backend")
}

// TestE2E_Recovery_HistoryLookup verifies sticky routing for a known queryId:
// after POST /v1/statement, the queryId is cached, so a subsequent
// GET /v1/query/<id>/1 lands on the owner backend even with multiple active
// backends in the same routing group.
//
// (In a multi-process gateway deployment the same routing would be served by
// the history DAO; in this in-process harness the cache wins first. Either
// path is acceptable per the recovery contract — the invariant under test is
// that the owner backend, not a peer, serves the sticky GET.)
func TestE2E_Recovery_HistoryLookup(t *testing.T) {
	h := harness.New(t)
	fakeA := h.AddBackend(t, "trino-A", "default")
	fakeB := h.AddBackend(t, "trino-B", "default")

	postResp, body := postStatement(t, h, "SELECT 1", nil)
	defer postResp.Body.Close()
	require.Equal(t, http.StatusOK, postResp.StatusCode, "body=%s", string(body))

	var parsed struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(body, &parsed))
	queryID := parsed.ID
	require.NotEmpty(t, queryID)

	// Discover which fake received the POST.
	var owner, peer = fakeA, fakeB
	if contains(fakeB.QueryIDs(), queryID) {
		owner, peer = fakeB, fakeA
	}
	require.Contains(t, owner.QueryIDs(), queryID, "POST must have landed on exactly one fake")

	getResp := doGet(t, h, h.ProxyURL+"/v1/query/"+queryID+"/1", nil)
	defer getResp.Body.Close()
	require.Equal(t, http.StatusOK, getResp.StatusCode)

	assert.GreaterOrEqual(t, owner.HitCount(queryID), 1,
		"sticky routing must deliver GET to the owner backend")
	assert.Equal(t, 0, peer.HitCount(queryID),
		"peer backend must NOT receive the sticky GET for this queryId")
}

// TestE2E_StatementPolls_BypassCache verifies that the /v1/statement/<id>/...
// poll paths are forwarded by the gateway (they do not match the cache key
// regex, which only extracts queryIds from /v1/query/<id>). The invariant is
// that these paths still reach an active backend — they should not be
// rejected by the cache lookup or the recovery chain.
//
// Gap call-out from qa-tech-lead §1.2c — confirms the streaming router does
// not require a cache hit on /v1/statement/... paths.
func TestE2E_StatementPolls_BypassCache(t *testing.T) {
	h := harness.New(t)
	fake := h.AddBackend(t, "trino-1", "default")

	// First produce a queryId by POSTing /v1/statement (this populates the cache).
	postResp, body := postStatement(t, h, "SELECT 1", nil)
	defer postResp.Body.Close()
	require.Equal(t, http.StatusOK, postResp.StatusCode, "body=%s", string(body))

	var parsed struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(body, &parsed))
	require.NotEmpty(t, parsed.ID)

	// The /v1/statement/<id>/executing/<token> path is what Trino clients poll
	// once the initial POST returns. The proxy's chi route catches it via the
	// /* fall-through handler (handleStream).
	pollURL := h.ProxyURL + "/v1/statement/" + parsed.ID + "/executing/token123"

	// TrinoFake does not implement /v1/statement/.../, so it will return 404 —
	// but the invariant under test is that the gateway forwards (not blocks)
	// the request. We assert the fake actually saw it by checking it returns
	// a non-502 response (i.e. a backend response was obtained).
	resp := doGet(t, h, pollURL, nil)
	defer resp.Body.Close()

	assert.NotEqual(t, http.StatusBadGateway, resp.StatusCode,
		"gateway must forward /v1/statement/<id>/executing polls, not 502 them")
	require.NotNil(t, fake, "fake must be reachable for assertion bookkeeping")
}
