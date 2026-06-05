// Package server implements the TrinoGatewayRouter gRPC service and the
// grpc.health.v1.Health service.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"runtime/debug"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/hclincode/trino-goway-routing-service/internal/config"
	"github.com/hclincode/trino-goway-routing-service/internal/engine"
	"github.com/hclincode/trino-goway-routing-service/internal/logging"
	"github.com/hclincode/trino-goway-routing-service/internal/metrics"
	pb "github.com/hclincode/trino-goway-routing-service/routerpb"
)

// gracefulStopTimeout is the maximum time to wait for in-flight RPCs to drain
// before falling back to a hard Stop. Matches the 30s deadline documented in
// the TODO and in cmd/routing-service/main.go's signal handler.
const gracefulStopTimeout = 30 * time.Second

// newTimer returns a channel that fires after d. Indirected via a variable so
// tests can override it with a shorter timeout.
var newTimer = func(d time.Duration) <-chan time.Time {
	return time.After(d)
}

// Evaluator is the interface the routing engine exposes to the server.
// RS-3 wires the real pipeline here; RS-2 provides a stub implementation.
type Evaluator interface {
	// Evaluate returns the routing group for the request, or "" to defer to
	// the gateway default. Must never return a non-nil error to the caller —
	// the pipeline swallows method errors and falls back to the default group.
	Evaluate(ctx context.Context, req *pb.RouteRequest) (routingGroup string)
	// EvaluateResult returns the routing group together with the deciding method
	// type and outcome flags, used by Route for metrics labels and decision logs.
	EvaluateResult(ctx context.Context, req *pb.RouteRequest) engine.EvalResult
	// Ready reports whether the engine has loaded at least one valid config.
	Ready() bool
}

// Server wraps a gRPC server and owns both the TrinoGatewayRouter and Health
// service registrations.
type Server struct {
	pb.UnimplementedTrinoGatewayRouterServer
	cfg     *config.Config
	log     *slog.Logger
	eval    Evaluator
	grpcs   *grpc.Server
	health  *health.Server
	metrics *metrics.Metrics         // nil when observability is not wired
	dlog    *logging.DecisionLogger  // never nil after New
	tracer  trace.Tracer             // no-op tracer when tracing is disabled
	confVer atomic.Pointer[string]   // active config version hash (for logs)
}

// Option configures optional Server behaviour (observability seams).
type Option func(*serverOpts)

type serverOpts struct {
	metrics *metrics.Metrics
	dlog    *logging.DecisionLogger
	tp      trace.TracerProvider
	prop    propagation.TextMapPropagator
}

// WithMetrics enables Prometheus recording in Route using m.
func WithMetrics(m *metrics.Metrics) Option {
	return func(o *serverOpts) { o.metrics = m }
}

// WithDecisionLogger overrides the default decision logger.
func WithDecisionLogger(dl *logging.DecisionLogger) Option {
	return func(o *serverOpts) { o.dlog = dl }
}

// WithTracing enables OpenTelemetry: the otelgrpc server interceptor extracts
// the gateway's parent trace, and Route starts a child span. Pass the provider
// and propagator from tracing.Init.
func WithTracing(tp trace.TracerProvider, prop propagation.TextMapPropagator) Option {
	return func(o *serverOpts) { o.tp = tp; o.prop = prop }
}

// New constructs a Server. eval must not be nil. Observability is opt-in via
// options; without them the server behaves exactly as the RS-2..RS-8 server
// (no metrics, no tracing, decision logs via the provided logger).
func New(cfg *config.Config, eval Evaluator, log *slog.Logger, opts ...Option) *Server {
	o := &serverOpts{}
	for _, fn := range opts {
		fn(o)
	}

	hs := health.NewServer()
	// Start NOT_SERVING; SetReady(true) transitions to SERVING once the engine
	// is ready. We override the default "" service status (which is SERVING by
	// default in health.NewServer) so that the service starts NOT_SERVING.
	hs.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)

	// Build server options. Recovery is the outermost unary interceptor so it
	// catches panics from the handler. When tracing is enabled, the otelgrpc
	// stats handler extracts the gateway's parent trace context and creates the
	// server-side RPC span (Route then starts a child span for the decision).
	srvOpts := []grpc.ServerOption{
		grpc.Creds(insecure.NewCredentials()),
		grpc.ChainUnaryInterceptor(panicRecoveryInterceptor(log)),
	}
	if o.tp != nil {
		srvOpts = append(srvOpts, grpc.StatsHandler(otelgrpc.NewServerHandler(
			otelgrpc.WithTracerProvider(o.tp),
			otelgrpc.WithPropagators(o.prop),
		)))
	}

	gs := grpc.NewServer(srvOpts...)

	dlog := o.dlog
	if dlog == nil {
		dlog = logging.NewDecisionLogger(log)
	}

	s := &Server{
		cfg:     cfg,
		log:     log,
		eval:    eval,
		grpcs:   gs,
		health:  hs,
		metrics: o.metrics,
		dlog:    dlog,
		tracer:  tracerFrom(o.tp),
	}

	pb.RegisterTrinoGatewayRouterServer(gs, s)
	healthpb.RegisterHealthServer(gs, hs)

	return s
}

