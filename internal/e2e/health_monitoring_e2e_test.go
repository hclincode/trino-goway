//go:build e2e

package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/e2e/harness"
	"github.com/hclincode/trino-goway/internal/testutil"
)

// backendEntry is the wire shape returned by GET /gateway/backend/all.
type backendEntry struct {
	Name         string `json:"name"`
	ProxyTo      string `json:"proxyTo"`
	Active       bool   `json:"active"`
	RoutingGroup string `json:"routingGroup"`
}

// backendStatusEntry is the wire shape returned by /webapp/getAllBackends:
// includes the live status field that the monitor populates.
type backendStatusEntry struct {
	Name         string `json:"name"`
	ProxyTo      string `json:"proxyTo"`
	Active       bool   `json:"active"`
	RoutingGroup string `json:"routingGroup"`
	Status       string `json:"status"`
}

// webappBackendsResp is the {code,msg,data} envelope around the backends list.
type webappBackendsResp struct {
	Code int                  `json:"code"`
	Msg  string               `json:"msg"`
	Data []backendStatusEntry `json:"data"`
}

// fetchWebappBackends issues POST /webapp/getAllBackends and returns the parsed
// envelope contents.
func fetchWebappBackends(t *testing.T, h *harness.Harness) []backendStatusEntry {
	t.Helper()
	resp, err := h.AdminClient("").Post(
		h.AdminURL+"/webapp/getAllBackends",
		"application/json",
		bytes.NewReader([]byte(`{}`)),
	)
	require.NoError(t, err, "POST /webapp/getAllBackends")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var env webappBackendsResp
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&env))
	require.Equal(t, 200, env.Code)
	return env.Data
}

// findBackend returns the entry for the named backend or fails the test.
func findBackend(t *testing.T, entries []backendStatusEntry, name string) backendStatusEntry {
	t.Helper()
	for _, b := range entries {
		if b.Name == name {
			return b
		}
	}
	t.Fatalf("backend %q not found in %+v", name, entries)
	return backendStatusEntry{}
}

// pollBackendStatus polls /webapp/getAllBackends until the named backend has
// the wanted status, or the deadline elapses. Returns the last observed status
// on timeout for the t.Fatal message.
func pollBackendStatus(t *testing.T, h *harness.Harness, name, want string, deadline time.Duration) string {
	t.Helper()
	end := time.Now().Add(deadline)
	var last string
	for time.Now().Before(end) {
		entries := fetchWebappBackends(t, h)
		for _, b := range entries {
			if b.Name == name {
				last = b.Status
				if last == want {
					return last
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("backend %q never reached status %q within %s (last=%q)", name, want, deadline, last)
	return last
}

// TestE2E_Monitor_HealthyBackend verifies that a healthy TrinoFake backend
// transitions to HEALTHY after the monitor's first probe cycle.
func TestE2E_Monitor_HealthyBackend(t *testing.T) {
	h := harness.New(t, harness.WithMonitorInterval(2*time.Second))
	h.AddBackend(t, "trino-1", "default")

	got := pollBackendStatus(t, h, "trino-1", "HEALTHY", 10*time.Second)
	assert.Equal(t, "HEALTHY", got)
}

// TestE2E_Monitor_UnhealthyBackend verifies that a backend whose /v1/info
// reports {"starting":true} is marked UNHEALTHY by the monitor.
func TestE2E_Monitor_UnhealthyBackend(t *testing.T) {
	h := harness.New(t, harness.WithMonitorInterval(2*time.Second))

	fake := testutil.NewTrinoFake(t)
	fake.SetStarting(true)

	registerBackend(t, h, "trino-starting", "default", fake.URL)

	got := pollBackendStatus(t, h, "trino-starting", "UNHEALTHY", 10*time.Second)
	assert.Equal(t, "UNHEALTHY", got)
}

// TestE2E_Monitor_TransportError verifies that a backend pointing to a closed
// port (no server listening) is marked UNHEALTHY.
func TestE2E_Monitor_TransportError(t *testing.T) {
	h := harness.New(t, harness.WithMonitorInterval(2*time.Second))

	// Allocate a free port and never bind it — connection attempts will fail.
	deadPort := testutil.FreePort(t)
	deadURL := fmt.Sprintf("http://127.0.0.1:%d", deadPort)

	registerBackend(t, h, "trino-dead", "default", deadURL)

	got := pollBackendStatus(t, h, "trino-dead", "UNHEALTHY", 10*time.Second)
	assert.Equal(t, "UNHEALTHY", got)
}

// TestE2E_Monitor_NewlyAddedBackend verifies that an active backend appears
// with status PENDING immediately after POST /entity, then transitions to
// HEALTHY after the next probe cycle.
func TestE2E_Monitor_NewlyAddedBackend(t *testing.T) {
	h := harness.New(t, harness.WithMonitorInterval(2*time.Second))

	fake := testutil.NewTrinoFake(t)
	registerBackend(t, h, "trino-new", "default", fake.URL)

	// Immediately observe: should be PENDING, not absent and not HEALTHY yet.
	entries := fetchWebappBackends(t, h)
	entry := findBackend(t, entries, "trino-new")
	assert.Equal(t, "PENDING", entry.Status, "newly-added active backend must be PENDING immediately after POST /entity")

	got := pollBackendStatus(t, h, "trino-new", "HEALTHY", 10*time.Second)
	assert.Equal(t, "HEALTHY", got)
}

// TestE2E_Monitor_DeactivatedBackend verifies that deactivating a backend
// removes it from /gateway/backend/active and that proxy requests no longer
// land on it (502 when no other active backend is registered).
func TestE2E_Monitor_DeactivatedBackend(t *testing.T) {
	h := harness.New(t, harness.WithMonitorInterval(2*time.Second))
	h.AddBackend(t, "trino-1", "default")

	// Deactivate via the admin port.
	resp, err := h.AdminClient("").Post(
		h.AdminURL+"/gateway/backend/deactivate/trino-1",
		"application/json",
		nil,
	)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Active list must exclude the backend.
	activeResp, err := h.AdminClient("").Get(h.AdminURL + "/gateway/backend/active")
	require.NoError(t, err)
	defer activeResp.Body.Close()
	require.Equal(t, http.StatusOK, activeResp.StatusCode)

	var active []backendEntry
	require.NoError(t, json.NewDecoder(activeResp.Body).Decode(&active))
	for _, b := range active {
		assert.NotEqual(t, "trino-1", b.Name, "deactivated backend must not appear in /gateway/backend/active")
	}

	// Proxy must respond 502 (no active backends) on /v1/statement.
	stmtResp, err := h.ProxyClient().Post(
		h.ProxyURL+"/v1/statement",
		"text/plain",
		bytes.NewReader([]byte("SELECT 1")),
	)
	require.NoError(t, err)
	defer stmtResp.Body.Close()
	body, _ := io.ReadAll(stmtResp.Body)
	assert.Equal(t, http.StatusBadGateway, stmtResp.StatusCode,
		"POST /v1/statement with no active backends must return 502 (body=%s)", string(body))
}

