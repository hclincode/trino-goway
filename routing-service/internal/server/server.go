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

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/hclincode/trino-goway-routing-service/internal/config"
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
	// Ready reports whether the engine has loaded at least one valid config.
	Ready() bool
}

// Server wraps a gRPC server and owns both the TrinoGatewayRouter and Health
// service registrations.
type Server struct {
	pb.UnimplementedTrinoGatewayRouterServer
	cfg    *config.Config
	log    *slog.Logger
	eval   Evaluator
	grpcs  *grpc.Server
	health *health.Server
}

// New constructs a Server. eval must not be nil.
func New(cfg *config.Config, eval Evaluator, log *slog.Logger) *Server {
	hs := health.NewServer()
	// Start NOT_SERVING; SetReady(true) transitions to SERVING once the engine
	// is ready. We override the default "" service status (which is SERVING by
	// default in health.NewServer) so that the service starts NOT_SERVING.
	hs.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)

	gs := grpc.NewServer(
		grpc.Creds(insecure.NewCredentials()),
		// Interceptor chain (unary). Order: panic recovery → (OTel seam) → (metrics seam).
		// Recovery must be outermost so it catches panics from any inner interceptor.
		// OTel and metrics seams are no-ops here; full implementations land in RS-9.
		grpc.ChainUnaryInterceptor(
			panicRecoveryInterceptor(log),
			noopOTelInterceptor,
			noopMetricsInterceptor,
		),
	)

	s := &Server{
		cfg:    cfg,
		log:    log,
		eval:   eval,
		grpcs:  gs,
		health: hs,
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

// noopOTelInterceptor is a placeholder for the OpenTelemetry tracing
// interceptor. RS-9 replaces this with otelgrpc.UnaryServerInterceptor.
func noopOTelInterceptor(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	return handler(ctx, req)
}

// noopMetricsInterceptor is a placeholder for the Prometheus metrics
// interceptor. RS-9 replaces this with real counter/histogram recording.
func noopMetricsInterceptor(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	return handler(ctx, req)
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

	group := s.eval.Evaluate(ctx, req)
	if group == "" {
		group = s.cfg.DefaultRoutingGroup
	}

	return &pb.RouteResponse{RoutingGroup: group}, nil
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

func (e *stubEvaluator) Ready() bool { return false }

// _ confirms stubEvaluator implements Evaluator at compile time.
var _ Evaluator = (*stubEvaluator)(nil)
