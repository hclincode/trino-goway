package expr_test

import (
	"testing"

	"github.com/hclincode/trino-goway-routing-service/internal/engine"
)

// TestProvider_RoutesOnSQLFields verifies the UC-RTG-04 request fields
// (query_type, query_category, catalogs, schemas, tables, parse_ok) are exposed
// to expr programs and type-check / evaluate correctly.
func TestProvider_RoutesOnSQLFields(t *testing.T) {
	tests := []struct {
		name    string
		program string
		in      *engine.RouteInput
		want    engine.Decision
	}{
		{
			name:    "route writes to etl by query_type",
			program: `request.query_type == "INSERT" ? "etl" : ""`,
			in:      &engine.RouteInput{QueryType: "INSERT", ParseOK: true},
			want:    engine.Decision{RoutingGroup: "etl", Decided: true},
		},
		{
			name:    "route by query_category",
			program: `request.query_category == "WRITE" ? "etl" : ""`,
			in:      &engine.RouteInput{QueryCategory: "WRITE", ParseOK: true},
			want:    engine.Decision{RoutingGroup: "etl", Decided: true},
		},
		{
			name:    "route hive reads via catalogs membership",
			program: `"hive" in request.catalogs ? "warehouse" : ""`,
			in:      &engine.RouteInput{Catalogs: []string{"hive", "pg"}, ParseOK: true},
			want:    engine.Decision{RoutingGroup: "warehouse", Decided: true},
		},
		{
			name:    "route on tables membership",
			program: `"hive.sales.orders" in request.tables ? "sales" : ""`,
			in:      &engine.RouteInput{Tables: []string{"hive.sales.orders"}, ParseOK: true},
			want:    engine.Decision{RoutingGroup: "sales", Decided: true},
		},
		{
			name:    "parse_ok false defers to header fallback",
			program: `request.parse_ok && request.query_type == "INSERT" ? "etl" : ""`,
			in:      &engine.RouteInput{QueryType: "INSERT", ParseOK: false},
			want:    engine.Decision{Decided: false},
		},
		{
			name:    "schemas exposed",
			program: `"sales" in request.schemas ? "s" : ""`,
			in:      &engine.RouteInput{Schemas: []string{"sales"}, ParseOK: true},
			want:    engine.Decision{RoutingGroup: "s", Decided: true},
		},
		{
			name:    "nil SQL slices are safe to range / membership-test",
			program: `len(request.tables) == 0 ? "no-tables" : ""`,
			in:      &engine.RouteInput{ParseOK: false},
			want:    engine.Decision{RoutingGroup: "no-tables", Decided: true},
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
