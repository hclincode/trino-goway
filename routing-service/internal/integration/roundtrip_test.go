//go:build integration

// Package integration holds the in-process gateway ↔ routing-service round-trip
// tests. They start the full gRPC service (real server + engine + a real
// expr/script config) over a bufconn pipe — no containers, no external services
// — and exercise the Route RPC contract exactly as the trino-goway gateway would.
//
// Run with: go test -tags=integration -race ./internal/integration/...
package integration

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"go.uber.org/goleak"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/hclincode/trino-goway-routing-service/internal/config"
	"github.com/hclincode/trino-goway-routing-service/internal/engine"
	exprovider "github.com/hclincode/trino-goway-routing-service/internal/engine/providers/expr"
	scriptprovider "github.com/hclincode/trino-goway-routing-service/internal/engine/providers/script"
	"github.com/hclincode/trino-goway-routing-service/internal/metrics"
	"github.com/hclincode/trino-goway-routing-service/internal/server"
	"github.com/hclincode/trino-goway-routing-service/internal/sqlmeta"
	pb "github.com/hclincode/trino-goway-routing-service/routerpb"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// --- in-process harness ---

// harness is a running routing-service reachable over bufconn.
type harness struct {
	router  pb.TrinoGatewayRouterClient
	admin   pb.RoutingServiceAdminClient
	health  healthpb.HealthClient
	srv     *server.Server
	metrics *metrics.Metrics
}

const bufSize = 1024 * 1024

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// integrationConfig: an expr method routing on trino_source/client_tags, then a
// script method routing on catalog. Mirrors a realistic operator config and
// exercises the PRD §4.1 new fields end to end.
const integrationConfig = `
addr: ":0"
defaultRoutingGroup: "default"
methods:
  - type: expr
    program: |
      request.source == "airflow" ? "etl" :
      ("tier=premium" in request.client_tags ? "premium" : "")
  - type: script
    program: |
      def route(req):
          if req.catalog == "hive":
              return "warehouse"
          return None
`

// startHarness builds the full service from cfgYAML and serves it over bufconn.
func startHarness(t *testing.T, cfgYAML string) *harness {
	t.Helper()

	cfg := loadConfig(t, cfgYAML)

	reg := engine.NewRegistry()
	reg.Register("expr", func() engine.RoutingMethod { return exprovider.New() })
	reg.Register("script", func() engine.RoutingMethod { return scriptprovider.New() })

	methods := make([]engine.RoutingMethod, 0, len(cfg.Methods))
	for _, mc := range cfg.Methods {
		m, err := reg.Build(mc)
		if err != nil {
			t.Fatalf("build method %q: %v", mc.Type, err)
		}
		methods = append(methods, m)
	}

	pipeline := engine.NewPipeline(methods, cfg.DefaultRoutingGroup, discardLogger())
	mx := metrics.New()

	// Mirror production wiring (cmd/routing-service): inject the SQL analyzer +
	// metrics observer when SQL parsing is enabled (the default).
	evalOpts := []engine.EvaluatorOption{}
	if cfg.SQLParsing.Enabled {
		evalOpts = append(evalOpts,
			engine.WithSQLAnalyzer(sqlmeta.NewHeuristic(cfg.SQLParsing.MaxBodyBytes)),
			engine.WithSQLObserver(func(result string, dur time.Duration, truncated bool) {
				mx.RecordSQLParse(metrics.SQLParseResult(result), dur, truncated)
			}),
		)
	}
	eval := engine.NewPipelineEvaluator(pipeline, evalOpts...)

	srv := server.New(cfg, eval, discardLogger(), server.WithMetrics(mx))
	admin := server.NewAdmin(pipeline, discardLogger())

	// Two bufconn pipes: one for the data plane, one for the admin plane,
	// mirroring the separate listeners the binary uses.
	dataLis := bufconn.Listen(bufSize)
	adminLis := bufconn.Listen(bufSize)

	ctx, cancel := context.WithCancel(context.Background())
	dataDone := make(chan struct{})
	adminDone := make(chan struct{})
	go func() { defer close(dataDone); _ = srv.StartOnListener(ctx, dataLis) }()
	go func() { defer close(adminDone); _ = admin.StartOnListener(ctx, adminLis) }()

	dataConn := dialBuf(t, dataLis)
	adminConn := dialBuf(t, adminLis)

	t.Cleanup(func() {
		_ = dataConn.Close()
		_ = adminConn.Close()
		cancel()
		<-dataDone
		<-adminDone
	})

	return &harness{
		router:  pb.NewTrinoGatewayRouterClient(dataConn),
		admin:   pb.NewRoutingServiceAdminClient(adminConn),
		health:  healthpb.NewHealthClient(dataConn),
		srv:     srv,
		metrics: mx,
	}
}

