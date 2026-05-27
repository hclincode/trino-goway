package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// QueryRecord represents a query routing history entry.
type QueryRecord struct {
	QueryID      string    `db:"query_id"`
	BackendURL   string    `db:"backend_url"`
	UserName     string    `db:"user_name"`
	Source       string    `db:"source"`
	RoutingGroup string    `db:"routing_group"`
	QueryText    string    `db:"query_text"`
	CreatedAt    time.Time `db:"created_at"`
}

// HistoryFilter describes optional filter parameters for querying history records.
type HistoryFilter struct {
	UserName   string
	BackendURL string
	QueryID    string
	Source     string
	Page       int // 1-based; default 1
	PageSize   int // default 10
}

// HistoryDAO provides access to the query_history table.
type HistoryDAO struct {
	db *sqlx.DB
}

// NewHistoryDAO returns a new HistoryDAO backed by the given database.
func NewHistoryDAO(db *sqlx.DB) *HistoryDAO {
	return &HistoryDAO{db: db}
}

// Insert records a new query routing entry.
// Uses ON CONFLICT DO NOTHING / ON DUPLICATE KEY UPDATE to handle duplicates gracefully.
func (d *HistoryDAO) Insert(ctx context.Context, r QueryRecord) error {
	var query string
	switch d.db.DriverName() {
	case "postgres":
		query = `
INSERT INTO query_history (query_id, backend_url, user_name, source, created_at)
VALUES (:query_id, :backend_url, :user_name, :source, :created_at)
ON CONFLICT (query_id) DO NOTHING`
	case "mysql":
		query = `
INSERT IGNORE INTO query_history (query_id, backend_url, user_name, source, created_at)
VALUES (:query_id, :backend_url, :user_name, :source, :created_at)`
	default:
		return fmt.Errorf("persistence: history: insert: unsupported driver %q", d.db.DriverName())
	}

	if _, err := d.db.NamedExecContext(ctx, query, r); err != nil {
		return fmt.Errorf("persistence: history: insert: %w", err)
	}
	return nil
}

// LookupByQueryID returns the backend URL for a given queryID, or "" if not found.
func (d *HistoryDAO) LookupByQueryID(ctx context.Context, queryID string) (string, error) {
	var backendURL string
	query := d.db.Rebind(`SELECT backend_url FROM query_history WHERE query_id = ?`)
	err := d.db.QueryRowContext(ctx, query, queryID).Scan(&backendURL)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("persistence: history: lookup by query id: %w", err)
	}
	return backendURL, nil
}

// ListRecent returns the most recent 'limit' query records, descending by created_at.
func (d *HistoryDAO) ListRecent(ctx context.Context, limit int) ([]QueryRecord, error) {
	if limit <= 0 {
		limit = 10
	}
	var records []QueryRecord
	var query string
	switch d.db.DriverName() {
	case "postgres":
		query = `SELECT query_id, backend_url, user_name, source, created_at FROM query_history ORDER BY created_at DESC LIMIT $1`
	case "mysql":
		query = `SELECT query_id, backend_url, user_name, source, created_at FROM query_history ORDER BY created_at DESC LIMIT ?`
	default:
		return nil, fmt.Errorf("persistence: history: list recent: unsupported driver %q", d.db.DriverName())
	}
	if err := d.db.SelectContext(ctx, &records, query, limit); err != nil {
		return nil, fmt.Errorf("persistence: history: list recent: %w", err)
	}
	return records, nil
}

// FindByFilter returns records matching the filter plus total count (for pagination).
// Filter fields are all optional (nil/empty = no filter).
func (d *HistoryDAO) FindByFilter(ctx context.Context, f HistoryFilter) ([]QueryRecord, int64, error) {
	page := f.Page
	if page < 1 {
		page = 1
	}
	pageSize := f.PageSize
	if pageSize <= 0 {
		pageSize = 10
	}
	offset := (page - 1) * pageSize

	// Build WHERE clause dynamically.
	var conditions []string
	var args []interface{}
	argIdx := 1

	placeholder := func() string {
		switch d.db.DriverName() {
		case "mysql":
			return "?"
		default:
			p := fmt.Sprintf("$%d", argIdx)
			argIdx++
			return p
		}
	}

	if f.UserName != "" {
		conditions = append(conditions, "user_name = "+placeholder())
		args = append(args, f.UserName)
	}
	if f.BackendURL != "" {
		conditions = append(conditions, "backend_url = "+placeholder())
		args = append(args, f.BackendURL)
	}
	if f.QueryID != "" {
		conditions = append(conditions, "query_id = "+placeholder())
		args = append(args, f.QueryID)
	}
	if f.Source != "" {
		conditions = append(conditions, "source = "+placeholder())
		args = append(args, f.Source)
	}

	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	// Count query — reuse same args.
	countQuery := "SELECT COUNT(*) FROM query_history" + where
	var total int64
	if err := d.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("persistence: history: find by filter count: %w", err)
	}

	// Data query — append limit/offset args.
	var dataQuery string
	switch d.db.DriverName() {
	case "mysql":
		dataQuery = "SELECT query_id, backend_url, user_name, source, created_at FROM query_history" + where +
			" ORDER BY created_at DESC LIMIT ? OFFSET ?"
	default:
		limitP := fmt.Sprintf("$%d", argIdx)
		argIdx++
		offsetP := fmt.Sprintf("$%d", argIdx)
		dataQuery = "SELECT query_id, backend_url, user_name, source, created_at FROM query_history" + where +
			" ORDER BY created_at DESC LIMIT " + limitP + " OFFSET " + offsetP
	}
	args = append(args, pageSize, offset)

	var records []QueryRecord
	if err := d.db.SelectContext(ctx, &records, dataQuery, args...); err != nil {
		return nil, 0, fmt.Errorf("persistence: history: find by filter: %w", err)
	}
	return records, total, nil
}
