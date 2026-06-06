package clusterstats

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hclincode/trino-goway/internal/clusterstatus"
)

// TestNoopCollector_NeverProbes verifies NOOP issues no HTTP, reports zero counts,
// carries the persistence-derived identity fields, and reflects the reused verdict.
func TestNoopCollector_NeverProbes(t *testing.T) {
	t.Parallel()

	client, tr := recordingClient()
	c, err := NewCollector(
		clusterStatsCfg("NOOP"), monitorCfg(), backendStateCfg(),
		staticStatus(clusterstatus.Healthy), client, nil,
	)
	assert.NoError(t, err)

	b := newBackend("be-noop", "http://backend.invalid")
	cs := c.Collect(context.Background(), b)

	assert.Equal(t, clusterstatus.Healthy, cs.TrinoStatus)
	assert.Equal(t, "be-noop", cs.ClusterID)
	assert.Equal(t, "http://backend.invalid", cs.ProxyTo)
	assert.Equal(t, "https://external.example/be-noop", cs.ExternalURL)
	assert.Equal(t, "adhoc", cs.RoutingGroup)
	assert.Zero(t, cs.RunningQueryCount)
	assert.Zero(t, cs.QueuedQueryCount)
	assert.Zero(t, cs.NumWorkerNodes)
	assert.Nil(t, cs.UserQueuedCount)
	assert.Zero(t, tr.Calls(), "NOOP must issue no stats HTTP")
}

// TestNoopCollector_NilStatusFunc_NoPanic guards the nil-safe path: a NOOP
// collector with a nil verdict leaves TrinoStatus at the zero value (UNKNOWN).
func TestNoopCollector_NilStatusFunc_NoPanic(t *testing.T) {
	t.Parallel()

	c := newNoopCollector(nil)
	cs := c.Collect(context.Background(), newBackend("be-x", "http://backend.invalid"))

	assert.Equal(t, clusterstatus.Unknown, cs.TrinoStatus)
	assert.Equal(t, "be-x", cs.ClusterID)
}