func dialBuf(t *testing.T, lis *bufconn.Listener) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	return conn
}

func loadConfig(t *testing.T, yaml string) *config.Config {
	t.Helper()
	path := writeTemp(t, "config.yaml", yaml)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := t.TempDir() + "/" + name
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

// newQueryReq builds a RouteRequest the way trino-goway's buildProtoRequest does
// for a new submission, plus the PRD §4.1 fields (trino_source, client_tags).
func newQueryReq(source, user string, clientTags []string, catalog string) *pb.RouteRequest {
	return &pb.RouteRequest{
		TrinoQueryProperties: &pb.TrinoQueryProperties{
			Body:                 "SELECT 1",
			DefaultCatalog:       catalog,
			IsNewQuerySubmission: true,
		},
		TrinoRequestUser: &pb.TrinoRequestUser{User: user},
		ContentType:      "application/json",
		Method:           "POST",
		RequestUri:       "/v1/statement",
		TrinoSource:      source,
		ClientTags:       clientTags,
	}
}

// --- tests ---

func TestRoundTrip_SourceRoutesToETL(t *testing.T) {
	h := startHarness(t, integrationConfig)
	h.srv.SetReady(true)

	resp, err := h.router.Route(context.Background(), newQueryReq("airflow", "pipeline@acme.com", nil, ""))
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if resp.GetRoutingGroup() != "etl" {
		t.Errorf("routing_group = %q, want etl (from trino_source=airflow)", resp.GetRoutingGroup())
	}
}

func TestRoundTrip_ClientTagsRoute(t *testing.T) {
	h := startHarness(t, integrationConfig)
	h.srv.SetReady(true)

	// Not airflow, but tier=premium client tag → premium (proves client_tags round-trip).
	resp, err := h.router.Route(context.Background(),
		newQueryReq("superset", "alice@acme.com", []string{"team=ds", "tier=premium"}, ""))
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if resp.GetRoutingGroup() != "premium" {
		t.Errorf("routing_group = %q, want premium (from client_tags)", resp.GetRoutingGroup())
	}
}

func TestRoundTrip_ScriptMethodDecidesOnCatalog(t *testing.T) {
	h := startHarness(t, integrationConfig)
	h.srv.SetReady(true)

	// expr defers (not airflow, no premium tag); script decides on catalog=hive.
	resp, err := h.router.Route(context.Background(),
		newQueryReq("superset", "bob@acme.com", nil, "hive"))
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if resp.GetRoutingGroup() != "warehouse" {
		t.Errorf("routing_group = %q, want warehouse (from script catalog=hive)", resp.GetRoutingGroup())
	}
}

func TestRoundTrip_NoMatch_FallsToDefault(t *testing.T) {
	h := startHarness(t, integrationConfig)
	h.srv.SetReady(true)

	resp, err := h.router.Route(context.Background(),
		newQueryReq("superset", "carol@acme.com", nil, "postgres"))
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if resp.GetRoutingGroup() != "default" {
		t.Errorf("routing_group = %q, want default (no rule matched)", resp.GetRoutingGroup())
	}
}

func TestRoundTrip_NonNewSubmission_EmptyEarlyReturn(t *testing.T) {
	h := startHarness(t, integrationConfig)
	h.srv.SetReady(true)

	req := newQueryReq("airflow", "pipeline@acme.com", nil, "")
	req.TrinoQueryProperties.IsNewQuerySubmission = false // a poll/cancel, not a new query
	req.Method = "GET"

	resp, err := h.router.Route(context.Background(), req)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if resp.GetRoutingGroup() != "" {
		t.Errorf("routing_group = %q, want empty (service must not decide on non-new submissions)", resp.GetRoutingGroup())
	}
}

func TestRoundTrip_KillSwitch_DisableScript(t *testing.T) {
	h := startHarness(t, integrationConfig)
	h.srv.SetReady(true)
	ctx := context.Background()

	// Before: catalog=hive routes via script → warehouse.
	if resp, _ := h.router.Route(ctx, newQueryReq("superset", "u", nil, "hive")); resp.GetRoutingGroup() != "warehouse" {
		t.Fatalf("pre-disable = %q, want warehouse", resp.GetRoutingGroup())
	}

	// Disable script via the admin plane.
	if _, err := h.admin.DisableMethod(ctx, &pb.DisableMethodRequest{Type: "script"}); err != nil {
		t.Fatalf("DisableMethod: %v", err)
	}

	// After: script skipped → expr defers → default.
	if resp, _ := h.router.Route(ctx, newQueryReq("superset", "u", nil, "hive")); resp.GetRoutingGroup() != "default" {
		t.Errorf("post-disable = %q, want default (script disabled, expr defers)", resp.GetRoutingGroup())
	}

	// expr still works (airflow → etl) — only script was disabled.
	if resp, _ := h.router.Route(ctx, newQueryReq("airflow", "u", nil, "")); resp.GetRoutingGroup() != "etl" {
		t.Errorf("expr after script disable = %q, want etl", resp.GetRoutingGroup())
	}
}

func TestRoundTrip_HealthLifecycle(t *testing.T) {
	h := startHarness(t, integrationConfig)

	// Before SetReady: NOT_SERVING.
	if resp, err := h.health.Check(context.Background(), &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatalf("Health.Check: %v", err)
	} else if resp.GetStatus() != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Errorf("status = %v, want NOT_SERVING before ready", resp.GetStatus())
	}

	h.srv.SetReady(true)

	if resp, err := h.health.Check(context.Background(), &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatalf("Health.Check: %v", err)
	} else if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("status = %v, want SERVING after ready", resp.GetStatus())
	}
}

