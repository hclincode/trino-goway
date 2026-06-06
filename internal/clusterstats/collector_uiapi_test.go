package clusterstats

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/clusterstatus"
	"github.com/hclincode/trino-goway/internal/config"
	"github.com/hclincode/trino-goway/internal/testutil"
)

// newUICollector wires a UI_API collector to a TrinoFake. The fake's URL is the
// backend's proxyTo; statsClient carries no cookie jar (the collector owns it).
func newUICollector(t *testing.T, bs config.BackendStateConfig) (Collector, *testutil.TrinoFake) {
	t.Helper()
	fake := testutil.NewTrinoFake(t)
	c, err := NewCollector(clusterStatsCfg("UI_API"), monitorCfg(), bs, nil, statsClient(), nil)
	require.NoError(t, err)
	return c, fake
}

// statsClient mirrors the production statsClient: a redirect-suppressing client
// with no cookie jar (the UI_API collector keeps its session jar internally).
func statsClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func TestUIAPICollector_LoginThenStats(t *testing.T) {
	t.Parallel()

	c, fake := newUICollector(t, uiBackendStateConfig("admin", "secret", false))
	fake.SetUICredentials("admin", "secret")
	fake.SetUIStats(3, 5, 2)
	fake.SetQueuedQueries(map[string]int{"alice": 2})

	cs := c.Collect(context.Background(), newBackend("be-ui", fake.URL))

	assert.Equal(t, clusterstatus.Healthy, cs.TrinoStatus)
	assert.Equal(t, 3, cs.NumWorkerNodes)
	assert.Equal(t, 5, cs.RunningQueryCount)
	assert.Equal(t, 2, cs.QueuedQueryCount)
	assert.Equal(t, map[string]int{"alice": 2}, cs.UserQueuedCount)
	assert.Equal(t, "be-ui", cs.ClusterID)
	assert.Equal(t, fake.URL, cs.ProxyTo)
	assert.Equal(t, 1, fake.LoginCount())
	assert.Equal(t, 1, fake.UIStatsHits())
	assert.Equal(t, 1, fake.UIQueryHits())
}

func TestUIAPICollector_ActiveWorkersZero_Unhealthy(t *testing.T) {
	t.Parallel()

	c, fake := newUICollector(t, uiBackendStateConfig("admin", "secret", false))
	fake.SetUICredentials("admin", "secret")
	fake.SetUIStats(0, 1, 4) // activeWorkers == 0

	cs := c.Collect(context.Background(), newBackend("be-ui", fake.URL))

	assert.Equal(t, clusterstatus.Unhealthy, cs.TrinoStatus)
	assert.Equal(t, 0, cs.NumWorkerNodes)
	// Counts still surface even when unhealthy (Java sets them before the verdict).
	assert.Equal(t, 1, cs.RunningQueryCount)
	assert.Equal(t, 4, cs.QueuedQueryCount)
}

func TestUIAPICollector_PerUserQueuedTally(t *testing.T) {
	t.Parallel()

	c, fake := newUICollector(t, uiBackendStateConfig("admin", "secret", false))
	fake.SetUICredentials("admin", "secret")
	fake.SetUIStats(2, 0, 3)
	fake.SetQueuedQueries(map[string]int{"alice": 2, "bob": 1})

	cs := c.Collect(context.Background(), newBackend("be-ui", fake.URL))

	assert.Equal(t, map[string]int{"alice": 2, "bob": 1}, cs.UserQueuedCount)
}

func TestUIAPICollector_LoginFailed_NoCounts(t *testing.T) {
	t.Parallel()

	c, fake := newUICollector(t, uiBackendStateConfig("admin", "wrong-password", false))
	fake.SetUICredentials("admin", "secret") // collector sends the wrong password
	fake.SetUIStats(3, 5, 2)

	cs := c.Collect(context.Background(), newBackend("be-ui", fake.URL))

	// Login is rejected (403); collector returns a partial result: identity fields
	// only, no counts, UNKNOWN status, and never reaches the stats endpoint.
	assert.Equal(t, clusterstatus.Unknown, cs.TrinoStatus)
	assert.Zero(t, cs.RunningQueryCount)
	assert.Zero(t, cs.QueuedQueryCount)
	assert.Zero(t, cs.NumWorkerNodes)
	assert.Nil(t, cs.UserQueuedCount)
	assert.Equal(t, "be-ui", cs.ClusterID)
	assert.Equal(t, 0, fake.UIStatsHits(), "no stats GET after a failed login")
}

func TestUIAPICollector_EmptyStats_NoCounts(t *testing.T) {
	t.Parallel()

	// Credentials valid → cookie issued, but stats are never configured. The fake
	// returns activeWorkers/running/queued all zero, so the response is non-empty
	// but reports an idle, worker-less cluster → UNHEALTHY, counts zero.
	c, fake := newUICollector(t, uiBackendStateConfig("admin", "secret", false))
	fake.SetUICredentials("admin", "secret")

	cs := c.Collect(context.Background(), newBackend("be-ui", fake.URL))

	assert.Equal(t, clusterstatus.Unhealthy, cs.TrinoStatus)
	assert.Zero(t, cs.RunningQueryCount)
	assert.Zero(t, cs.QueuedQueryCount)
	assert.Zero(t, cs.NumWorkerNodes)
}

func TestUIAPICollector_XForwardedProtoHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		xForwarded  bool
		wantForward string
	}{
		{name: "enabled", xForwarded: true, wantForward: "https"},
		{name: "disabled", xForwarded: false, wantForward: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c, fake := newUICollector(t, uiBackendStateConfig("admin", "secret", tc.xForwarded))
			fake.SetUICredentials("admin", "secret")
			fake.SetUIStats(1, 0, 0)

			_ = c.Collect(context.Background(), newBackend("be-ui", fake.URL))

			// The header is added to the two GETs (stats/query), not the login.
			assert.Equal(t, tc.wantForward, fake.LastForwardedProto())
		})
	}
}

// TestUIAPICollector_CookieReusedAcrossCalls pins the documented one-session
// optimization: the collector logs in once and reuses the cookie across ticks, so
// after two Collect calls LoginCount stays 1 while stats are fetched twice.
func TestUIAPICollector_CookieReusedAcrossCalls(t *testing.T) {
	t.Parallel()

	c, fake := newUICollector(t, uiBackendStateConfig("admin", "secret", false))
	fake.SetUICredentials("admin", "secret")
	fake.SetUIStats(2, 1, 1)

	b := newBackend("be-ui", fake.URL)
	_ = c.Collect(context.Background(), b)
	_ = c.Collect(context.Background(), b)

	assert.Equal(t, 1, fake.LoginCount(), "session cookie should be reused across ticks")
	assert.Equal(t, 2, fake.UIStatsHits())
	assert.Equal(t, 2, fake.UIQueryHits())
}
