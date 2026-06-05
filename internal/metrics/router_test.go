package metrics_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/metrics"
)

func TestRouterMetrics_RecordsFamilies(t *testing.T) {
	reg := metrics.New()
	rm, err := metrics.NewRouterMetrics(reg.Registerer())
	require.NoError(t, err)

	rm.RouterCall("grpc", "ok", 0.01)
	rm.RouterCall("http", "timeout", 0.5)
	rm.CacheEvent("hit")
	rm.CacheEvent("miss")
	rm.CacheEvent("miss")
	rm.RecoveryStep("history")
	rm.RecoveryStep("probe")
	rm.RecoveryStep("default")
	rm.KillQueryRoute()

	fams, err := reg.Gatherer().Gather()
	require.NoError(t, err)

	c := findMetric(t, fams, "trino_goway_router_calls_total",
		map[string]string{"transport": "grpc", "outcome": "ok"})
	require.NotNil(t, c)
	assert.Equal(t, float64(1), c.GetCounter().GetValue())

	to := findMetric(t, fams, "trino_goway_router_calls_total",
		map[string]string{"transport": "http", "outcome": "timeout"})
	require.NotNil(t, to)
	assert.Equal(t, float64(1), to.GetCounter().GetValue())

	dur := findMetric(t, fams, "trino_goway_router_call_duration_seconds",
		map[string]string{"transport": "grpc"})
	require.NotNil(t, dur)
	assert.Equal(t, uint64(1), dur.GetHistogram().GetSampleCount())

	miss := findMetric(t, fams, "trino_goway_routing_cache_events_total",
		map[string]string{"event": "miss"})
	require.NotNil(t, miss)
	assert.Equal(t, float64(2), miss.GetCounter().GetValue())

	hist := findMetric(t, fams, "trino_goway_recovery_chain_steps_total",
		map[string]string{"step": "history"})
	require.NotNil(t, hist)
	assert.Equal(t, float64(1), hist.GetCounter().GetValue())

	kq := findMetric(t, fams, "trino_goway_kill_query_routes_total", nil)
	require.NotNil(t, kq)
	assert.Equal(t, float64(1), kq.GetCounter().GetValue())
}
