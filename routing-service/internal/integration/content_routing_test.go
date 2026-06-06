//go:build integration

package integration

import (
	"context"
	"testing"

	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	pb "github.com/hclincode/trino-goway-routing-service/routerpb"
)

// contentConfig routes on SQL content (UC-RTG-04): writes → etl, hive reads →
// analytics, else defer. Content rules are gated on request.parse_ok so an
// unparseable body falls through to source routing rather than misrouting.
const contentConfig = `
addr: ":0"
defaultRoutingGroup: "default"
sqlParsing:
  enabled: true
  maxBodyBytes: 262144
methods:
  - type: expr
    program: |
      request.parse_ok && request.query_category == "WRITE" ? "etl" :
      (request.parse_ok && "hive" in request.catalogs && request.query_type == "SELECT" ? "analytics" :
      (request.source == "airflow" ? "etl" : ""))
`

// bodyReq builds a new-submission RouteRequest carrying a SQL body but no
// pre-parsed proto fields, exactly as trino-goway v1 sends it (the gateway
// leaves query_type/catalogs empty with is_query_parsing_successful=false). The
// routing-service derives the structured fields in-process.
func bodyReq(source, body, defaultCatalog, defaultSchema string) *pb.RouteRequest {
	return &pb.RouteRequest{
		TrinoQueryProperties: &pb.TrinoQueryProperties{
			Body:                 body,
			DefaultCatalog:       defaultCatalog,
			DefaultSchema:        defaultSchema,
			IsNewQuerySubmission: true,
		},
		TrinoRequestUser: &pb.TrinoRequestUser{User: "u"},
		ContentType:      "application/json",
		Method:           "POST",
		RequestUri:       "/v1/statement",
		TrinoSource:      source,
	}
}

func TestContentRouting_WriteToETL(t *testing.T) {
	h := startHarness(t, contentConfig)
	h.srv.SetReady(true)

	resp, err := h.router.Route(context.Background(),
		bodyReq("superset", "INSERT INTO hive.staging.events SELECT * FROM raw.events", "", ""))
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if resp.GetRoutingGroup() != "etl" {
		t.Errorf("routing_group = %q, want etl (INSERT → WRITE)", resp.GetRoutingGroup())
	}
}

func TestContentRouting_HiveSelectToAnalytics(t *testing.T) {
	h := startHarness(t, contentConfig)
	h.srv.SetReady(true)

	resp, err := h.router.Route(context.Background(),
		bodyReq("superset", "SELECT * FROM hive.sales.orders", "", ""))
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if resp.GetRoutingGroup() != "analytics" {
		t.Errorf("routing_group = %q, want analytics (SELECT FROM hive)", resp.GetRoutingGroup())
	}
}

func TestContentRouting_DefaultCatalogQualifiesUnqualifiedTable(t *testing.T) {
	h := startHarness(t, contentConfig)
	h.srv.SetReady(true)

	// Unqualified table; default catalog=hive qualifies it → matches the hive rule.
	resp, err := h.router.Route(context.Background(),
		bodyReq("superset", "SELECT * FROM orders", "hive", "sales"))
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if resp.GetRoutingGroup() != "analytics" {
		t.Errorf("routing_group = %q, want analytics (default catalog hive)", resp.GetRoutingGroup())
	}
}

func TestContentRouting_UnparseableBodyFallsBackToSource(t *testing.T) {
	h := startHarness(t, contentConfig)
	h.srv.SetReady(true)

	// A non-SQL body: parse_ok=false → content rules skip → source=airflow → etl.
	// No error; health must stay SERVING.
	resp, err := h.router.Route(context.Background(),
		bodyReq("airflow", "this is not sql at all", "", ""))
	if err != nil {
		t.Fatalf("Route: %v (must never error on a parse miss)", err)
	}
	if resp.GetRoutingGroup() != "etl" {
		t.Errorf("routing_group = %q, want etl (fallback to source=airflow)", resp.GetRoutingGroup())
	}

	// Unparseable body that also has no source match → default, still no error.
	resp, err = h.router.Route(context.Background(),
		bodyReq("superset", "/* nothing routable */", "", ""))
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if resp.GetRoutingGroup() != "default" {
		t.Errorf("routing_group = %q, want default", resp.GetRoutingGroup())
	}

	// Health stays SERVING throughout.
	if hr, err := h.health.Check(context.Background(), &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatalf("Health.Check: %v", err)
	} else if hr.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("health = %v, want SERVING after parse misses", hr.GetStatus())
	}
}

func TestContentRouting_PrefersProtoParsedFields(t *testing.T) {
	h := startHarness(t, contentConfig)
	h.srv.SetReady(true)

	// Forward-compat: a (future) SQL-aware gateway pre-populates the parsed
	// fields. The service must use them verbatim, not re-parse the body. Here the
	// body looks like a hive SELECT, but the proto declares a WRITE → etl wins.
	req := bodyReq("superset", "SELECT * FROM hive.sales.orders", "", "")
	req.TrinoQueryProperties.QueryType = "INSERT"
	req.TrinoQueryProperties.Catalogs = []string{"etl_cat"}
	req.TrinoQueryProperties.IsQueryParsingSuccessful = true

	resp, err := h.router.Route(context.Background(), req)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	// The proto declares query_type=INSERT; the service derives query_category=
	// WRITE from it (CategoryForType) and routes to etl WITHOUT re-parsing the
	// body. If the service had ignored the proto and re-parsed the
	// "SELECT * FROM hive.sales.orders" body, it would have routed to analytics.
	if resp.GetRoutingGroup() != "etl" {
		t.Errorf("routing_group = %q, want etl (proto INSERT honoured, body not re-parsed)", resp.GetRoutingGroup())
	}
}
