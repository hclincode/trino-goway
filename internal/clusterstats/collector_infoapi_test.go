package clusterstats

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hclincode/trino-goway/internal/clusterstatus"
)

// TestInfoAPICollector_ReflectsStatusFunc_NoHTTP is the core INFO_API contract:
// the collector reports TrinoStatus from the reused health verdict, populates the
// persistence-derived identity fields, keeps counts at 0, and issues NO HTTP.
func TestInfoAPICollector_ReflectsStatusFunc_NoHTTP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status clusterstatus.Status
	}{
		{name: "healthy", status: clusterstatus.Healthy},
		{name: "unhealthy", status: clusterstatus.Unhealthy},
		// Driving Pending directly (option a) decouples this from the Task 78
		// starting→PENDING monitor fix: the collector mirrors whatever verdict the
		// StatusFunc returns, so a Pending verdict must surface as Pending.
		{name: "starting maps to pending", status: clusterstatus.Pending},
		{name: "unknown", status: clusterstatus.Unknown},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client, tr := recordingClient()
			c, err := NewCollector(
				clusterStatsCfg("INFO_API"), monitorCfg(), backendStateCfg(),
				staticStatus(tc.status), client, nil,
			)
			assert.NoError(t, err)

			b := newBackend("be-1", "http://backend.invalid")
			cs := c.Collect(context.Background(), b)

			assert.Equal(t, tc.status, cs.TrinoStatus)
			assert.Equal(t, "be-1", cs.ClusterID)
			assert.Equal(t, "http://backend.invalid", cs.ProxyTo)
			assert.Equal(t, "https://external.example/be-1", cs.ExternalURL)
			assert.Equal(t, "adhoc", cs.RoutingGroup)
			assert.Zero(t, cs.RunningQueryCount)
			assert.Zero(t, cs.QueuedQueryCount)
			assert.Zero(t, cs.NumWorkerNodes)
			assert.Nil(t, cs.UserQueuedCount)
			assert.Zero(t, tr.Calls(), "INFO_API must issue no stats HTTP")
		})
	}
}