// panicRecoveryInterceptor returns a unary interceptor that recovers from
// panics in downstream handlers, logs a stack trace, and converts the panic
// to a gRPC INTERNAL error so the server goroutine stays alive.
func panicRecoveryInterceptor(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				log.Error("server: panic in handler — recovered",
					"method", info.FullMethod,
					"panic", r,
					"stack", string(stack),
				)
				err = status.Errorf(codes.Internal, "internal error")
			}
		}()
		return handler(ctx, req)
	}
}

// tracerFrom returns a tracer from tp, or a no-op tracer when tp is nil so
// Route can always start a span without a nil check.
func tracerFrom(tp trace.TracerProvider) trace.Tracer {
	if tp == nil {
		return noop.NewTracerProvider().Tracer("routing-service")
	}
	return tp.Tracer("routing-service")
}

// Start begins listening on cfg.Addr and serving gRPC. It blocks until ctx is
// cancelled, then calls GracefulStop.
func (s *Server) Start(ctx context.Context) error {
	lis, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("server: listen %s: %w", s.cfg.Addr, err)
	}
	return s.StartOnListener(ctx, lis)
}

// StartOnListener serves gRPC on an already-bound listener. It blocks until
// ctx is cancelled, then performs a graceful shutdown with a 30s cap before
// falling back to a hard Stop. Useful in tests to inject a pre-bound :0
// listener and read back the OS-assigned port.
func (s *Server) StartOnListener(ctx context.Context, lis net.Listener) error {
	serveErr := make(chan error, 1)
	go func() {
		s.log.Info("routing-service: gRPC server started", "addr", lis.Addr())
		if err := s.grpcs.Serve(lis); err != nil {
			serveErr <- err
		}
		close(serveErr)
	}()

	select {
	case <-ctx.Done():
		s.log.Info("routing-service: context done, stopping gRPC server")
		s.gracefulStopWithTimeout()
		return nil
	case err := <-serveErr:
		return fmt.Errorf("server: serve: %w", err)
	}
}

// Stop performs a graceful shutdown with a 30s cap, draining in-flight RPCs.
// Falls back to a hard Stop if the drain exceeds the cap.
// Safe to call from a signal handler concurrently with Start.
func (s *Server) Stop() {
	s.gracefulStopWithTimeout()
}

// gracefulStopWithTimeout attempts GracefulStop within gracefulStopTimeout.
// If the drain does not complete in time, it calls Stop() for an immediate
// shutdown, ensuring the process can always exit within a bounded deadline.
func (s *Server) gracefulStopWithTimeout() {
	stopped := make(chan struct{})
	go func() {
		s.grpcs.GracefulStop()
		close(stopped)
	}()

	select {
	case <-stopped:
		// Clean drain — nothing to do.
	case <-newTimer(gracefulStopTimeout):
		s.log.Warn("routing-service: graceful stop timeout, forcing hard stop",
			"timeout", gracefulStopTimeout)
		s.grpcs.Stop()
	}
}

// SetReady transitions the health status. Pass true once the routing engine
// has loaded its first valid config; pass false to revert to NOT_SERVING.
// RS-3 will replace this with a callback driven by Pipeline.Ready().
func (s *Server) SetReady(ready bool) {
	if ready {
		s.health.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	} else {
		s.health.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	}
}

