// Package tracing wires OpenTelemetry for the routing-service. Tracing is
// optional: when the OTLP endpoint is empty the provider uses no exporter (spans
// are created but dropped), so the rest of the code can always start a span
// without a nil check. No global otel.SetTracerProvider is used — the provider
// is passed explicitly (CONVENTIONS: no global state).
package tracing

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// serviceName is the resource service.name attribute for emitted spans.
const serviceName = "routing-service"

// Config controls tracing setup.
type Config struct {
	// Endpoint is the OTLP/gRPC collector endpoint (e.g. "localhost:4317").
	// Empty disables export: spans are still created (so instrumentation code is
	// unconditional) but never shipped.
	Endpoint string
	// Insecure uses plaintext gRPC to the collector (Phase 1 default).
	Insecure bool
}

// Init builds a TracerProvider and the W3C TraceContext propagator. The returned
// shutdown func flushes and stops the provider; call it on service shutdown.
func Init(ctx context.Context, cfg Config) (*sdktrace.TracerProvider, propagation.TextMapPropagator, func(context.Context) error, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(serviceName)),
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("tracing: resource: %w", err)
	}

	var opts []sdktrace.TracerProviderOption
	opts = append(opts, sdktrace.WithResource(res))

	if cfg.Endpoint != "" {
		expOpts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(cfg.Endpoint)}
		if cfg.Insecure {
			expOpts = append(expOpts, otlptracegrpc.WithInsecure())
		}
		exp, err := otlptracegrpc.New(ctx, expOpts...)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("tracing: otlp exporter: %w", err)
		}
		opts = append(opts, sdktrace.WithBatcher(exp))
	}

	tp := sdktrace.NewTracerProvider(opts...)
	prop := propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	)
	return tp, prop, tp.Shutdown, nil
}

// Tracer returns the named tracer from tp. tp must not be nil.
func Tracer(tp trace.TracerProvider) trace.Tracer {
	return tp.Tracer(serviceName)
}
