package sqlmeta

import (
	"reflect"
	"strings"
	"testing"
)

func TestHeuristic_Analyze(t *testing.T) {
	tests := []struct {
		name           string
		sql            string
		defaultCatalog string
		defaultSchema  string
		wantType       string
		wantCategory   Category
		wantParseOK    bool
		wantCatalogs   []string
		wantSchemas    []string
		wantCatSchemas []string
		wantTables     []string
	}{
		{
			name:           "select three-part qualified with join",
			sql:            "SELECT * FROM hive.sales.orders o JOIN hive.sales.customers c ON o.cid = c.id",
			wantType:       "SELECT",
			wantCategory:   CategoryRead,
			wantParseOK:    true,
			wantCatalogs:   []string{"hive"},
			wantSchemas:    []string{"sales"},
			wantCatSchemas: []string{"hive.sales"},
			wantTables:     []string{"hive.sales.customers", "hive.sales.orders"},
		},
		{
			name:           "insert into with select source",
			sql:            "INSERT INTO etl.staging.events SELECT * FROM raw_cat.raw_sch.events",
			wantType:       "INSERT",
			wantCategory:   CategoryWrite,
			wantParseOK:    true,
			wantCatalogs:   []string{"etl", "raw_cat"},
			wantSchemas:    []string{"raw_sch", "staging"},
			wantCatSchemas: []string{"etl.staging", "raw_cat.raw_sch"},
			wantTables:     []string{"etl.staging.events", "raw_cat.raw_sch.events"},
		},
		{
			name:           "one-part name qualified by defaults",
			sql:            "SELECT a FROM orders",
			defaultCatalog: "hive",
			defaultSchema:  "public",
			wantType:       "SELECT",
			wantCategory:   CategoryRead,
			wantParseOK:    true,
			wantCatalogs:   []string{"hive"},
			wantSchemas:    []string{"public"},
			wantCatSchemas: []string{"hive.public"},
			wantTables:     []string{"hive.public.orders"},
		},
		{
			name:           "two-part name qualified by default catalog",
			sql:            "SELECT a FROM sales.orders",
			defaultCatalog: "hive",
			defaultSchema:  "public",
			wantType:       "SELECT",
			wantCategory:   CategoryRead,
			wantParseOK:    true,
			wantCatalogs:   []string{"hive"},
			wantSchemas:    []string{"sales"},
			wantCatSchemas: []string{"hive.sales"},
			wantTables:     []string{"hive.sales.orders"},
		},
		{
			name:           "unqualified with no defaults yields bare table",
			sql:            "SELECT a FROM orders",
			wantType:       "SELECT",
			wantCategory:   CategoryRead,
			wantParseOK:    true,
			wantCatalogs:   []string{},
			wantSchemas:    []string{},
			wantCatSchemas: []string{},
			wantTables:     []string{"orders"},
		},
		{
			name:           "with CTE then select",
			sql:            "WITH t AS (SELECT * FROM hive.s.x) SELECT * FROM hive.s.y",
			wantType:       "SELECT",
			wantCategory:   CategoryRead,
			wantParseOK:    true,
			wantCatalogs:   []string{"hive"},
			wantSchemas:    []string{"s"},
			wantCatSchemas: []string{"hive.s"},
			// the CTE name "t" is not referenced here; both real tables are.
			wantTables: []string{"hive.s.x", "hive.s.y"},
		},
		{
			name:           "line comment before statement",
			sql:            "-- route me\nUPDATE pg.public.users SET x = 1 WHERE id = 2",
			wantType:       "UPDATE",
			wantCategory:   CategoryWrite,
			wantParseOK:    true,
			wantCatalogs:   []string{"pg"},
			wantSchemas:    []string{"public"},
			wantCatSchemas: []string{"pg.public"},
			wantTables:     []string{"pg.public.users"},
		},
		{
			name:           "block comment before statement",
			sql:            "/* leading\n   block */ DELETE FROM hive.s.t WHERE 1 = 1",
			wantType:       "DELETE",
			wantCategory:   CategoryWrite,
			wantParseOK:    true,
			wantCatalogs:   []string{"hive"},
			wantSchemas:    []string{"s"},
			wantCatSchemas: []string{"hive.s"},
			wantTables:     []string{"hive.s.t"},
		},
		{
			name:           "merge into captures target",
			sql:            "MERGE INTO hive.s.target t USING hive.s.src s ON t.id = s.id WHEN MATCHED THEN UPDATE SET t.v = s.v",
			wantType:       "MERGE",
			wantCategory:   CategoryWrite,
			wantParseOK:    true,
			wantCatalogs:   []string{"hive"},
			wantSchemas:    []string{"s"},
			wantCatSchemas: []string{"hive.s"},
			wantTables:     []string{"hive.s.target"},
		},
		{
			name:           "create table is DDL",
			sql:            "CREATE TABLE hive.s.newt (a INTEGER, b VARCHAR)",
			wantType:       "CREATE",
			wantCategory:   CategoryDDL,
			wantParseOK:    true,
			wantCatalogs:   []string{"hive"},
			wantSchemas:    []string{"s"},
			wantCatSchemas: []string{"hive.s"},
			wantTables:     []string{"hive.s.newt"},
		},
		{
			name:           "quoted identifiers preserve case and spaces",
			sql:            `SELECT * FROM "Hive"."My Schema"."Order Table"`,
			wantType:       "SELECT",
			wantCategory:   CategoryRead,
			wantParseOK:    true,
			wantCatalogs:   []string{"Hive"},
			wantSchemas:    []string{"My Schema"},
			wantCatSchemas: []string{"Hive.My Schema"},
			wantTables:     []string{"Hive.My Schema.Order Table"},
		},
		{
			name:           "explain reports EXPLAIN type",
			sql:            "EXPLAIN SELECT * FROM hive.s.t",
			wantType:       "EXPLAIN",
			wantCategory:   CategoryExplain,
			wantParseOK:    true,
			wantCatalogs:   []string{"hive"},
			wantSchemas:    []string{"s"},
			wantCatSchemas: []string{"hive.s"},
			wantTables:     []string{"hive.s.t"},
		},
		{
			name:           "comma-separated from list",
			sql:            "SELECT * FROM a, b, c",
			defaultCatalog: "cat",
			defaultSchema:  "sch",
			wantType:       "SELECT",
			wantCategory:   CategoryRead,
			wantParseOK:    true,
			wantCatalogs:   []string{"cat"},
			wantSchemas:    []string{"sch"},
			wantCatSchemas: []string{"cat.sch"},
			wantTables:     []string{"cat.sch.a", "cat.sch.b", "cat.sch.c"},
		},
		{
			name:         "string literal is not parsed as identifier",
			sql:          "SELECT * FROM hive.s.t WHERE name = 'FROM secret.table'",
			wantType:     "SELECT",
			wantCategory: CategoryRead,
			wantParseOK:  true,
			wantCatalogs: []string{"hive"},
			wantSchemas:  []string{"s"},
			wantTables:   []string{"hive.s.t"},
		},
		{
			name:           "values statement is a read",
			sql:            "VALUES (1, 2), (3, 4)",
			wantType:       "VALUES",
			wantCategory:   CategoryRead,
			wantParseOK:    true,
			wantCatalogs:   []string{},
			wantSchemas:    []string{},
			wantCatSchemas: []string{},
			wantTables:     []string{},
		},
		{
			name:        "non-SQL input is a parse miss",
			sql:         "this is not a query at all",
			wantType:    "",
			wantParseOK: false,
		},
		{
			name:        "empty input is a parse miss",
			sql:         "",
			wantType:    "",
			wantParseOK: false,
		},
		{
			name:        "whitespace and comment only is a parse miss",
			sql:         "   -- just a comment\n  /* and a block */  ",
			wantType:    "",
			wantParseOK: false,
		},
		{
			name:           "lowercase keywords",
			sql:            "select * from hive.s.orders",
			wantType:       "SELECT",
			wantCategory:   CategoryRead,
			wantParseOK:    true,
			wantCatalogs:   []string{"hive"},
			wantSchemas:    []string{"s"},
			wantCatSchemas: []string{"hive.s"},
			wantTables:     []string{"hive.s.orders"},
		},
		{
			name:           "bare identifiers fold to lowercase",
			sql:            "SELECT * FROM HIVE.SALES.ORDERS",
			wantType:       "SELECT",
			wantCategory:   CategoryRead,
			wantParseOK:    true,
			wantCatalogs:   []string{"hive"},
			wantSchemas:    []string{"sales"},
			wantCatSchemas: []string{"hive.sales"},
			wantTables:     []string{"hive.sales.orders"},
		},
	}

	h := NewHeuristic(0)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := h.Analyze(tc.sql, tc.defaultCatalog, tc.defaultSchema)

			if got.ParseOK != tc.wantParseOK {
				t.Fatalf("ParseOK = %v, want %v (type=%q tables=%v)", got.ParseOK, tc.wantParseOK, got.QueryType, got.Tables)
			}
			if got.QueryType != tc.wantType {
				t.Errorf("QueryType = %q, want %q", got.QueryType, tc.wantType)
			}
			if tc.wantParseOK && got.Category != tc.wantCategory {
				t.Errorf("Category = %q, want %q", got.Category, tc.wantCategory)
			}
			// Slices are always non-nil.
			assertNonNil(t, "Catalogs", got.Catalogs)
			assertNonNil(t, "Schemas", got.Schemas)
			assertNonNil(t, "CatalogSchemas", got.CatalogSchemas)
			assertNonNil(t, "Tables", got.Tables)

			if tc.wantCatalogs != nil {
				assertSlice(t, "Catalogs", got.Catalogs, tc.wantCatalogs)
			}
			if tc.wantSchemas != nil {
				assertSlice(t, "Schemas", got.Schemas, tc.wantSchemas)
			}
			if tc.wantCatSchemas != nil {
				assertSlice(t, "CatalogSchemas", got.CatalogSchemas, tc.wantCatSchemas)
			}
			if tc.wantTables != nil {
				assertSlice(t, "Tables", got.Tables, tc.wantTables)
			}
		})
	}
}

