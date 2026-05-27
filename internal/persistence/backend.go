package persistence

import (
	"context"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

// Backend represents a Trino cluster backend.
type Backend struct {
	Name         string    `db:"name"`
	URL          string    `db:"url"`
	RoutingGroup string    `db:"routing_group"`
	Active       bool      `db:"active"`
	CreatedAt    time.Time `db:"created_at"`
	UpdatedAt    time.Time `db:"updated_at"`
}

// BackendDAO provides access to the gateway_backend table.
type BackendDAO struct {
	db *sqlx.DB
}

// NewBackendDAO returns a new BackendDAO backed by the given database.
func NewBackendDAO(db *sqlx.DB) *BackendDAO {
	return &BackendDAO{db: db}
}

// List returns all backends.
func (d *BackendDAO) List(ctx context.Context) ([]Backend, error) {
	var backends []Backend
	if err := d.db.SelectContext(ctx, &backends, `SELECT name, url, routing_group, active, created_at, updated_at FROM gateway_backend`); err != nil {
		return nil, fmt.Errorf("persistence: backend: list: %w", err)
	}
	return backends, nil
}

// Upsert inserts or updates a backend by name.
func (d *BackendDAO) Upsert(ctx context.Context, b Backend) error {
	var query string
	switch d.db.DriverName() {
	case "postgres":
		query = `
INSERT INTO gateway_backend (name, url, routing_group, active, created_at, updated_at)
VALUES (:name, :url, :routing_group, :active, :created_at, :updated_at)
ON CONFLICT (name) DO UPDATE SET
    url           = EXCLUDED.url,
    routing_group = EXCLUDED.routing_group,
    active        = EXCLUDED.active,
    updated_at    = EXCLUDED.updated_at`
	case "mysql":
		query = `
INSERT INTO gateway_backend (name, url, routing_group, active, created_at, updated_at)
VALUES (:name, :url, :routing_group, :active, :created_at, :updated_at)
ON DUPLICATE KEY UPDATE
    url           = VALUES(url),
    routing_group = VALUES(routing_group),
    active        = VALUES(active),
    updated_at    = VALUES(updated_at)`
	default:
		return fmt.Errorf("persistence: backend: upsert: unsupported driver %q", d.db.DriverName())
	}

	if _, err := d.db.NamedExecContext(ctx, query, b); err != nil {
		return fmt.Errorf("persistence: backend: upsert: %w", err)
	}
	return nil
}

// Delete removes a backend by name.
func (d *BackendDAO) Delete(ctx context.Context, name string) error {
	query := d.db.Rebind(`DELETE FROM gateway_backend WHERE name = ?`)
	if _, err := d.db.ExecContext(ctx, query, name); err != nil {
		return fmt.Errorf("persistence: backend: delete: %w", err)
	}
	return nil
}

// SetActive sets the active flag for a backend.
func (d *BackendDAO) SetActive(ctx context.Context, name string, active bool) error {
	query := d.db.Rebind(`UPDATE gateway_backend SET active = ?, updated_at = ? WHERE name = ?`)
	if _, err := d.db.ExecContext(ctx, query, active, time.Now().UTC(), name); err != nil {
		return fmt.Errorf("persistence: backend: set active: %w", err)
	}
	return nil
}

// ListActive returns only active backends.
func (d *BackendDAO) ListActive(ctx context.Context) ([]Backend, error) {
	var backends []Backend
	if err := d.db.SelectContext(ctx, &backends, `SELECT name, url, routing_group, active, created_at, updated_at FROM gateway_backend WHERE active = true`); err != nil {
		return nil, fmt.Errorf("persistence: backend: list active: %w", err)
	}
	return backends, nil
}
