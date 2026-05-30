//go:build e2e

package e2e_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/e2e/harness"
)

// TestE2E_KillQuery_RoutesToOwnerBackend verifies KILL QUERY '<id>' is routed
// to the backend that owns the original query, not to whichever backend the
// default routing-group selection would pick (Hard Invariant #6).
//
// Setup uses two routing groups so the default-group selection alone would
// pick at most one of them; the KILL QUERY routing logic must explicitly
// override that choice with the owner backend, discovered via the recovery
// chain (HEAD probe on the queryId).
func TestE2E_KillQuery_RoutesToOwnerBackend(t *testing.T) {
	h := harness.New(t)
	fakeA := h.AddBackend(t, "trino-A", "default")
	fakeB := h.AddBackend(t, "trino-B", "groupB")

	// Run a query — it must land on a backend in the default group → fakeA.
	postResp, body := postStatement(t, h, "SELECT 1", nil)
	defer postResp.Body.Close()
	require.Equal(t, http.StatusOK, postResp.StatusCode, "POST /v1/statement: body=%s", string(body))

	var parsed struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(body, &parsed))
	queryID := parsed.ID
	require.NotEmpty(t, queryID)
	require.Contains(t, fakeA.QueryIDs(), queryID, "default-group routing must send the query to fakeA")
	require.NotContains(t, fakeB.QueryIDs(), queryID, "fakeB must not have received the original query")

	// Record fakeA POST count BEFORE the KILL so we can detect the increment.
	fakeAPostsBefore := len(fakeA.QueryIDs())

	// Now send KILL QUERY through the gateway. The router's KILL path uses the
	// recovery chain (HEAD probe + history) rather than the default routing
	// group, so it should reach fakeA — which is the only backend that knows
	// about queryID — not fakeB.
	killBody := "KILL QUERY '" + queryID + "'"
	killResp, killRespBody := postStatement(t, h, killBody, nil)
	defer killResp.Body.Close()
	require.Equal(t, http.StatusOK, killResp.StatusCode, "kill query: body=%s", string(killRespBody))

	assert.Equal(t, fakeAPostsBefore+1, len(fakeA.QueryIDs()),
		"fakeA should have received the KILL QUERY POST (recovery chain routed it to the owner)")
	assert.Equal(t, 0, len(fakeB.QueryIDs()),
		"fakeB should NOT have received the KILL QUERY POST")
}

// TestE2E_KillQuery_Lowercase verifies the KILL QUERY regex is case-insensitive
// (Hard Invariant #6, regex flag `(?i)`).
func TestE2E_KillQuery_Lowercase(t *testing.T) {
	h := harness.New(t)
	fakeA := h.AddBackend(t, "trino-A", "default")
	fakeB := h.AddBackend(t, "trino-B", "groupB")

	postResp, body := postStatement(t, h, "SELECT 1", nil)
	defer postResp.Body.Close()
	require.Equal(t, http.StatusOK, postResp.StatusCode, "body=%s", string(body))

	var parsed struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(body, &parsed))
	queryID := parsed.ID
	require.NotEmpty(t, queryID)
	require.Contains(t, fakeA.QueryIDs(), queryID)

	postsBefore := len(fakeA.QueryIDs())

	killResp, killBody := postStatement(t, h, "kill query '"+queryID+"'", nil)
	defer killResp.Body.Close()
	require.Equal(t, http.StatusOK, killResp.StatusCode, "lowercase kill: body=%s", string(killBody))

	assert.Equal(t, postsBefore+1, len(fakeA.QueryIDs()),
		"lowercase 'kill query' must trigger same routing as uppercase")
	assert.Equal(t, 0, len(fakeB.QueryIDs()),
		"fakeB must not have received the lowercase kill")
}

// TestE2E_KillQuery_UnknownId verifies that KILL QUERY for a queryId no
// backend owns falls through to normal routing without returning an error.
// The current behavior is: the recovery chain returns the first-active backend
// when both history-lookup and HEAD-probe-fanout miss, so KILL QUERY is still
// delivered to some backend (and the gateway returns 200).
func TestE2E_KillQuery_UnknownId(t *testing.T) {
	h := harness.New(t)
	fake := h.AddBackend(t, "trino-1", "default")
	require.NotNil(t, fake)

	// Synthetic queryId that no backend has ever seen — matches the regex shape
	// `[0-9]+_[0-9]+_[0-9]+_\w+` so KILL QUERY routing kicks in.
	resp, body := postStatement(t, h, "KILL QUERY '99999_99999_99999_unknown'", nil)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"unknown-id KILL QUERY must not error; body=%s", string(body))
}
