// Package sqlmeta provides best-effort, in-service SQL analysis for SQL-aware
// routing (UC-RTG-04). It derives a query's statement type, a coarse routing
// category, and the catalogs/schemas/tables it touches from the raw SQL body —
// without putting a full SQL parser on the gateway hot path.
//
// The default analyzer (see heuristic.go) is a pure-Go, single-pass tokenizer.
// It is comment- and string-literal aware, understands 1/2/3-part and quoted
// identifiers, resolves them against a default catalog/schema, and caps the
// analyzed length so a hostile or huge body can never stall routing. See
// docs/CONVENTIONS.md ("SQL-analysis backend") for the design rationale and the
// upgrade seam to a future grammar-based backend.
//
// Fail-safe contract: a parse miss never produces an error. The analyzer returns
// a QueryMeta with ParseOK=false and empty (but non-nil) slices; callers fall
// back to header/source routing.
//
// PII rule (docs/CONVENTIONS.md): this package never logs the raw SQL body.
// When a caller needs to reference a body in logs, it must use the
// sha256(body)[:8] prefix produced by internal/logging.BodyHash — never the raw
// text, and never the extracted identifiers beyond aggregate counts.
package sqlmeta
