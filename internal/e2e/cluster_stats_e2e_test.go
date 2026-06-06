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
	"github.com/hclincode/trino-goway/internal/testutil"
)

// backendProbeWait bounds how long a test waits for a newly-registered backend to
// be picked up and probed/collected. main reloads the backend list from the DB on a
// 15s cadence (backendRefreshInterval) and the monitor needs one further probe tick,
// so a newly-added backend can take ~17s to surface live status/counts. The deadline
// sits comfortably above that boundary to keep these tests deterministic.
const backendProbeWait = 30 * time.Second

// clusterStatsResponse mirrors the M7 admin.ClusterStatsResponse wire shape
// returned by GET /api/public/backends/{name}/state (UC-ADM-14). Field names are
// the contract under test, so they are pinned here rather than imported.
type clusterStatsResponse struct {
	ClusterID         string         `json:"clusterId"`
	RunningQueryCount int            `json:"runningQueryCount"`
	QueuedQueryCount  int            `json:"queuedQueryCount"`
	NumWorkerNodes    int            `json:"numWorkerNodes"`
	TrinoStatus       string         `json:"trinoStatus"`
	ProxyTo           string         `json:"proxyTo"`
	ExternalURL       string         `json:"externalUrl"`
	RoutingGroup      string         `json:"routingGroup"`
	UserQueuedCount   map[string]int `json:"userQueuedCount"`
}

// backendCountEntry is the /webapp/getAllBackends DTO including the live
// queued/running counts (the BackendResponse shape).
type backendCountEntry struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Queued  int    `json:"queued"`
	Running int    `json:"running"`
}

type webappCountResp struct {
	Code int                 `json:"code"`
	Data []backendCountEntry `json:"data"`
}

// fetchPublicState issues GET /api/public/backends/{name}/state and decodes the
// M7 wire shape. It asserts 200 + JSON Content-Type and returns the decoded
// response plus the raw body so callers can assert on field presence/absence.
func fetchPublicState(t *testing.T, h *harness.Harness, name string) (clusterStatsResponse, map[string]json.RawMessage) {
	t.Helper()
	resp, err := h.AdminClient("").Get(h.AdminURL + "/api/public/backends/" + name + "/state")
	require.NoError(t, err, "GET public backend state")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "public state status")
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")

	var raw map[string]json.RawMessage
	var out clusterStatsResponse
	body := readBody(t, resp)
	require.NoError(t, json.Unmarshal(body, &raw), "decode raw public state")
	require.NoError(t, json.Unmarshal(body, &out), "decode public state")
	return out, raw
}

// fetchWebappCounts issues POST /webapp/getAllBackends and returns the entries
// carrying the live queued/running counts.
func fetchWebappCounts(t *testing.T, h *harness.Harness) []backendCountEntry {
	t.Helper()
	resp, err := h.AdminClient("").Post(h.AdminURL+"/webapp/getAllBackends", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var env webappCountResp
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&env))
	require.Equal(t, 200, env.Code)
	return env.Data
}

func findCountEntry(t *testing.T, entries []backendCountEntry, name string) backendCountEntry {
	t.Helper()
	for _, e := range entries {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("backend %q not found in getAllBackends %+v", name, entries)
	return backendCountEntry{}
}

func readBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	buf := make([]byte, 0, 1024)
	tmp := make([]byte, 512)
	for {
		n, err := resp.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
	}
	return buf
}

