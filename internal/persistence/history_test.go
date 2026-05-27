//go:build integration

package persistence_test

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/persistence"
	"github.com/hclincode/trino-goway/internal/testutil"
)

func TestHistoryDAO_Postgres(t *testing.T) {
	db := testutil.PostgresContainer(t)
	require.NoError(t, persistence.MigrateUp(db, "postgres"))
	runHistorySuite(t, db)
}

func TestHistoryDAO_MySQL(t *testing.T) {
	db := testutil.MySQLContainer(t)
	require.NoError(t, persistence.MigrateUp(db, "mysql"))
	runHistorySuite(t, db)
}

func runHistorySuite(t *testing.T, db *sqlx.DB) {
	t.Helper()

	dao := persistence.NewHistoryDAO(db)
	ctx := context.Background()

	t.Run("Insert and LookupByQueryID", func(t *testing.T) {
		resetHistory(t, db)

		rec := persistence.QueryRecord{
			QueryID:    "q1",
			BackendURL: "http://b1",
			UserName:   "alice",
			Source:     "cli",
			CreatedAt:  time.Now().UTC().Truncate(time.Second),
		}
		require.NoError(t, dao.Insert(ctx, rec))

		url, err := dao.LookupByQueryID(ctx, "q1")
		require.NoError(t, err)
		assert.Equal(t, "http://b1", url)
	})

	t.Run("LookupByQueryID missing returns empty string", func(t *testing.T) {
		resetHistory(t, db)

		url, err := dao.LookupByQueryID(ctx, "missing")
		require.NoError(t, err)
		assert.Empty(t, url)
	})

	t.Run("Insert duplicate query_id is a no-op", func(t *testing.T) {
		resetHistory(t, db)

		now := time.Now().UTC().Truncate(time.Second)
		first := persistence.QueryRecord{
			QueryID:    "dup",
			BackendURL: "http://first",
			UserName:   "alice",
			Source:     "cli",
			CreatedAt:  now,
		}
		require.NoError(t, dao.Insert(ctx, first))

		second := first
		second.BackendURL = "http://second"
		second.CreatedAt = now.Add(time.Minute)
		require.NoError(t, dao.Insert(ctx, second))

		url, err := dao.LookupByQueryID(ctx, "dup")
		require.NoError(t, err)
		assert.Equal(t, "http://first", url, "duplicate inserts must preserve the original backend_url")
	})

	t.Run("ListRecent orders by created_at descending", func(t *testing.T) {
		resetHistory(t, db)

		base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
		for i := 0; i < 5; i++ {
			require.NoError(t, dao.Insert(ctx, persistence.QueryRecord{
				QueryID:    fmt.Sprintf("q%d", i),
				BackendURL: fmt.Sprintf("http://b%d", i),
				UserName:   "alice",
				Source:     "cli",
				CreatedAt:  base.Add(time.Duration(i) * time.Minute),
			}))
		}

		got, err := dao.ListRecent(ctx, 3)
		require.NoError(t, err)
		require.Len(t, got, 3)
		assert.Equal(t, "q4", got[0].QueryID)
		assert.Equal(t, "q3", got[1].QueryID)
		assert.Equal(t, "q2", got[2].QueryID)
	})

	t.Run("ListRecent zero limit defaults to 10", func(t *testing.T) {
		resetHistory(t, db)

		base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
		for i := 0; i < 15; i++ {
			require.NoError(t, dao.Insert(ctx, persistence.QueryRecord{
				QueryID:    fmt.Sprintf("q%02d", i),
				BackendURL: "http://b",
				UserName:   "alice",
				Source:     "cli",
				CreatedAt:  base.Add(time.Duration(i) * time.Second),
			}))
		}

		got, err := dao.ListRecent(ctx, 0)
		require.NoError(t, err)
		assert.Len(t, got, 10)
	})

	t.Run("FindByFilter by user and pagination", func(t *testing.T) {
		resetHistory(t, db)

		base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
		users := []string{"alice", "alice", "alice", "bob", "carol"}
		for i, u := range users {
			require.NoError(t, dao.Insert(ctx, persistence.QueryRecord{
				QueryID:    fmt.Sprintf("q%d", i),
				BackendURL: "http://b",
				UserName:   u,
				Source:     "cli",
				CreatedAt:  base.Add(time.Duration(i) * time.Minute),
			}))
		}

		recs, total, err := dao.FindByFilter(ctx, persistence.HistoryFilter{
			UserName: "alice",
			Page:     1,
			PageSize: 2,
		})
		require.NoError(t, err)
		assert.EqualValues(t, 3, total)
		require.Len(t, recs, 2)
		assert.Equal(t, "q2", recs[0].QueryID)
		assert.Equal(t, "q1", recs[1].QueryID)

		recs, total, err = dao.FindByFilter(ctx, persistence.HistoryFilter{
			UserName: "alice",
			Page:     2,
			PageSize: 2,
		})
		require.NoError(t, err)
		assert.EqualValues(t, 3, total)
		require.Len(t, recs, 1)
		assert.Equal(t, "q0", recs[0].QueryID)
	})

	t.Run("FindByFilter combines multiple fields", func(t *testing.T) {
		resetHistory(t, db)

		base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
		cases := []persistence.QueryRecord{
			{QueryID: "a", BackendURL: "http://b1", UserName: "alice", Source: "cli", CreatedAt: base.Add(1 * time.Minute)},
			{QueryID: "b", BackendURL: "http://b1", UserName: "alice", Source: "ui", CreatedAt: base.Add(2 * time.Minute)},
			{QueryID: "c", BackendURL: "http://b2", UserName: "alice", Source: "cli", CreatedAt: base.Add(3 * time.Minute)},
			{QueryID: "d", BackendURL: "http://b1", UserName: "bob", Source: "cli", CreatedAt: base.Add(4 * time.Minute)},
		}
		for _, r := range cases {
			require.NoError(t, dao.Insert(ctx, r))
		}

		recs, total, err := dao.FindByFilter(ctx, persistence.HistoryFilter{
			UserName:   "alice",
			BackendURL: "http://b1",
			Source:     "cli",
		})
		require.NoError(t, err)
		assert.EqualValues(t, 1, total)
		require.Len(t, recs, 1)
		assert.Equal(t, "a", recs[0].QueryID)
	})

	t.Run("FindByFilter no filters returns all", func(t *testing.T) {
		resetHistory(t, db)

		base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
		for i := 0; i < 4; i++ {
			require.NoError(t, dao.Insert(ctx, persistence.QueryRecord{
				QueryID:    fmt.Sprintf("q%d", i),
				BackendURL: "http://b",
				UserName:   "alice",
				Source:     "cli",
				CreatedAt:  base.Add(time.Duration(i) * time.Minute),
			}))
		}

		recs, total, err := dao.FindByFilter(ctx, persistence.HistoryFilter{
			PageSize: 100,
		})
		require.NoError(t, err)
		assert.EqualValues(t, 4, total)
		require.Len(t, recs, 4)

		ids := make([]string, len(recs))
		for i, r := range recs {
			ids[i] = r.QueryID
		}
		// Sorted DESC by created_at, so q3 is first.
		assert.Equal(t, []string{"q3", "q2", "q1", "q0"}, ids)
	})

	t.Run("FindByFilter no matches returns empty", func(t *testing.T) {
		resetHistory(t, db)

		_, total, err := dao.FindByFilter(ctx, persistence.HistoryFilter{
			UserName: "nobody",
		})
		require.NoError(t, err)
		assert.EqualValues(t, 0, total)
	})

	t.Run("FindByFilter by query_id", func(t *testing.T) {
		resetHistory(t, db)

		base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
		for _, id := range []string{"a", "b", "c"} {
			require.NoError(t, dao.Insert(ctx, persistence.QueryRecord{
				QueryID:    id,
				BackendURL: "http://b",
				UserName:   "alice",
				Source:     "cli",
				CreatedAt:  base,
			}))
		}

		recs, total, err := dao.FindByFilter(ctx, persistence.HistoryFilter{
			QueryID: "b",
		})
		require.NoError(t, err)
		assert.EqualValues(t, 1, total)
		require.Len(t, recs, 1)
		assert.Equal(t, "b", recs[0].QueryID)
	})

	t.Run("ListRecent on empty table returns empty slice", func(t *testing.T) {
		resetHistory(t, db)

		got, err := dao.ListRecent(ctx, 10)
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("Inserts can be enumerated", func(t *testing.T) {
		resetHistory(t, db)

		base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
		expected := []string{"u1", "u2", "u3"}
		for i, u := range expected {
			require.NoError(t, dao.Insert(ctx, persistence.QueryRecord{
				QueryID:    fmt.Sprintf("q%d", i),
				BackendURL: "http://b",
				UserName:   u,
				Source:     "cli",
				CreatedAt:  base.Add(time.Duration(i) * time.Minute),
			}))
		}

		recs, _, err := dao.FindByFilter(ctx, persistence.HistoryFilter{PageSize: 100})
		require.NoError(t, err)
		got := make([]string, 0, len(recs))
		for _, r := range recs {
			got = append(got, r.UserName)
		}
		sort.Strings(got)
		assert.Equal(t, []string{"u1", "u2", "u3"}, got)
	})
}

func resetHistory(t *testing.T, db *sqlx.DB) {
	t.Helper()
	_, err := db.Exec(`DELETE FROM query_history`)
	require.NoError(t, err)
}
