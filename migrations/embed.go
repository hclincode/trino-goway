// Package migrations is the single source of truth for the database schema.
//
// The *.sql files in this directory are goose-format migrations. They are
// embedded into the binary and applied automatically on database connect (see
// internal/persistence.MigrateUp), and operators may also apply the same files
// directly with any standard migration tool (see the README).
package migrations

import "embed"

// FS holds the embedded goose migration files (*.sql) from this directory.
//
//go:embed *.sql
var FS embed.FS