// TestHeuristic_SizeCap verifies the byte cap bounds the analysed length and
// reports truncation, while still recovering the statement head.
func TestHeuristic_SizeCap(t *testing.T) {
	h := NewHeuristic(32)
	// A long body whose head is a valid SELECT; the cap slices off the tail.
	body := "SELECT * FROM hive.s.t WHERE x IN (" + strings.Repeat("1,", 100000) + "1)"
	meta, truncated := h.AnalyzeWithTruncation(body, "", "")
	if !truncated {
		t.Fatalf("expected truncated=true for a body over the cap")
	}
	if !meta.ParseOK {
		t.Errorf("ParseOK = false; head should still classify as SELECT")
	}
	if meta.QueryType != "SELECT" {
		t.Errorf("QueryType = %q, want SELECT", meta.QueryType)
	}
}

func TestHeuristic_NotTruncatedUnderCap(t *testing.T) {
	h := NewHeuristic(0) // default cap
	_, truncated := h.AnalyzeWithTruncation("SELECT 1", "", "")
	if truncated {
		t.Errorf("small body reported as truncated")
	}
}

// TestNoop verifies the disabled-parsing analyzer never parses.
func TestNoop(t *testing.T) {
	var a SQLAnalyzer = Noop{}
	m := a.Analyze("SELECT * FROM hive.s.t", "hive", "s")
	if m.ParseOK {
		t.Errorf("Noop.Analyze ParseOK = true, want false")
	}
	assertNonNil(t, "Catalogs", m.Catalogs)
	assertNonNil(t, "Tables", m.Tables)
	if len(m.Tables) != 0 {
		t.Errorf("Noop produced tables %v, want none", m.Tables)
	}
}

func assertNonNil(t *testing.T, field string, s []string) {
	t.Helper()
	if s == nil {
		t.Errorf("%s is nil, want non-nil slice", field)
	}
}

func assertSlice(t *testing.T, field string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %v, want %v", field, got, want)
	}
}
