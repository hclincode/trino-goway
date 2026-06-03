package persistence

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	"github.com/pressly/goose/v3"

	"github.com/hclincode/trino-goway/internal/config"
	"github.com/hclincode/trino-goway/migrations"
)

// Open opens a database connection pool for the given config.
// Driver must be "postgres" or "mysql".
// Runs goose migrations embedded from the top-level migrations package.
func Open(ctx context.Context, cfg config.DBConfig) (*sqlx.DB, error) {
	if cfg.Driver != "postgres" && cfg.Driver != "mysql" {
		return nil, fmt.Errorf("persistence: open: unsupported driver %q", cfg.Driver)
	}

	db, err := sqlx.ConnectContext(ctx, cfg.Driver, cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("persistence: open: connect: %w", err)
	}

	if err := MigrateUp(db, cfg.Driver); err != nil {
		_ = db.Close()
		return nil, err
	}

	return db, nil
}

// MigrateUp runs all embedded goose migrations against the given DB.
// Driver must be "postgres" or "mysql".
func MigrateUp(db *sqlx.DB, driver string) error {
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect(driver); err != nil {
		return fmt.Errorf("persistence: migrate: set dialect: %w", err)
	}
	if err := goose.Up(db.DB, "."); err != nil {
		return fmt.Errorf("persistence: migrate: up: %w", err)
	}
	return nil
}
