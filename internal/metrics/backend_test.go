package metrics_test

import (
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/metrics"
	"github.com/hclincode/trino-goway/internal/monitor"
)

func gaugeValue(t *testing.T, fams []*dto.MetricFamily, name string, want map[string]string) (float64, bool) {
	t.Helper()
	m := findMetric(t, fams, name, want)
	if m == nil {
		return 0, false
	}
	return m.GetGauge().GetValue(), true
}

func TestBackendMetrics_TracksTransitionsAndPrunes(t *testing.T) {
	reg := metrics.New()
	bm, err := metrics.NewBackendMetrics(reg.Registerer())
	require.NoError(t, err)

	const b1 = "http://b1:8080"
	const b2 = "http://b2:8080"
	names := map[string]string{b1: "c1", b2: "c2"}

	// Snapshot 1: b1 healthy, b2 unhealthy.
	bm.ObserveStatuses(map[string]monitor.TrinoStatus{
		b1: monitor.StatusHealthy,
		b2: monitor.StatusUnhealthy,
	}, names)

	fams, err := reg.Gatherer().Gather()
	require.NoError(t, err)

	v, ok := gaugeValue(t, fams, "trino_goway_backend_status", map[string]string{"backend": b1, "status": "healthy"})
	require.True(t, ok)
	assert.Equal(t, float64(1), v)
	v, _ = gaugeValue(t, fams, "trino_goway_backend_status", map[string]string{"backend": b1, "status": "unhealthy"})
	assert.Equal(t, float64(0), v)
	v, _ = gaugeValue(t, fams, "trino_goway_backend_status", map[string]string{"backend": b2, "status": "unhealthy"})
	assert.Equal(t, float64(1), v)

	v, _ = gaugeValue(t, fams, "trino_goway_backends", map[string]string{"status": "healthy"})
	assert.Equal(t, float64(1), v)
	v, _ = gaugeValue(t, fams, "trino_goway_backends", map[string]string{"status": "unhealthy"})
	assert.Equal(t, float64(1), v)
	v, _ = gaugeValue(t, fams, "trino_goway_backends_active", nil)
	assert.Equal(t, float64(2), v)
	v, _ = gaugeValue(t, fams, "trino_goway_backend_activation_status", map[string]string{"backend": b1})
	assert.Equal(t, float64(1), v)

	// Snapshot 2: b1 transitions to unhealthy; b2 removed from the set.
	bm.ObserveStatuses(map[string]monitor.TrinoStatus{
		b1: monitor.StatusUnhealthy,
	}, map[string]string{b1: "c1"})

	fams, err = reg.Gatherer().Gather()
	require.NoError(t, err)

	v, _ = gaugeValue(t, fams, "trino_goway_backend_status", map[string]string{"backend": b1, "status": "unhealthy"})
	assert.Equal(t, float64(1), v)
	v, _ = gaugeValue(t, fams, "trino_goway_backend_status", map[string]string{"backend": b1, "status": "healthy"})
	assert.Equal(t, float64(0), v)

	// b2 series must be pruned, not stale.
	_, ok = gaugeValue(t, fams, "trino_goway_backend_status", map[string]string{"backend": b2, "status": "unhealthy"})
	assert.False(t, ok, "removed backend b2 status series must be pruned")
	_, ok = gaugeValue(t, fams, "trino_goway_backend_activation_status", map[string]string{"backend": b2})
	assert.False(t, ok, "removed backend b2 activation series must be pruned")

	v, _ = gaugeValue(t, fams, "trino_goway_backends_active", nil)
	assert.Equal(t, float64(1), v)
	v, _ = gaugeValue(t, fams, "trino_goway_backends", map[string]string{"status": "healthy"})
	assert.Equal(t, float64(0), v)
}
