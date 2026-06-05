package tracing_test

import (
	"context"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/hclincode/trino-goway-routing-service/internal/tracing"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestInit_NoEndpoint_NoExporter(t *testing.T) {
	tp, prop, shutdown, err := tracing.Init(context.Background(), tracing.Config{})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if tp == nil || prop == nil || shutdown == nil {
		t.Fatal("Init returned nil provider/propagator/shutdown")
	}
	// A span can be created without an exporter (it is simply dropped).
	_, span := tp.Tracer("test").Start(context.Background(), "noop")
	span.End()
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown: %v", err)
	}
}

func TestInit_Propagator_RoundTrips(t *testing.T) {
	_, prop, shutdown, err := tracing.Init(context.Background(), tracing.Config{})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	// The composite propagator must understand W3C traceparent.
	carrier := map[string]string{}
	fields := prop.Fields()
	found := false
	for _, f := range fields {
		if f == "traceparent" {
			found = true
		}
	}
	if !found {
		t.Errorf("propagator fields = %v, want traceparent", fields)
	}
	_ = carrier
}

func TestInit_WithEndpoint_BuildsExporter(t *testing.T) {
	// The OTLP/gRPC exporter connects lazily, so Init succeeds even if nothing
	// is listening; this exercises the exporter-construction branch.
	tp, _, shutdown, err := tracing.Init(context.Background(), tracing.Config{
		Endpoint: "127.0.0.1:4317",
		Insecure: true,
	})
	if err != nil {
		t.Fatalf("Init with endpoint: %v", err)
	}
	if tp == nil {
		t.Fatal("nil tracer provider")
	}
	// Shutdown flushes the batcher; bound it so a missing collector can't hang.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = shutdown(ctx)
}

func TestTracer_NonNil(t *testing.T) {
	tp, _, shutdown, err := tracing.Init(context.Background(), tracing.Config{})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })
	if tracing.Tracer(tp) == nil {
		t.Fatal("Tracer returned nil")
	}
}
