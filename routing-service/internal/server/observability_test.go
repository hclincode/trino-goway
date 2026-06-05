package server_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"

	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/hclincode/trino-goway-routing-service/internal/config"
	"github.com/hclincode/trino-goway-routing-service/internal/engine"
	exprovider "github.com/hclincode/trino-goway-routing-service/internal/engine/providers/expr"
	"github.com/hclincode/trino-goway-routing-service/internal/metrics"
	"github.com/hclincode/trino-goway-routing-service/internal/server"
	pb "github.com/hclincode/trino-goway-routing-service/routerpb"
)

// decidingEvaluator wraps a real pipeline whose single expr method routes
// source=="airflow" to group (deferring when group is empty, forcing fallback).
func decidingEvaluator(t *testing.T, group string) server.Evaluator {
	t.Helper()
	var methods []engine.RoutingMethod
	if group != "" {
		methods = append(methods, newExprMethodForAirflow(t, group))
	}
	p := engine.NewPipeline(methods, "default", newTestLogger(t))
	return engine.NewPipelineEvaluator(p)
}

// newExprMethodForAirflow compiles an expr method routing source=="airflow" to
// group, via the production provider (so method_type is "expr").
func newExprMethodForAirflow(t *testing.T, group string) engine.RoutingMethod {
	t.Helper()
	m := exprovider.New()
	raw := []byte("type: expr\nprogram: |\n  request.source == \"airflow\" ? \"" + group + "\" : \"\"\n")
	if err := m.LoadConfig(raw); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return m
}

func newRouteReq(source, user string) *pb.RouteRequest {
	return &pb.RouteRequest{
		TrinoSource:      source,
		TrinoRequestUser: &pb.TrinoRequestUser{User: user},
		TrinoQueryProperties: &pb.TrinoQueryProperties{
			IsNewQuerySubmission: true,
			Body:                 "SELECT 1",
		},
	}
}

// startServerWithObs starts a data-plane server with metrics + an in-memory
// trace recorder, returning the client, the recorder, and the metrics.
func startServerWithObs(t *testing.T, eval server.Evaluator) (pb.TrinoGatewayRouterClient, *tracetest.SpanRecorder, *metrics.Metrics) {
	t.Helper()

	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	prop := propagation.TraceContext{}
	m := metrics.New()

	cfg := &config.Config{Addr: ":0", MetricsAddr: ":0", AdminAddr: ":0", DefaultRoutingGroup: "default"}
	srv := server.New(cfg, eval, newTestLogger(t),
		server.WithMetrics(m),
		server.WithTracing(tp, prop),
	)
	srv.SetReady(true)

	addr, _ := startOnFreePort(t, srv)
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		_ = tp.Shutdown(context.Background())
	})
	return pb.NewTrinoGatewayRouterClient(conn), sr, m
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func TestRoute_EmitsSpanWithAttributesAndParent(t *testing.T) {
	client, sr, _ := startServerWithObs(t, decidingEvaluator(t, "etl"))

	// Inject a W3C parent trace context via gRPC metadata.
	parentTID := randHex(16) // 16 bytes → 32 hex chars
	parentSID := randHex(8)  // 8 bytes → 16 hex chars
	md := metadata.New(map[string]string{
		"traceparent": "00-" + parentTID + "-" + parentSID + "-01",
	})
	ctx := metadata.NewOutgoingContext(context.Background(), md)

	if _, err := client.Route(ctx, newRouteReq("airflow", "alice")); err != nil {
		t.Fatalf("Route: %v", err)
	}

	var decision sdktrace.ReadOnlySpan
	for _, s := range sr.Ended() {
		if s.Name() == "TrinoGatewayRouter/Route" {
			decision = s
		}
	}
	if decision == nil {
		t.Fatalf("no TrinoGatewayRouter/Route span recorded; got %d spans", len(sr.Ended()))
	}

	attrs := map[string]string{}
	for _, kv := range decision.Attributes() {
		attrs[string(kv.Key)] = kv.Value.AsString()
	}
	if attrs["routing.group"] != "etl" {
		t.Errorf("routing.group = %q, want etl", attrs["routing.group"])
	}
	if attrs["routing.source"] != "airflow" {
		t.Errorf("routing.source = %q, want airflow", attrs["routing.source"])
	}
	if attrs["routing.method_type"] != "expr" {
		t.Errorf("routing.method_type = %q, want expr", attrs["routing.method_type"])
	}

	// Parent propagation: the decision span inherits the injected trace ID.
	if got := decision.SpanContext().TraceID().String(); got != parentTID {
		t.Errorf("span trace ID = %s, want injected parent %s", got, parentTID)
	}
}

func TestRoute_RecordsDecidedMetrics(t *testing.T) {
	client, _, m := startServerWithObs(t, decidingEvaluator(t, "etl"))

	for i := 0; i < 10; i++ {
		if _, err := client.Route(context.Background(), newRouteReq("airflow", "alice")); err != nil {
			t.Fatalf("Route: %v", err)
		}
	}
	got := gatherCounter(t, m, "routing_service_requests_total", map[string]string{
		"source": "airflow", "routing_group": "etl", "method_type": "expr", "outcome": "decided",
	})
	if got != 10 {
		t.Errorf("decided requests = %v, want 10", got)
	}
}

func TestRoute_FallbackRecordsFallbackMetric(t *testing.T) {
	// No methods → every request falls back to the default group.
	client, _, m := startServerWithObs(t, decidingEvaluator(t, ""))

	for i := 0; i < 4; i++ {
		if _, err := client.Route(context.Background(), newRouteReq("superset", "bob")); err != nil {
			t.Fatalf("Route: %v", err)
		}
	}
	if got := gatherCounter(t, m, "routing_service_fallback_total", nil); got != 4 {
		t.Errorf("fallback_total = %v, want 4", got)
	}
}

func TestRoute_NonNewSubmission_NoSpanNoMetric(t *testing.T) {
	client, sr, m := startServerWithObs(t, decidingEvaluator(t, "etl"))

	// Non-new submission: service must defer immediately, no eval, no decision span.
	req := newRouteReq("airflow", "alice")
	req.TrinoQueryProperties.IsNewQuerySubmission = false
	resp, err := client.Route(context.Background(), req)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if resp.GetRoutingGroup() != "" {
		t.Errorf("routing_group = %q, want empty for non-new submission", resp.GetRoutingGroup())
	}
	for _, s := range sr.Ended() {
		if s.Name() == "TrinoGatewayRouter/Route" {
			t.Errorf("decision span emitted for non-new submission")
		}
	}
	if got := gatherCounter(t, m, "routing_service_requests_total", nil); got != 0 {
		t.Errorf("requests_total = %v, want 0 for non-new submission", got)
	}
}
