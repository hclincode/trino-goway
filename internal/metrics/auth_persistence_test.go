package metrics_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/metrics"
)

func TestAuthMetrics_RecordsFamilies(t *testing.T) {
	reg := metrics.New()
	am, err := metrics.NewAuthMetrics(reg.Registerer())
	require.NoError(t, err)

	am.AuthRequest("oidc", "allow")
	am.AuthRequest("oidc", "deny")
	am.AuthRequest("oidc", "deny")
	am.JWKSRefresh("success")
	am.JWKSKeys(3)

	fams, err := reg.Gatherer().Gather()
	require.NoError(t, err)

	deny := findMetric(t, fams, "trino_goway_auth_requests_total",
		map[string]string{"type": "oidc", "result": "deny"})
	require.NotNil(t, deny)
	assert.Equal(t, float64(2), deny.GetCounter().GetValue())

	refresh := findMetric(t, fams, "trino_goway_jwks_refresh_total", map[string]string{"result": "success"})
	require.NotNil(t, refresh)
	assert.Equal(t, float64(1), refresh.GetCounter().GetValue())

	keys := findMetric(t, fams, "trino_goway_jwks_keys", nil)
	require.NotNil(t, keys)
	assert.Equal(t, float64(3), keys.GetGauge().GetValue())
}

func TestPersistenceMetrics_RecordsFamilies(t *testing.T) {
	reg := metrics.New()
	pm, err := metrics.NewPersistenceMetrics(reg.Registerer())
	require.NoError(t, err)

	pm.SetDBUp(true)
	pm.HistoryInsert("ok")
	pm.HistoryInsert("error")
	pm.BackendRefresh("ok")
	pm.BackendRefresh("ok")

	fams, err := reg.Gatherer().Gather()
	require.NoError(t, err)

	up := findMetric(t, fams, "trino_goway_db_up", nil)
	require.NotNil(t, up)
	assert.Equal(t, float64(1), up.GetGauge().GetValue())

	ins := findMetric(t, fams, "trino_goway_query_history_inserts_total", map[string]string{"result": "error"})
	require.NotNil(t, ins)
	assert.Equal(t, float64(1), ins.GetCounter().GetValue())

	refresh := findMetric(t, fams, "trino_goway_backend_refresh_total", map[string]string{"result": "ok"})
	require.NotNil(t, refresh)
	assert.Equal(t, float64(2), refresh.GetCounter().GetValue())

	// db_up flips to 0.
	pm.SetDBUp(false)
	fams, err = reg.Gatherer().Gather()
	require.NoError(t, err)
	up = findMetric(t, fams, "trino_goway_db_up", nil)
	require.NotNil(t, up)
	assert.Equal(t, float64(0), up.GetGauge().GetValue())
}
