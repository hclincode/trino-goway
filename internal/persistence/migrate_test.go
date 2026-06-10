//go:build integration

package persistence_test

import (
	"context"
	"sync"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/config"
	"github.com/hclincode/trino-goway/internal/persistence"
	"github.com/hclincode/trino-goway/internal/testutil"
)

// TestMigrateUp_Concurrent_Postgres verifies that many replicas booting against
// one Postgres DB at the same time all succeed and apply the schema exactly once.
func TestMigrateUp_Concurrent_Postgres(t *testing.T) {
	db := testutil.PostgresContainer(t)
	runConcurrentMigrate(t, db, "postgres")
}

// TestMigrateUp_Concurrent_MySQL is the MySQL counterpart.
func TestMigrateUp_Concurrent_MySQL(t *testing.T) {
	db := testutil.MySQLContainer(t)
	runConcurrentMigrate(t, db, "mysql")
}

func runConcurrentMigrate(t *testing.T, db *sqlx.DB, driver string) {
	t.Helper()

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release all goroutines together to maximize contention
			errs[i] = persistence.MigrateUp(context.Background(), db, driver)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		require.NoErrorf(t, err, "concurrent MigrateUp goroutine %d", i)
	}

	// Schema applied and usable: the gateway_backend table exists.
	var count int
	require.NoError(t, db.Get(&count, `SELECT COUNT(*) FROM gateway_backend`))
	assert.Equal(t, 0, count)

	// Applied exactly once: no goose migration version recorded more than once,
	// which would indicate the advisory lock failed to serialize the racers.
	var dupes int
	require.NoError(t, db.Get(&dupes,
		`SELECT COUNT(*) FROM (
			SELECT version_id FROM goose_db_version GROUP BY version_id HAVING COUNT(*) > 1
		) d`))
	assert.Equal(t, 0, dupes, "no migration version may be applied more than once")
}

// TestOpen_AutoMigrateFalse_Postgres verifies that Open with AutoMigrate=false
// does not run migrations, and that a manual MigrateUp then applies them.
func TestOpen_AutoMigrateFalse_Postgres(t *testing.T) {
	runAutoMigrateFalse(t, "postgres", testutil.PostgresContainerDSN(t))
}

// TestOpen_AutoMigrateFalse_MySQL is the MySQL counterpart.
func TestOpen_AutoMigrateFalse_MySQL(t *testing.T) {
	runAutoMigrateFalse(t, "mysql", testutil.MySQLContainerDSN(t))
}

func runAutoMigrateFalse(t *testing.T, driver, dsn string) {
	t.Helper()

	ctx := context.Background()
	db, err := persistence.Open(ctx, config.DBConfig{Driver: driver, DSN: dsn, AutoMigrate: false})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// AutoMigrate=false: Open must not have created the schema, so the table is
	// absent and querying it errors.
	var count int
	require.Error(t, db.Get(&count, `SELECT COUNT(*) FROM gateway_backend`),
		"gateway_backend must not exist when autoMigrate is false")

	// A manual MigrateUp then creates the schema.
	require.NoError(t, persistence.MigrateUp(ctx, db, driver))
	require.NoError(t, db.Get(&count, `SELECT COUNT(*) FROM gateway_backend`))
	assert.Equal(t, 0, count)
}
