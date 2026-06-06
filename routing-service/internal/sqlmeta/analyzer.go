package sqlmeta

// Category is the coarse routing class derived from a statement's QueryType.
// It lets rules route on intent (writes vs reads vs DDL) without enumerating
// every keyword.
type Category = string

const (
	// CategoryRead covers read-only queries (SELECT, WITH … SELECT, VALUES).
	CategoryRead Category = "READ"
	// CategoryWrite covers row-level writes (INSERT, UPDATE, DELETE, MERGE).
	CategoryWrite Category = "WRITE"
	// CategoryDDL covers schema/catalog definition (CREATE, DROP, ALTER, …).
	CategoryDDL Category = "DDL"
	// CategoryDML is reserved for data-management statements that are neither a
	// plain read nor a row write (currently unused by the heuristic; kept for
	// the interface contract and future backends).
	CategoryDML Category = "DML"
	// CategoryExplain covers EXPLAIN / EXPLAIN ANALYZE.
	CategoryExplain Category = "EXPLAIN"
	// CategoryOther covers everything else (SHOW, USE, CALL, SET, …) and the
	// empty/unrecognized case.
	CategoryOther Category = "OTHER"
)

// QueryMeta is the structured, provider-facing result of analysing a SQL body.
// All slices are always non-nil (possibly empty) so callers can range over them
// without a nil check. The fields are only meaningful when ParseOK is true; on a
// parse miss every slice is empty and ParseOK is false.
type QueryMeta struct {
	// QueryType is the upper-case leading statement keyword (e.g. "SELECT",
	// "INSERT", "CREATE TABLE"-style keywords collapse to "CREATE"). Empty when
	// ParseOK is false.
	QueryType string
	// Category is the coarse routing class for QueryType (READ|WRITE|DDL|DML|
	// EXPLAIN|OTHER). CategoryOther when ParseOK is false.
	Category Category
	// Catalogs is the sorted, de-duplicated set of catalog names referenced by
	// fully-qualified table refs (or resolved via the default catalog).
	Catalogs []string
	// Schemas is the sorted, de-duplicated set of schema names.
	Schemas []string
	// CatalogSchemas is the sorted, de-duplicated set of "catalog.schema" pairs.
	CatalogSchemas []string
	// Tables is the sorted, de-duplicated set of fully-qualified
	// "catalog.schema.table" references (best-effort; unqualified parts are
	// filled from the defaults when available).
	Tables []string
	// ParseOK reports whether analysis recognised the statement. False means the
	// input was empty, over the size cap with no recognisable head, or not SQL —
	// callers must fall back to header/source routing.
	ParseOK bool
}

// SQLAnalyzer derives QueryMeta from a raw SQL body. Implementations must be
// safe for concurrent use, must never perform I/O, and must never return an
// error or panic — a parse miss yields QueryMeta{ParseOK: false} with empty
// slices (PRD §5 fail-safe rule). This interface is the upgrade seam: the
// default Heuristic can be replaced by a grammar-based backend without touching
// the engine or providers.
type SQLAnalyzer interface {
	// Analyze returns the structured metadata for sql. defaultCatalog and
	// defaultSchema qualify 1- and 2-part identifiers; pass "" when unknown.
	Analyze(sql, defaultCatalog, defaultSchema string) QueryMeta
}

// emptyMeta returns a QueryMeta for an unrecognised/empty input: non-nil empty
// slices, ParseOK=false, CategoryOther.
func emptyMeta() QueryMeta {
	return QueryMeta{
		Category:       CategoryOther,
		Catalogs:       []string{},
		Schemas:        []string{},
		CatalogSchemas: []string{},
		Tables:         []string{},
		ParseOK:        false,
	}
}

// Noop is a SQLAnalyzer that never parses anything. It is injected when SQL
// parsing is disabled in config: every call returns ParseOK=false with empty
// fields, so providers see no content and fall back to header/source routing.
type Noop struct{}

// Analyze always returns an empty, ParseOK=false QueryMeta.
func (Noop) Analyze(_, _, _ string) QueryMeta { return emptyMeta() }

var _ SQLAnalyzer = Noop{}
var _ SQLAnalyzer = (*Heuristic)(nil)
