//go:build e2e

package e2e_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/e2e/harness"
)

// TestE2E_RoutingGroup_SteeringByGroup asserts that with two backends in
// different groups, repeated requests follow the router's group decision —
// every request goes to the etl backend, none to the adhoc backend.
func TestE2E_RoutingGroup_SteeringByGroup(t *testing.T) {
	router := newHTTPRouter(t, "etl", nil, nil)
	h := harness.New(t, harness.WithExternalHTTPRouter(router.URL()))

	adhoc := h.AddBackend(t, "adhoc-1", "adhoc")
	etl := h.AddBackend(t, "etl-1", "etl")

	const n = 5
	for i := 0; i < n; i++ {
		resp, body := postStatement(t, h, "SELECT 1", nil)
		_ = resp.Body.Close()
		require.Equalf(t, http.StatusOK, resp.StatusCode,
			"iteration %d: status=%d body=%s", i, resp.StatusCode, string(body))
	}

	assert.Len(t, etl.QueryIDs(), n, "all %d requests must land on etl-1", n)
	assert.Empty(t, adhoc.QueryIDs(), "no requests should land on adhoc-1")
}

// TestE2E_RoutingGroup_RecoveryWhenGroupEmpty asserts that when the router
// selects a group that has no active backend, the gateway's recovery chain
// picks an active backend from another group rather than returning a 502.
func TestE2E_RoutingGroup_RecoveryWhenGroupEmpty(t *testing.T) {
	router := newHTTPRouter(t, "etl", nil, nil)
	h := harness.New(t, harness.WithExternalHTTPRouter(router.URL()))

	// Only an adhoc backend is registered; no etl backend exists.
	adhoc := h.AddBackend(t, "adhoc-1", "adhoc")

	resp, body := postStatement(t, h, "SELECT 1", nil)
	defer resp.Body.Close()
	require.Equalf(t, http.StatusOK, resp.StatusCode,
		"empty target group must recover to first active backend, got status=%d body=%s",
		resp.StatusCode, string(body))

	assert.Len(t, adhoc.QueryIDs(), 1, "recovery chain must route to adhoc-1")
}

// TestE2E_SingleCluster_NoExternalRouter asserts that with no external routing
// service configured at all, every request routes to the defaultGroup
// backend — no 502, no attempt to reach a non-existent router.
func TestE2E_SingleCluster_NoExternalRouter(t *testing.T) {
	h := harness.New(t)

	backend := h.AddBackend(t, "solo", "default")

	resp, body := postStatement(t, h, "SELECT 1", nil)
	defer resp.Body.Close()
	require.Equalf(t, http.StatusOK, resp.StatusCode,
		"single-cluster mode must serve via defaultGroup, got status=%d body=%s",
		resp.StatusCode, string(body))

	assert.Len(t, backend.QueryIDs(), 1)
}
