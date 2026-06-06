package script_test

import (
	"testing"

	"github.com/hclincode/trino-goway-routing-service/internal/engine"
)

// TestProvider_RoutesOnSQLFields verifies the UC-RTG-04 request attributes
// (query_type, query_category, catalogs, schemas, tables, parse_ok) are exposed
// to Starlark route(req) functions.
func TestProvider_RoutesOnSQLFields(t *testing.T) {
	tests := []struct {
		name    string
		program string
		in      *engine.RouteInput
		want    engine.Decision
	}{
		{
			name: "route writes to etl by query_type",
			program: `
def route(req):
    if req.query_type == "INSERT":
        return "etl"
    return None
`,
			in:   &engine.RouteInput{QueryType: "INSERT", ParseOK: true},
			want: engine.Decision{RoutingGroup: "etl", Decided: true},
		},
		{
			name: "route by query_category",
			program: `
def route(req):
    return "etl" if req.query_category == "WRITE" else None
`,
			in:   &engine.RouteInput{QueryCategory: "WRITE", ParseOK: true},
			want: engine.Decision{RoutingGroup: "etl", Decided: true},
		},
		{
			name: "route hive reads via catalogs membership",
			program: `
def route(req):
    return "warehouse" if "hive" in req.catalogs else None
`,
			in:   &engine.RouteInput{Catalogs: []string{"hive", "pg"}, ParseOK: true},
			want: engine.Decision{RoutingGroup: "warehouse", Decided: true},
		},
		{
			name: "route on tables membership",
			program: `
def route(req):
    return "sales" if "hive.sales.orders" in req.tables else None
`,
			in:   &engine.RouteInput{Tables: []string{"hive.sales.orders"}, ParseOK: true},
			want: engine.Decision{RoutingGroup: "sales", Decided: true},
		},
		{
			name: "parse_ok false defers to header fallback",
			program: `
def route(req):
    if req.parse_ok and req.query_type == "INSERT":
        return "etl"
    return None
`,
			in:   &engine.RouteInput{QueryType: "INSERT", ParseOK: false},
			want: engine.Decision{Decided: false},
		},
		{
			name: "schemas exposed and empty slices safe",
			program: `
def route(req):
    if len(req.tables) == 0 and "sales" in req.schemas:
        return "s"
    return None
`,
			in:   &engine.RouteInput{Schemas: []string{"sales"}, ParseOK: true},
			want: engine.Decision{RoutingGroup: "s", Decided: true},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newProvider(t, tc.program)
			got := eval(t, p, tc.in)
			if got.Decided != tc.want.Decided || got.RoutingGroup != tc.want.RoutingGroup {
				t.Errorf("decision = {group:%q decided:%v}, want {group:%q decided:%v}",
					got.RoutingGroup, got.Decided, tc.want.RoutingGroup, tc.want.Decided)
			}
		})
	}
}
