//go:build integration

package persistence_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/persistence"
	"github.com/hclincode/trino-goway/internal/testutil"
)

func TestBackendDAO_Postgres(t *testing.T) {
	db := testutil.PostgresContainer(t)
	require.NoError(t, persistence.MigrateUp(db, "postgres"))
	runBackendSuite(t, db)
}

func TestBackendDAO_MySQL(t *testing.T) {
	db := testutil.MySQLContainer(t)
	require.NoError(t, persistence.MigrateUp(db, "mysql"))
	runBackendSuite(t, db)
}

func runBackendSuite(t *testing.T, db *sqlx.DB) {
	t.Helper()

	dao := persistence.NewBackendDAO(db)
	ctx := context.Background()

	t.Run("List empty returns no rows", func(t *testing.T) {
		resetBackends(t, db)
		got, err := dao.List(ctx)
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("Upsert inserts then updates", func(t *testing.T) {
		resetBackends(t, db)

		now := time.Now().UTC().Truncate(time.Second)
		b := persistence.Backend{
			Name:         "cluster-a",
			URL:          "http://a.example:8080",
			RoutingGroup: "adhoc",
			Active:       true,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		require.NoError(t, dao.Upsert(ctx, b))

		got, err := dao.List(ctx)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "cluster-a", got[0].Name)
		assert.Equal(t, "http://a.example:8080", got[0].URL)
		assert.Equal(t, "adhoc", got[0].RoutingGroup)
		assert.True(t, got[0].Active)

		// Re-upsert with new URL/routing group.
		b.URL = "http://a-new.example:8080"
		b.RoutingGroup = "etl"
		b.UpdatedAt = now.Add(time.Minute)
		require.NoError(t, dao.Upsert(ctx, b))

		got, err = dao.List(ctx)
		require.NoError(t, err)
		require.Len(t, got, 1, "upsert must not create a duplicate row")
		assert.Equal(t, "http://a-new.example:8080", got[0].URL)
		assert.Equal(t, "etl", got[0].RoutingGroup)
	})

	t.Run("List returns multiple backends", func(t *testing.T) {
		resetBackends(t, db)
		seedBackends(t, dao,
			persistence.Backend{Name: "a", URL: "http://a", Active: true},
			persistence.Backend{Name: "b", URL: "http://b", Active: true},
			persistence.Backend{Name: "c", URL: "http://c", Active: false},
		)

		got, err := dao.List(ctx)
		require.NoError(t, err)
		require.Len(t, got, 3)

		names := backendNames(got)
		sort.Strings(names)
		assert.Equal(t, []string{"a", "b", "c"}, names)
	})

	t.Run("Delete removes a backend", func(t *testing.T) {
		resetBackends(t, db)
		seedBackends(t, dao,
			persistence.Backend{Name: "keep", URL: "http://keep", Active: true},
			persistence.Backend{Name: "drop", URL: "http://drop", Active: true},
		)

		require.NoError(t, dao.Delete(ctx, "drop"))

		got, err := dao.List(ctx)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "keep", got[0].Name)
	})

	t.Run("Delete missing name is a no-op", func(t *testing.T) {
		resetBackends(t, db)
		require.NoError(t, dao.Delete(ctx, "does-not-exist"))
	})

	t.Run("SetActive toggles the flag", func(t *testing.T) {
		resetBackends(t, db)
		seedBackends(t, dao,
			persistence.Backend{Name: "x", URL: "http://x", Active: true},
		)

		require.NoError(t, dao.SetActive(ctx, "x", false))

		got, err := dao.List(ctx)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.False(t, got[0].Active)

		require.NoError(t, dao.SetActive(ctx, "x", true))
		got, err = dao.List(ctx)
		require.NoError(t, err)
		assert.True(t, got[0].Active)
	})

	t.Run("ListActive filters out inactive backends", func(t *testing.T) {
		resetBackends(t, db)
		seedBackends(t, dao,
			persistence.Backend{Name: "on1", URL: "http://on1", Active: true},
			persistence.Backend{Name: "on2", URL: "http://on2", Active: true},
			persistence.Backend{Name: "off", URL: "http://off", Active: false},
		)

		got, err := dao.ListActive(ctx)
		require.NoError(t, err)
		require.Len(t, got, 2)

		names := backendNames(got)
		sort.Strings(names)
		assert.Equal(t, []string{"on1", "on2"}, names)
	})
}

func resetBackends(t *testing.T, db *sqlx.DB) {
	t.Helper()
	_, err := db.Exec(`DELETE FROM gateway_backend`)
	require.NoError(t, err)
}

func seedBackends(t *testing.T, dao *persistence.BackendDAO, backends ...persistence.Backend) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	for _, b := range backends {
		if b.CreatedAt.IsZero() {
			b.CreatedAt = now
		}
		if b.UpdatedAt.IsZero() {
			b.UpdatedAt = now
		}
		require.NoError(t, dao.Upsert(context.Background(), b))
	}
}

func backendNames(bs []persistence.Backend) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.Name
	}
	return out
}
