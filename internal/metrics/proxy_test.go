package metrics_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/metrics"
)

func TestProxyMetrics_RecordsFamilies(t *testing.T) {
	reg := metrics.New()
	pm, err := metrics.NewProxyMetrics(reg.Registerer())
	require.NoError(t, err)

	pm.RequestHandled("http://b1:8080", "etl", "ok")
	pm.RequestHandled("http://b1:8080", "etl", "ok")
	pm.RequestHandled("", "", "error")
	pm.UpstreamDuration("http://b1:8080", 0.05)
	pm.OversizedResponse()
	pm.StatementCacheWrite()

	fams, err := reg.Gatherer().Gather()
	require.NoError(t, err)

	okMetric := findMetric(t, fams, "trino_goway_proxy_requests_total",
		map[string]string{"backend": "http://b1:8080", "routing_group": "etl", "outcome": "ok"})
	require.NotNil(t, okMetric)
	assert.Equal(t, float64(2), okMetric.GetCounter().GetValue())

	errMetric := findMetric(t, fams, "trino_goway_proxy_requests_total",
		map[string]string{"backend": "", "routing_group": "", "outcome": "error"})
	require.NotNil(t, errMetric)
	assert.Equal(t, float64(1), errMetric.GetCounter().GetValue())

	durMetric := findMetric(t, fams, "trino_goway_proxy_upstream_duration_seconds",
		map[string]string{"backend": "http://b1:8080"})
	require.NotNil(t, durMetric)
	assert.Equal(t, uint64(1), durMetric.GetHistogram().GetSampleCount())

	oversized := findMetric(t, fams, "trino_goway_proxy_oversized_responses_total", nil)
	require.NotNil(t, oversized)
	assert.Equal(t, float64(1), oversized.GetCounter().GetValue())

	cacheWrites := findMetric(t, fams, "trino_goway_proxy_statement_cache_writes_total", nil)
	require.NotNil(t, cacheWrites)
	assert.Equal(t, float64(1), cacheWrites.GetCounter().GetValue())
}
