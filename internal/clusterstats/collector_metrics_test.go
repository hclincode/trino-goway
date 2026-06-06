package clusterstats

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/clusterstatus"
	"github.com/hclincode/trino-goway/internal/config"
	"github.com/hclincode/trino-goway/internal/testutil"
)

const (
	runningMetric    = "trino_execution_name_QueryManager_RunningQueries"
	queuedMetric     = "trino_execution_name_QueryManager_QueuedQueries"
	activeNodeMetric = "trino_metadata_name_DiscoveryNodeManager_ActiveNodeCount"
)

// newMetricsCollectorWithFake wires a METRICS collector to a TrinoFake serving the
// given OpenMetrics body. The default monitorCfg() carries the Java-default metric
// names + the ActiveNodeCount>=1 minimum gate.
func newMetricsCollectorWithFake(t *testing.T, bs config.BackendStateConfig, body string) (Collector, *testutil.TrinoFake) {
	t.Helper()
	fake := testutil.NewTrinoFake(t)
	fake.SetMetrics(body)
	c, err := NewCollector(clusterStatsCfg("METRICS"), monitorCfg(), bs, nil, statsClient(), nil)
	require.NoError(t, err)
	return c, fake
}

func TestMetricsCollector_ParsesOpenMetrics(t *testing.T) {
	t.Parallel()

	// 3.9 must truncate to 3 (int(ParseFloat)), matching Java's (int) cast.
	body := "# HELP queries\n" +
		runningMetric + " 3.9\n" +
		queuedMetric + " 7\n" +
		activeNodeMetric + " 2\n"

	c, fake := newMetricsCollectorWithFake(t, uiBackendStateConfig("svc", "pw", false), body)
	cs := c.Collect(context.Background(), newBackend("be-m", fake.URL))

	assert.Equal(t, clusterstatus.Healthy, cs.TrinoStatus)
	assert.Equal(t, 3, cs.RunningQueryCount, "3.9 should truncate to 3")
	assert.Equal(t, 7, cs.QueuedQueryCount)
	// METRICS sets no worker count and no per-user breakdown.
	assert.Zero(t, cs.NumWorkerNodes)
	assert.Nil(t, cs.UserQueuedCount)
	assert.Equal(t, 1, fake.MetricsHits())
}

func TestMetricsCollector_MinimumGate_Unhealthy(t *testing.T) {
	t.Parallel()

	// ActiveNodeCount 0 is below the default minimum of 1 → UNHEALTHY, counts zeroed.
	body := runningMetric + " 5\n" +
		queuedMetric + " 2\n" +
		activeNodeMetric + " 0\n"

	c, fake := newMetricsCollectorWithFake(t, uiBackendStateConfig("svc", "pw", false), body)
	cs := c.Collect(context.Background(), newBackend("be-m", fake.URL))

	assert.Equal(t, clusterstatus.Unhealthy, cs.TrinoStatus)
	assert.Zero(t, cs.RunningQueryCount)
	assert.Zero(t, cs.QueuedQueryCount)
}

func TestMetricsCollector_MissingRequiredMetric_Unhealthy(t *testing.T) {
	t.Parallel()

	// ActiveNodeCount absent → the parse rejects the response (missing required
	// key) → no metrics → UNHEALTHY.
	body := runningMetric + " 5\n" + queuedMetric + " 2\n"

	c, fake := newMetricsCollectorWithFake(t, uiBackendStateConfig("svc", "pw", false), body)
	cs := c.Collect(context.Background(), newBackend("be-m", fake.URL))

	assert.Equal(t, clusterstatus.Unhealthy, cs.TrinoStatus)
	assert.Zero(t, cs.RunningQueryCount)
}

func TestMetricsCollector_MaximumGate_Unhealthy(t *testing.T) {
	t.Parallel()

	fake := testutil.NewTrinoFake(t)
	fake.SetMetrics(runningMetric + " 5\n" + queuedMetric + " 2\n" + activeNodeMetric + " 3\n")

	// Add a maximum gate on ActiveNodeCount of 2; value 3 exceeds it → UNHEALTHY.
	mon := monitorCfg()
	mon.MetricMaximumValues = map[string]float64{activeNodeMetric: 2}
	c, err := NewCollector(clusterStatsCfg("METRICS"), mon, uiBackendStateConfig("svc", "pw", false), nil, statsClient(), nil)
	require.NoError(t, err)

	cs := c.Collect(context.Background(), newBackend("be-m", fake.URL))
	assert.Equal(t, clusterstatus.Unhealthy, cs.TrinoStatus)
}

func TestMetricsCollector_HealthyWhenThresholdsMet(t *testing.T) {
	t.Parallel()

	body := runningMetric + " 0\n" +
		queuedMetric + " 0\n" +
		activeNodeMetric + " 4\n"

	c, fake := newMetricsCollectorWithFake(t, uiBackendStateConfig("svc", "pw", false), body)
	cs := c.Collect(context.Background(), newBackend("be-m", fake.URL))

	assert.Equal(t, clusterstatus.Healthy, cs.TrinoStatus)
	assert.Zero(t, cs.RunningQueryCount)
	assert.Zero(t, cs.QueuedQueryCount)
	assert.Zero(t, cs.NumWorkerNodes)
	assert.Nil(t, cs.UserQueuedCount)
}

// TestMetricsCollector_AuthHeaderSelection pins Basic-vs-X-Trino-User: a password
// selects Authorization: Basic; an empty password selects X-Trino-User.
func TestMetricsCollector_AuthHeaderSelection(t *testing.T) {
	t.Parallel()

	body := runningMetric + " 1\n" + queuedMetric + " 0\n" + activeNodeMetric + " 1\n"

	t.Run("basic when password set", func(t *testing.T) {
		t.Parallel()
		c, fake := newMetricsCollectorWithFake(t, uiBackendStateConfig("svc", "pw", false), body)
		_ = c.Collect(context.Background(), newBackend("be-m", fake.URL))

		basicUser, trinoUser := fake.LastMetricsAuth()
		assert.Equal(t, "svc", basicUser)
		assert.Empty(t, trinoUser)
	})

	t.Run("x-trino-user when no password", func(t *testing.T) {
		t.Parallel()
		c, fake := newMetricsCollectorWithFake(t, uiBackendStateConfig("svc", "", false), body)
		_ = c.Collect(context.Background(), newBackend("be-m", fake.URL))

		basicUser, trinoUser := fake.LastMetricsAuth()
		assert.Empty(t, basicUser)
		assert.Equal(t, "svc", trinoUser)
	})
}

// TestMetricsCollector_DefaultMinimums confirms the ActiveNodeCount>=1 minimum is
// applied from the default config even when the body would otherwise be HEALTHY:
// a worker-less cluster reporting counts is gated to UNHEALTHY.
func TestMetricsCollector_DefaultMinimums(t *testing.T) {
	t.Parallel()

	// ActiveNodeCount present but 0 → below default minimum 1 → UNHEALTHY.
	body := runningMetric + " 9\n" + queuedMetric + " 9\n" + activeNodeMetric + " 0\n"

	c, fake := newMetricsCollectorWithFake(t, uiBackendStateConfig("svc", "pw", false), body)
	cs := c.Collect(context.Background(), newBackend("be-m", fake.URL))

	assert.Equal(t, clusterstatus.Unhealthy, cs.TrinoStatus)
}
