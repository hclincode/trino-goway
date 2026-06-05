package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/hclincode/trino-goway-routing-service/routerpb"
)

// KillSwitch is the subset of the pipeline the admin service drives. It is
// satisfied by *engine.Pipeline (DisableMethod/EnableMethod/DisabledMethods,
// all atomic and effective on the next Evaluate call).
type KillSwitch interface {
	DisableMethod(typeName string)
	EnableMethod(typeName string)
	DisabledMethods() []string
}

// AdminServer serves the RoutingServiceAdmin gRPC service on a dedicated
// listener, separate from the data-plane TrinoGatewayRouter server.
//
// SECURITY (Phase 1): the admin listener has no authentication. It must be
// bound to a firewalled admin address reachable only by platform operators.
// Phase 2 adds mTLS + a scoped credential (see PRD backlog).
type AdminServer struct {
	pb.UnimplementedRoutingServiceAdminServer
	ks    KillSwitch
	log   *slog.Logger
	grpcs *grpc.Server
}

// _ confirms AdminServer implements the generated server interface.
var _ pb.RoutingServiceAdminServer = (*AdminServer)(nil)

// NewAdmin constructs an AdminServer driving ks. ks must not be nil.
func NewAdmin(ks KillSwitch, log *slog.Logger) *AdminServer {
	gs := grpc.NewServer(
		grpc.Creds(insecure.NewCredentials()),
		// Reuse the same panic-recovery discipline as the data-plane server so a
		// panic in an admin handler cannot take down the admin goroutine.
		grpc.ChainUnaryInterceptor(panicRecoveryInterceptor(log)),
	)
	a := &AdminServer{ks: ks, log: log, grpcs: gs}
	pb.RegisterRoutingServiceAdminServer(gs, a)
	return a
}

// Start binds addr and serves the admin gRPC service, blocking until ctx is
// cancelled, then performs a graceful shutdown (with the same bounded fallback
// as the data-plane server).
func (a *AdminServer) Start(ctx context.Context, addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("admin: listen %s: %w", addr, err)
	}
	return a.StartOnListener(ctx, lis)
}

// StartOnListener serves the admin service on an already-bound listener.
// Blocks until ctx is cancelled. Useful for tests (bufconn / :0).
func (a *AdminServer) StartOnListener(ctx context.Context, lis net.Listener) error {
	serveErr := make(chan error, 1)
	go func() {
		a.log.Info("routing-service: admin gRPC server started", "addr", lis.Addr())
		if err := a.grpcs.Serve(lis); err != nil {
			serveErr <- err
		}
		close(serveErr)
	}()

	select {
	case <-ctx.Done():
		a.log.Info("routing-service: context done, stopping admin gRPC server")
		a.stopWithTimeout()
		return nil
	case err := <-serveErr:
		return fmt.Errorf("admin: serve: %w", err)
	}
}

// Stop performs a graceful shutdown of the admin server. Safe to call from a
// signal handler concurrently with Start.
func (a *AdminServer) Stop() { a.stopWithTimeout() }

// stopWithTimeout mirrors the data-plane server's bounded graceful stop.
func (a *AdminServer) stopWithTimeout() {
	stopped := make(chan struct{})
	go func() {
		a.grpcs.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-newTimer(gracefulStopTimeout):
		a.log.Warn("routing-service: admin graceful stop timeout, forcing hard stop",
			"timeout", gracefulStopTimeout)
		a.grpcs.Stop()
	}
}

// DisableMethod implements pb.RoutingServiceAdminServer. It disables the named
// method (no-op for an unknown type) and returns the resulting disabled set.
func (a *AdminServer) DisableMethod(_ context.Context, req *pb.DisableMethodRequest) (*pb.DisableMethodResponse, error) {
	t := req.GetType()
	before := contains(a.ks.DisabledMethods(), t)
	a.ks.DisableMethod(t)
	disabled := a.ks.DisabledMethods()

	msg := "disabled"
	switch {
	case before:
		msg = "already disabled"
	case !contains(disabled, t):
		// The type was not present in the pipeline — DisableMethod is a no-op.
		msg = "unknown method type"
	}
	a.log.Info("admin: DisableMethod", "type", t, "result", msg, "disabled", disabled)
	return &pb.DisableMethodResponse{Ok: true, Message: msg, Disabled: disabled}, nil
}

// EnableMethod implements pb.RoutingServiceAdminServer. It re-enables the named
// method (no-op if not disabled / unknown) and returns the resulting set.
func (a *AdminServer) EnableMethod(_ context.Context, req *pb.EnableMethodRequest) (*pb.EnableMethodResponse, error) {
	t := req.GetType()
	wasDisabled := contains(a.ks.DisabledMethods(), t)
	a.ks.EnableMethod(t)
	disabled := a.ks.DisabledMethods()

	msg := "enabled"
	if !wasDisabled {
		msg = "not disabled"
	}
	a.log.Info("admin: EnableMethod", "type", t, "result", msg, "disabled", disabled)
	return &pb.EnableMethodResponse{Ok: true, Message: msg, Disabled: disabled}, nil
}

// ListDisabled implements pb.RoutingServiceAdminServer.
func (a *AdminServer) ListDisabled(_ context.Context, _ *pb.ListDisabledRequest) (*pb.ListDisabledResponse, error) {
	return &pb.ListDisabledResponse{Disabled: a.ks.DisabledMethods()}, nil
}

// contains reports whether s is in xs.
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
