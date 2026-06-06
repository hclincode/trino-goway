package clusterstats

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/clusterstatus"
)

// TestNewCollector_SelectsByType maps each monitor type to its concrete collector,
// confirms the empty type defaults to INFO_API, and confirms JDBC/JMX/unknown are
// rejected (defense-in-depth behind config.Validate, R8).
func TestNewCollector_SelectsByType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		monitor    string
		wantType   any
		wantErr    bool
		needsCreds bool
	}{
		{name: "NOOP", monitor: "NOOP", wantType: (*noopCollector)(nil)},
		{name: "INFO_API", monitor: "INFO_API", wantType: (*infoAPICollector)(nil)},
		{name: "empty defaults to INFO_API", monitor: "", wantType: (*infoAPICollector)(nil)},
		{name: "UI_API", monitor: "UI_API", wantType: (*uiAPICollector)(nil), needsCreds: true},
		{name: "METRICS", monitor: "METRICS", wantType: (*metricsCollector)(nil), needsCreds: true},
		{name: "JDBC rejected", monitor: "JDBC", wantErr: true},
		{name: "JMX rejected", monitor: "JMX", wantErr: true},
		{name: "unknown rejected", monitor: "BOGUS", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			bs := backendStateCfg()
			if tc.needsCreds {
				bs = uiBackendStateConfig("svc", "pw", false)
			}
			client, _ := recordingClient()

			c, err := NewCollector(
				clusterStatsCfg(tc.monitor), monitorCfg(), bs,
				staticStatus(clusterstatus.Healthy), client, nil,
			)

			if tc.wantErr {
				require.Error(t, err)
				assert.Nil(t, c)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, c)
			assert.IsType(t, tc.wantType, c)
		})
	}
}