// Route implements pb.TrinoGatewayRouterServer.
func (s *Server) Route(ctx context.Context, req *pb.RouteRequest) (*pb.RouteResponse, error) {
	// Hard requirement from PRD §3: the service must not make a routing
	// decision for non-new submissions (polls, cancels, etc.). Return an
	// empty routing_group so the gateway uses its own default.
	if qp := req.GetTrinoQueryProperties(); qp == nil || !qp.GetIsNewQuerySubmission() {
		s.log.Debug("routing: non-new submission, skipping eval",
			"uri", req.GetRequestUri(),
			"method", req.GetMethod(),
		)
		return &pb.RouteResponse{}, nil
	}

	s.log.Debug("routing: evaluating",
		"source", req.GetTrinoSource(),
		"user", req.GetTrinoRequestUser().GetUser(),
		"uri", req.GetRequestUri(),
	)

	// Child span for the routing decision (parent = the gateway's RPC span,
	// extracted by the otelgrpc stats handler). No-op when tracing is disabled.
	ctx, span := s.tracer.Start(ctx, "TrinoGatewayRouter/Route")
	defer span.End()

	start := time.Now()
	res := s.eval.EvaluateResult(ctx, req)
	latency := time.Since(start)
	if res.RoutingGroup == "" {
		res.RoutingGroup = s.cfg.DefaultRoutingGroup
	}

	source := req.GetTrinoSource()
	outcome := classifyOutcome(res)

	// Span attributes (PRD §7): routing.group, routing.source, routing.method_type.
	span.SetAttributes(
		attribute.String("routing.group", res.RoutingGroup),
		attribute.String("routing.source", source),
		attribute.String("routing.method_type", res.MethodType),
	)

	if s.metrics != nil {
		s.metrics.RecordDecision(source, res.RoutingGroup, res.MethodType, outcome, latency)
	}

	s.dlog.Log(ctx, logging.DecisionFields{
		RuleID:            res.MethodType,
		Source:            source,
		User:              req.GetTrinoRequestUser().GetUser(),
		Body:              req.GetTrinoQueryProperties().GetBody(),
		RoutingGroup:      res.RoutingGroup,
		Latency:           latency,
		ConfigVersionHash: s.configVersion(),
		Fallback:          !res.Decided,
	})

	return &pb.RouteResponse{RoutingGroup: res.RoutingGroup}, nil
}

// classifyOutcome maps an engine result to a metrics outcome. An errored
// evaluation that still fell back is reported as "error" (not double-counted as
// fallback); a clean fallback is "fallback"; a definitive call is "decided".
func classifyOutcome(res engine.EvalResult) metrics.Outcome {
	switch {
	case res.Decided:
		return metrics.OutcomeDecided
	case res.HadError:
		return metrics.OutcomeError
	default:
		return metrics.OutcomeFallback
	}
}

// SetConfigVersion records the active config hash for decision logs (and, when
// metrics are wired, the config_version gauge). Called on startup and reload.
func (s *Server) SetConfigVersion(hash string) {
	s.confVer.Store(&hash)
	if s.metrics != nil {
		s.metrics.SetConfigVersion(hash)
	}
}

// configVersion returns the active config hash, or "" if unset.
func (s *Server) configVersion() string {
	if p := s.confVer.Load(); p != nil {
		return *p
	}
	return ""
}

// stubEvaluator is the RS-2 placeholder. It always defers (returns ""),
// causing the server to return cfg.DefaultRoutingGroup. RS-3 replaces this.
type stubEvaluator struct {
	// evalCount is incremented on every Evaluate call; used in tests to verify
	// that non-new submissions bypass evaluation entirely.
	evalCount *int64
}

// NewStubEvaluator returns a stub Evaluator suitable for RS-2 tests.
// evalCount is updated atomically; pass a non-nil pointer to observe calls.
func NewStubEvaluator(evalCount *int64) Evaluator {
	if evalCount == nil {
		var n int64
		evalCount = &n
	}
	return &stubEvaluator{evalCount: evalCount}
}

func (e *stubEvaluator) Evaluate(_ context.Context, _ *pb.RouteRequest) string {
	atomic.AddInt64(e.evalCount, 1)
	return "" // always defer; server returns default group
}

func (e *stubEvaluator) EvaluateResult(_ context.Context, _ *pb.RouteRequest) engine.EvalResult {
	atomic.AddInt64(e.evalCount, 1)
	return engine.EvalResult{Decided: false} // always defer
}

func (e *stubEvaluator) Ready() bool { return false }

// _ confirms stubEvaluator implements Evaluator at compile time.
var _ Evaluator = (*stubEvaluator)(nil)
