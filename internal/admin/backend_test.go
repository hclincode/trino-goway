package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/admin"
	"github.com/hclincode/trino-goway/internal/clusterstats"
	"github.com/hclincode/trino-goway/internal/clusterstatus"
	"github.com/hclincode/trino-goway/internal/monitor"
	"github.com/hclincode/trino-goway/internal/persistence"
)

// fakeStatsProvider is an admin.StatsProvider keyed by backend NAME.
type fakeStatsProvider struct {
	stats map[string]clusterstats.ClusterStats
}

func newFakeStatsProvider() *fakeStatsProvider {
	return &fakeStatsProvider{stats: make(map[string]clusterstats.ClusterStats)}
}

func (f *fakeStatsProvider) Stats(name string) clusterstats.ClusterStats {
	if cs, ok := f.stats[name]; ok {
		return cs
	}
	return clusterstats.ClusterStats{ClusterID: name}
}

// adminCfgWithStats builds a no-auth admin.Config wired with a StatsProvider.
func adminCfgWithStats(bs admin.BackendStore, hs admin.HistoryStore, sp *fakeStatusProvider, stats admin.StatsProvider) admin.Config {
	cfg := adminCfgNoAuth(bs, hs, sp)
	cfg.Stats = stats
	return cfg
}

// TestGetAllBackends_LiveCountsFromStore verifies webappGetAllBackends surfaces
// queued/running from the stats store keyed by name.
func TestGetAllBackends_LiveCountsFromStore(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()
	_ = bs.Upsert(context.Background(), persistence.Backend{
		Name: "be1", URL: "http://be1:8080", Active: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	sp.statuses["http://be1:8080"] = monitor.StatusHealthy

	stats := newFakeStatsProvider()
	stats.stats["be1"] = clusterstats.ClusterStats{ClusterID: "be1", QueuedQueryCount: 7, RunningQueryCount: 11}

	a := admin.New(adminCfgWithStats(bs, hs, sp, stats))

	rec := do(a, http.MethodPost, "/webapp/getAllBackends", []byte(`{}`))
	require.Equal(t, http.StatusOK, rec.Code)

	var env admin.Result[[]admin.BackendResponse]
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.Len(t, env.Data, 1)
	assert.Equal(t, 7, env.Data[0].Queued)
	assert.Equal(t, 11, env.Data[0].Running)
	assert.Equal(t, "HEALTHY", env.Data[0].Status)
}

// TestGetAllBackends_NilStatsZeroCounts verifies counts are 0 when no
// StatsProvider is configured (INFO_API parity).
func TestGetAllBackends_NilStatsZeroCounts(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()
	_ = bs.Upsert(context.Background(), persistence.Backend{
		Name: "be1", URL: "http://be1:8080", Active: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})

	a := admin.New(adminCfgNoAuth(bs, hs, sp)) // no Stats

	rec := do(a, http.MethodPost, "/webapp/getAllBackends", []byte(`{}`))
	require.Equal(t, http.StatusOK, rec.Code)

	var env admin.Result[[]admin.BackendResponse]
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.Len(t, env.Data, 1)
	assert.Equal(t, 0, env.Data[0].Queued)
	assert.Equal(t, 0, env.Data[0].Running)
}

// TestGetPublicBackendState_LiveStats verifies the M7 wire shape carries live
// counts, worker count, and the per-user queued breakdown under a UI_API-style
// collected snapshot.
func TestGetPublicBackendState_LiveStats(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()
	_ = bs.Upsert(context.Background(), persistence.Backend{
		Name: "be1", URL: "http://be1:8080", ExternalURL: "https://ext:8443",
		RoutingGroup: "adhoc", Active: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	sp.statuses["http://be1:8080"] = monitor.StatusHealthy

	stats := newFakeStatsProvider()
	stats.stats["be1"] = clusterstats.ClusterStats{
		ClusterID:         "be1",
		QueuedQueryCount:  2,
		RunningQueryCount: 5,
		NumWorkerNodes:    3,
		UserQueuedCount:   map[string]int{"alice": 2},
	}

	a := admin.New(adminCfgWithStats(bs, hs, sp, stats))

	rec := do(a, http.MethodGet, "/api/public/backends/be1/state", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var cs admin.ClusterStatsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &cs))
	assert.Equal(t, "be1", cs.ClusterID)
	assert.Equal(t, 5, cs.RunningQueryCount)
	assert.Equal(t, 2, cs.QueuedQueryCount)
	assert.Equal(t, 3, cs.NumWorkerNodes)
	assert.Equal(t, "HEALTHY", cs.TrinoStatus)
	assert.Equal(t, "http://be1:8080", cs.ProxyTo)
	assert.Equal(t, "https://ext:8443", cs.ExternalURL)
	assert.Equal(t, "adhoc", cs.RoutingGroup)
	assert.Equal(t, map[string]int{"alice": 2}, cs.UserQueuedCount)
}

// TestGetPublicBackendState_UnobservedDefaultFromPersistence verifies choice (b):
// a not-yet-collected backend still emits populated proxyTo/externalUrl/
// routingGroup with zero counts and absent userQueuedCount.
func TestGetPublicBackendState_UnobservedDefaultFromPersistence(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()
	_ = bs.Upsert(context.Background(), persistence.Backend{
		Name: "be1", URL: "http://be1:8080", ExternalURL: "https://ext:8443",
		RoutingGroup: "adhoc", Active: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	// Status not set ⇒ Unknown ⇒ "UNKNOWN" label.

	stats := newFakeStatsProvider() // empty ⇒ uncollected default
	a := admin.New(adminCfgWithStats(bs, hs, sp, stats))

	rec := do(a, http.MethodGet, "/api/public/backends/be1/state", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	// Exact field tags + null userQueuedCount via raw JSON.
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	_, hasUserQueued := raw["userQueuedCount"]
	assert.False(t, hasUserQueued, "userQueuedCount must be absent (omitempty) when uncollected")

	var cs admin.ClusterStatsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &cs))
	assert.Equal(t, "be1", cs.ClusterID)
	assert.Equal(t, 0, cs.QueuedQueryCount)
	assert.Equal(t, 0, cs.RunningQueryCount)
	assert.Equal(t, 0, cs.NumWorkerNodes)
	assert.Equal(t, "UNKNOWN", cs.TrinoStatus)
	assert.Equal(t, "http://be1:8080", cs.ProxyTo)
	assert.Equal(t, "https://ext:8443", cs.ExternalURL)
	assert.Equal(t, "adhoc", cs.RoutingGroup)
	assert.Nil(t, cs.UserQueuedCount)
}

// TestGetPublicBackendState_ExactWireShape pins the M7 9-field JSON tag set via a
// golden file so a tag rename is caught.
func TestGetPublicBackendState_ExactWireShape(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()
	_ = bs.Upsert(context.Background(), persistence.Backend{
		Name: "be1", URL: "http://be1:8080", ExternalURL: "https://ext:8443",
		RoutingGroup: "adhoc", Active: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	sp.statuses["http://be1:8080"] = monitor.StatusHealthy

	stats := newFakeStatsProvider()
	stats.stats["be1"] = clusterstats.ClusterStats{
		ClusterID: "be1", QueuedQueryCount: 2, RunningQueryCount: 5, NumWorkerNodes: 3,
		UserQueuedCount: map[string]int{"alice": 2},
	}
	a := admin.New(adminCfgWithStats(bs, hs, sp, stats))

	rec := do(a, http.MethodGet, "/api/public/backends/be1/state", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	// Re-marshal through a stable indent so key ordering is deterministic.
	var got admin.ClusterStatsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	pretty, err := json.MarshalIndent(got, "", "  ")
	require.NoError(t, err)
	pretty = append(pretty, '\n')

	golden := "testdata/cluster_stats_response.golden"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.WriteFile(golden, pretty, 0o600))
	}
	want, err := os.ReadFile(golden)
	require.NoError(t, err, "golden missing; run with UPDATE_GOLDEN=1")
	assert.Equal(t, string(want), string(pretty))
}

// TestTrinoStatusLabel_Unknown verifies the admin label delegates to the shared
// enum and emits "UNKNOWN" (no longer collapsed to PENDING).
func TestTrinoStatusLabel_Unknown(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()
	_ = bs.Upsert(context.Background(), persistence.Backend{
		Name: "be1", URL: "http://be1:8080", Active: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	// No status set ⇒ Unknown.
	require.Equal(t, clusterstatus.Unknown, sp.Status("http://be1:8080"))

	a := admin.New(adminCfgWithStats(bs, hs, sp, newFakeStatsProvider()))
	rec := do(a, http.MethodGet, "/api/public/backends/be1/state", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var cs admin.ClusterStatsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &cs))
	assert.Equal(t, "UNKNOWN", cs.TrinoStatus)
}
