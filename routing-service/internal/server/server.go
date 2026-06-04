// Package server implements the TrinoGatewayRouter gRPC service and the
// grpc.health.v1.Health service.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync/atomic"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/hclincode/trino-goway-routing-service/internal/config"
	pb "github.com/hclincode/trino-goway-routing-service/routerpb"
)

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
// ctx is cancelled, then calls GracefulStop. Useful in tests to inject a
// pre-bound :0 listener and read back the OS-assigned port.
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
		s.grpcs.GracefulStop()
		return nil
	case err := <-serveErr:
		return fmt.Errorf("server: serve: %w", err)
	}
}

// Stop performs a graceful shutdown, draining in-flight RPCs before returning.
// Safe to call from a signal handler concurrently with Start.
func (s *Server) Stop() {
	s.grpcs.GracefulStop()
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
