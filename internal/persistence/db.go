package persistence

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	"github.com/pressly/goose/v3"

	"github.com/hclincode/trino-goway/internal/config"
	"github.com/hclincode/trino-goway/migrations"
)

// Migration advisory-lock parameters. The lock serializes goose.Up across
// replicas that boot together (e.g. an N-replica Helm Deployment), so the first
// replica applies the schema while the others block and then no-op once it is
// done. The Postgres key and MySQL name are arbitrary but must be identical
// across replicas for them to contend on the same lock.
const (
	// pgMigrateLockKey is the fixed key for the Postgres session-level advisory
	// lock guarding migrations (pg_advisory_lock takes a single bigint).
	pgMigrateLockKey int64 = 5_345_177_286_116_207

	// mysqlMigrateLockName names the MySQL GET_LOCK guarding migrations.
	mysqlMigrateLockName = "trino_goway_migrate"

	// migrateLockTimeout bounds how long a booting replica waits to acquire the
	// migration lock before giving up.
	migrateLockTimeout = 60 * time.Second
)

// Open opens a database connection pool for the given config.
// Driver must be "postgres" or "mysql".
// Runs goose migrations embedded from the top-level migrations package unless
// cfg.AutoMigrate is false, in which case migrations are skipped (an external
// migrate job is expected to own them).
func Open(ctx context.Context, cfg config.DBConfig) (*sqlx.DB, error) {
	if cfg.Driver != "postgres" && cfg.Driver != "mysql" {
		return nil, fmt.Errorf("persistence: open: unsupported driver %q", cfg.Driver)
	}

	db, err := sqlx.ConnectContext(ctx, cfg.Driver, cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("persistence: open: connect: %w", err)
	}

	if cfg.AutoMigrate {
		if err := MigrateUp(ctx, db, cfg.Driver); err != nil {
			_ = db.Close()
			return nil, err
		}
	}

	return db, nil
}

// MigrateUp runs all embedded goose migrations against the given DB under a
// driver-specific advisory lock, so concurrent callers (e.g. replicas booting
// together) serialize instead of racing. Driver must be "postgres" or "mysql".
//
// It uses goose's instance-based Provider rather than the package-global goose
// API so that no shared global state (dialect/base FS) is mutated; concurrent
// callers within one process are therefore independent and serialize purely on
// the database advisory lock.
func MigrateUp(ctx context.Context, db *sqlx.DB, driver string) error {
	dialect, err := gooseDialect(driver)
	if err != nil {
		return err
	}

	lockCtx, cancel := context.WithTimeout(ctx, migrateLockTimeout)
	defer cancel()

	// Advisory locks are session-scoped: acquire and release must run on the
	// same physical connection. Pull one dedicated *sql.Conn from the pool and
	// hold it for the whole lock lifetime. goose itself uses other pool
	// connections, but the lock still serializes the migration critical section
	// across replicas.
	conn, err := db.Conn(lockCtx)
	if err != nil {
		return fmt.Errorf("persistence: migrate: acquire connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	release, err := acquireMigrateLock(lockCtx, conn, driver)
	if err != nil {
		return err
	}
	defer release()

	provider, err := goose.NewProvider(dialect, db.DB, migrations.FS)
	if err != nil {
		return fmt.Errorf("persistence: migrate: new provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("persistence: migrate: up: %w", err)
	}
	return nil
}

// gooseDialect maps a driver name to the corresponding goose dialect.
func gooseDialect(driver string) (goose.Dialect, error) {
	switch driver {
	case "postgres":
		return goose.DialectPostgres, nil
	case "mysql":
		return goose.DialectMySQL, nil
	default:
		return "", fmt.Errorf("persistence: migrate: unsupported driver %q", driver)
	}
}

// acquireMigrateLock takes the driver-specific session advisory lock on conn and
// returns a release func that drops it. Callers that contend block until the
// holder releases. The release func uses a fresh context because the caller's
// context may already be done by the time the lock is dropped.
func acquireMigrateLock(ctx context.Context, conn *sql.Conn, driver string) (func(), error) {
	switch driver {
	case "postgres":
		if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", pgMigrateLockKey); err != nil {
			return nil, fmt.Errorf("persistence: migrate: acquire advisory lock: %w", err)
		}
		return func() {
			rctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			// Closing the connection also drops the session lock, so a failed
			// unlock here is not fatal.
			_, _ = conn.ExecContext(rctx, "SELECT pg_advisory_unlock($1)", pgMigrateLockKey)
		}, nil
	case "mysql":
		timeoutSecs := int(migrateLockTimeout / time.Second)
		var acquired sql.NullInt64
		if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", mysqlMigrateLockName, timeoutSecs).Scan(&acquired); err != nil {
			return nil, fmt.Errorf("persistence: migrate: acquire lock %q: %w", mysqlMigrateLockName, err)
		}
		// GET_LOCK returns 1 on success, 0 on timeout, NULL on error.
		if !acquired.Valid || acquired.Int64 != 1 {
			return nil, fmt.Errorf("persistence: migrate: acquire lock %q: timed out after %s", mysqlMigrateLockName, migrateLockTimeout)
		}
		return func() {
			rctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _ = conn.ExecContext(rctx, "SELECT RELEASE_LOCK(?)", mysqlMigrateLockName)
		}, nil
	default:
		return nil, fmt.Errorf("persistence: migrate: unsupported driver %q", driver)
	}
}