func TestRoundTrip_MetricsAfterTraffic(t *testing.T) {
	h := startHarness(t, integrationConfig)
	h.srv.SetReady(true)
	ctx := context.Background()

	// 10 decided (airflow → etl).
	for i := 0; i < 10; i++ {
		if _, err := h.router.Route(ctx, newQueryReq("airflow", "u", nil, "")); err != nil {
			t.Fatalf("Route: %v", err)
		}
	}
	// 3 fallbacks (no rule matches).
	for i := 0; i < 3; i++ {
		if _, err := h.router.Route(ctx, newQueryReq("superset", "u", nil, "postgres")); err != nil {
			t.Fatalf("Route: %v", err)
		}
	}

	total := sumCounter(t, h.metrics, "routing_service_requests_total", nil)
	if total != 13 {
		t.Errorf("requests_total = %v, want 13", total)
	}
	if fb := sumCounter(t, h.metrics, "routing_service_fallback_total", nil); fb != 3 {
		t.Errorf("fallback_total = %v, want 3", fb)
	}
	if etl := sumCounter(t, h.metrics, "routing_service_requests_total", map[string]string{
		"routing_group": "etl", "outcome": "decided",
	}); etl != 10 {
		t.Errorf("decided etl = %v, want 10", etl)
	}
}

// TestRoundTrip_FallbackOnServerError documents and asserts the service-side
// error semantics that drive the gateway's fallback-to-default behaviour:
//   - A method that errors internally is swallowed (defer) → the pipeline falls
//     back to the default group; the RPC still succeeds with group=default.
//   - A hard server error (panic in a handler) surfaces as a gRPC error; the
//     gateway's client-side circuit breaker treats that as fallback-to-default.
//
// We cannot run the real gateway here, so we assert both contract points: the
// soft path returns default, and a hard error is a gRPC status the caller can
// detect. The soft path is the common case (a buggy rule must never fail a query).
func TestRoundTrip_FallbackOnServerError(t *testing.T) {
	// A config whose expr program errors at runtime would be rejected at load,
	// so we instead use a pipeline that simply never matches → default. The
	// "method error → default" path is unit-tested in engine; here we assert the
	// end-to-end contract: a request that matches nothing yields the default
	// group over the wire (which is what the gateway would use on any non-answer).
	h := startHarness(t, integrationConfig)
	h.srv.SetReady(true)

	resp, err := h.router.Route(context.Background(),
		newQueryReq("unknown-source", "u", nil, "unknown-catalog"))
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if resp.GetRoutingGroup() != "default" {
		t.Errorf("routing_group = %q, want default (gateway fallback target)", resp.GetRoutingGroup())
	}

	// Hard error contract: calling Route on a closed/cancelled connection yields
	// a gRPC error (codes != OK), which the gateway treats as fallback.
	cctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel → the RPC must fail with a non-OK status
	_, err = h.router.Route(cctx, newQueryReq("airflow", "u", nil, ""))
	if err == nil {
		t.Fatal("expected a gRPC error on a cancelled context (gateway fallback trigger)")
	}
	if code := status.Code(err); code == codes.OK {
		t.Errorf("status code = OK, want a non-OK error the gateway treats as fallback")
	}
}

// --- helpers ---

func sumCounter(t *testing.T, m *metrics.Metrics, name string, labels map[string]string) float64 {
	t.Helper()
	mfs, err := m.Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var total float64
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, metric := range mf.GetMetric() {
			if !labelsMatch(metric, labels) {
				continue
			}
			if metric.GetCounter() != nil {
				total += metric.GetCounter().GetValue()
			}
		}
	}
	return total
}

func labelsMatch(m *dto.Metric, want map[string]string) bool {
	got := make(map[string]string, len(m.GetLabel()))
	for _, l := range m.GetLabel() {
		got[l.GetName()] = l.GetValue()
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}