// assertNineFieldShape verifies the public-state body carries exactly the M7
// ClusterStats field set (the regression guard against reverting to
// BackendResponse). userQueuedCount may be absent (omitempty) for an uncollected
// or non-UI_API backend, so it is checked separately by the caller.
func assertNineFieldShape(t *testing.T, raw map[string]json.RawMessage) {
	t.Helper()
	for _, key := range []string{
		"clusterId", "runningQueryCount", "queuedQueryCount", "numWorkerNodes",
		"trinoStatus", "proxyTo", "externalUrl", "routingGroup",
	} {
		_, ok := raw[key]
		assert.Truef(t, ok, "public state must carry M7 field %q (got keys %v)", key, keysOf(raw))
	}
	// BackendResponse-only fields must NOT appear — this is the M7 switch.
	for _, key := range []string{"status", "queued", "running"} {
		_, ok := raw[key]
		assert.Falsef(t, ok, "public state must NOT carry BackendResponse field %q", key)
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestE2E_ClusterStats_InfoAPI_PublicStateShape — default INFO_API: the public
// state endpoint returns the M7 9-field ClusterStatsResponse (not BackendResponse)
// with counts 0 and trinoStatus HEALTHY once the backend is probed.
func TestE2E_ClusterStats_InfoAPI_PublicStateShape(t *testing.T) {
	h := newHarnessNoLeaks(t, harness.WithMonitorInterval(2*time.Second))
	h.AddBackend(t, "be-infoapi", "default")

	got := pollBackendStatus(t, h, "be-infoapi", "HEALTHY", backendProbeWait)
	require.Equal(t, "HEALTHY", got)

	state, raw := fetchPublicState(t, h, "be-infoapi")
	assertNineFieldShape(t, raw)
	assert.Equal(t, "be-infoapi", state.ClusterID)
	assert.Equal(t, "HEALTHY", state.TrinoStatus)
	assert.Zero(t, state.RunningQueryCount, "INFO_API counts are 0")
	assert.Zero(t, state.QueuedQueryCount)
	assert.Zero(t, state.NumWorkerNodes)
	assert.NotEmpty(t, state.ProxyTo)
	assert.NotEmpty(t, state.ExternalURL)
	assert.Equal(t, "default", state.RoutingGroup)
}

// TestE2E_ClusterStats_InfoAPI_GetAllBackendsZeroCounts — under the default,
// getAllBackends reports Queued/Running 0.
func TestE2E_ClusterStats_InfoAPI_GetAllBackendsZeroCounts(t *testing.T) {
	h := newHarnessNoLeaks(t, harness.WithMonitorInterval(2*time.Second))
	h.AddBackend(t, "be-counts", "default")

	pollBackendStatus(t, h, "be-counts", "HEALTHY", backendProbeWait)

	entry := findCountEntry(t, fetchWebappCounts(t, h), "be-counts")
	assert.Zero(t, entry.Queued)
	assert.Zero(t, entry.Running)
}

// TestE2E_ClusterStats_InfoAPI_StartingIsPending — a backend reporting
// {"starting":true} surfaces trinoStatus PENDING on the public state (the
// starting→PENDING correction), never UNHEALTHY.
func TestE2E_ClusterStats_InfoAPI_StartingIsPending(t *testing.T) {
	h := newHarnessNoLeaks(t, harness.WithMonitorInterval(2*time.Second))

	fake := testutil.NewTrinoFake(t)
	fake.SetStarting(true)
	registerBackend(t, h, "be-starting", "default", fake.URL)

	// Wait out the refresh+probe window so the verdict reflects an actual probe of
	// the starting backend (the pre-probe default is also PENDING, so a bare poll
	// would not prove the starting→PENDING mapping). Throughout, the status must
	// stay PENDING and must never flip to HEALTHY or UNHEALTHY — the correction
	// under test is that {"starting":true} maps to PENDING, not UNHEALTHY.
	deadline := time.Now().Add(backendProbeWait)
	var state clusterStatsResponse
	for time.Now().Before(deadline) {
		state, _ = fetchPublicState(t, h, "be-starting")
		assert.Equal(t, "PENDING", state.TrinoStatus, "a starting backend must remain PENDING (never HEALTHY/UNHEALTHY)")
		time.Sleep(1 * time.Second)
	}

	assert.Equal(t, "PENDING", state.TrinoStatus, "starting backend maps to PENDING, not UNHEALTHY")
	assert.Zero(t, state.RunningQueryCount)
	assert.Zero(t, state.QueuedQueryCount)
}

// TestE2E_ClusterStats_UnobservedDefaultFromPersistence — querying the public
// state right after registration (before the stats store is populated) returns the
// persistence-populated default: proxyTo/externalUrl/routingGroup set, counts 0,
// userQueuedCount null/absent, never a 500 (choice b).
func TestE2E_ClusterStats_UnobservedDefaultFromPersistence(t *testing.T) {
	h := newHarnessNoLeaks(t, harness.WithMonitorInterval(2*time.Second))

	fake := testutil.NewTrinoFake(t)
	registerBackend(t, h, "be-unobserved", "adhoc", fake.URL)

	// Immediately — the backend is registered but may not be collected yet.
	state, raw := fetchPublicState(t, h, "be-unobserved")
	assertNineFieldShape(t, raw)
	assert.Equal(t, "be-unobserved", state.ClusterID)
	assert.Equal(t, fake.URL, state.ProxyTo, "proxyTo populated from persistence")
	assert.Equal(t, fake.URL, state.ExternalURL, "externalUrl falls back to proxyTo")
	assert.Equal(t, "adhoc", state.RoutingGroup)
	assert.Zero(t, state.RunningQueryCount)
	assert.Zero(t, state.QueuedQueryCount)
	assert.Zero(t, state.NumWorkerNodes)
	// userQueuedCount is null/absent under INFO_API (omitempty).
	_, present := raw["userQueuedCount"]
	assert.False(t, present, "userQueuedCount absent for an uncollected/INFO_API backend")
	assert.Nil(t, state.UserQueuedCount)

	// Missing name still 404s (unchanged).
	resp, err := h.AdminClient("").Get(h.AdminURL + "/api/public/backends/does-not-exist/state")
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestE2E_ClusterStats_UIAPI_LiveCountsSurface — UI_API collector: live counts
// flow through both the public state (running/queued/numWorkerNodes/userQueuedCount)
// and getAllBackends (Queued/Running).
func TestE2E_ClusterStats_UIAPI_LiveCountsSurface(t *testing.T) {
	const user = "admin"
	h := newHarnessNoLeaks(t,
		harness.WithMonitorInterval(2*time.Second),
		harness.WithClusterStats("UI_API", user, ""),
	)
	fake := h.AddBackend(t, "be-uiapi", "default")
	fake.SetUICredentials(user, "")
	fake.SetUIStats(3, 5, 2) // activeWorkers=3, running=5, queued=2
	fake.SetQueuedQueries(map[string]int{"alice": 2})

	// Wait until the UI stats have been collected at least once (running becomes 5).
	deadline := time.Now().Add(backendProbeWait)
	var state clusterStatsResponse
	for time.Now().Before(deadline) {
		state, _ = fetchPublicState(t, h, "be-uiapi")
		if state.RunningQueryCount == 5 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}

	assert.Equal(t, 5, state.RunningQueryCount)
	assert.Equal(t, 2, state.QueuedQueryCount)
	assert.Equal(t, 3, state.NumWorkerNodes)
	assert.Equal(t, "HEALTHY", state.TrinoStatus)
	assert.Equal(t, map[string]int{"alice": 2}, state.UserQueuedCount)

	entry := findCountEntry(t, fetchWebappCounts(t, h), "be-uiapi")
	assert.Equal(t, 2, entry.Queued)
	assert.Equal(t, 5, entry.Running)
}

// TestE2E_ClusterStats_Metrics_CountsAndThreshold — METRICS collector: counts
// surface and the ActiveNodeCount minimum gate flips HEALTHY↔UNHEALTHY across
// ticks; when gated UNHEALTHY the counts are zeroed.
func TestE2E_ClusterStats_Metrics_CountsAndThreshold(t *testing.T) {
	h := newHarnessNoLeaks(t,
		harness.WithMonitorInterval(2*time.Second),
		harness.WithClusterStats("METRICS", "svc", "pw"),
	)
	fake := h.AddBackend(t, "be-metrics", "default")

	const running = "trino_execution_name_QueryManager_RunningQueries"
	const queued = "trino_execution_name_QueryManager_QueuedQueries"
	const activeNodes = "trino_metadata_name_DiscoveryNodeManager_ActiveNodeCount"

	// Healthy: ActiveNodeCount 4 (>= min 1) + running/queued counts.
	fake.SetMetrics(running + " 6\n" + queued + " 3\n" + activeNodes + " 4\n")

	deadline := time.Now().Add(backendProbeWait)
	var state clusterStatsResponse
	for time.Now().Before(deadline) {
		state, _ = fetchPublicState(t, h, "be-metrics")
		if state.RunningQueryCount == 6 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	assert.Equal(t, 6, state.RunningQueryCount)
	assert.Equal(t, 3, state.QueuedQueryCount)

	// Drop ActiveNodeCount below the minimum → next tick gates UNHEALTHY, counts 0.
	fake.SetMetrics(running + " 6\n" + queued + " 3\n" + activeNodes + " 0\n")

	deadline = time.Now().Add(backendProbeWait)
	for time.Now().Before(deadline) {
		state, _ = fetchPublicState(t, h, "be-metrics")
		if state.RunningQueryCount == 0 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	assert.Zero(t, state.RunningQueryCount, "below-minimum gate zeroes counts")
	assert.Zero(t, state.QueuedQueryCount)
}

// TestE2E_ClusterStats_ReadyzNotGatedByStats (Hard Invariant #12) — the stats
// surface always failing (UI_API pointed at a backend whose login is rejected)
// must NOT block readiness: /trino-gateway/readyz still reaches 200 after the
// first health probe.
func TestE2E_ClusterStats_ReadyzNotGatedByStats(t *testing.T) {
	h := newHarnessNoLeaks(t,
		harness.WithMonitorInterval(2*time.Second),
		harness.WithClusterStats("UI_API", "admin", "wrong"),
	)
	// Backend is health-probeable (/v1/info ok) but never accepts the UI login
	// (no credentials configured on the fake), so stats collection always fails.
	h.AddBackend(t, "be-readyz", "default")

	// The backend still reaches HEALTHY via the health probe, proving stats failure
	// did not abort the tick.
	got := pollBackendStatus(t, h, "be-readyz", "HEALTHY", backendProbeWait)
	assert.Equal(t, "HEALTHY", got)

	// readyz is 200 (New already polled it during startup; re-assert explicitly).
	resp, err := h.AdminClient("").Get(h.AdminURL + "/trino-gateway/readyz")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "readyz must not be gated by stats collection")
}
