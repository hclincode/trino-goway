//go:build e2e

package e2e_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/hclincode/trino-goway/internal/e2e/harness"
	"github.com/hclincode/trino-goway/internal/routing/routerpb"
)

// grpcRouterRecorder is an in-process gRPC implementation of
// TrinoGatewayRouter that records every RouteRequest received. The server
// binds to a real localhost TCP port so the trino-goway subprocess can reach it.
type grpcRouterRecorder struct {
	routerpb.UnimplementedTrinoGatewayRouterServer

	addr string

	mu          sync.Mutex
	calls       int
	lastRequest *routerpb.RouteRequest
	group       string
}

func newGRPCRouter(t *testing.T, group string) *grpcRouterRecorder {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "grpc router: listen")

	rec := &grpcRouterRecorder{
		group: group,
		addr:  lis.Addr().String(),
	}

	srv := grpc.NewServer()
	routerpb.RegisterTrinoGatewayRouterServer(srv, rec)

	// goroutine exits when srv.GracefulStop is called via t.Cleanup.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(lis)
	}()

	t.Cleanup(func() {
		srv.GracefulStop()
		<-done
	})

	return rec
}

func (r *grpcRouterRecorder) Addr() string { return r.addr }

func (r *grpcRouterRecorder) Route(_ context.Context, req *routerpb.RouteRequest) (*routerpb.RouteResponse, error) {
	r.mu.Lock()
	r.calls++
	r.lastRequest = req
	group := r.group
	r.mu.Unlock()

	return &routerpb.RouteResponse{
		RoutingGroup:    group,
		Errors:          []string{},
		ExternalHeaders: map[string]string{},
	}, nil
}

func (r *grpcRouterRecorder) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *grpcRouterRecorder) LastRequest() *routerpb.RouteRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastRequest
}

// TestE2E_ExternalGRPC_RoutingGroupUsed asserts that a routing group returned
// over gRPC steers the request to a backend in that group.
func TestE2E_ExternalGRPC_RoutingGroupUsed(t *testing.T) {
	router := newGRPCRouter(t, "etl")
	h := harness.New(t, harness.WithExternalGRPCRouter(router.Addr()))

	etl := h.AddBackend(t, "etl-backend", "etl")

	resp, body := postStatement(t, h, "SELECT 1", nil)
	defer resp.Body.Close()
	require.Equalf(t, http.StatusOK, resp.StatusCode, "status=%d body=%s", resp.StatusCode, string(body))

	assert.GreaterOrEqual(t, router.Calls(), 1, "gRPC router must have been called")
	assert.Len(t, etl.QueryIDs(), 1, "etl-backend must receive the POST")
}

// TestE2E_ExternalGRPC_FallbackToHTTP asserts that when the gRPC address is
// unreachable but the HTTP URL is reachable, the gateway falls back to the
// HTTP transport and routes successfully.
func TestE2E_ExternalGRPC_FallbackToHTTP(t *testing.T) {
	httpRouter := newHTTPRouter(t, "default", nil, nil)

	// Pick a localhost port nobody is listening on so the gRPC dial fails on
	// the first call. grpc.NewClient is lazy so dial errors surface at Route().
	deadPort := allocAndCloseTCP(t)

	h := harness.New(t,
		harness.WithExternalGRPCRouter(fmt.Sprintf("127.0.0.1:%d", deadPort)),
		harness.WithExternalHTTPRouter(httpRouter.URL()),
	)
	backend := h.AddBackend(t, "default-backend", "default")

	resp, body := postStatement(t, h, "SELECT 1", nil)
	defer resp.Body.Close()
	require.Equalf(t, http.StatusOK, resp.StatusCode,
		"gRPC down → HTTP fallback must succeed, got status=%d body=%s",
		resp.StatusCode, string(body))

	assert.GreaterOrEqual(t, httpRouter.Calls(), 1, "HTTP router must be called after gRPC fails")
	assert.Len(t, backend.QueryIDs(), 1)
}

// TestE2E_ExternalGRPC_FallbackOnBothDown asserts that when both gRPC and HTTP
// routing services are unreachable, the gateway still serves the request via
// the defaultGroup fallback.
func TestE2E_ExternalGRPC_FallbackOnBothDown(t *testing.T) {
	deadGRPC := allocAndCloseTCP(t)

	h := harness.New(t,
		harness.WithExternalGRPCRouter(fmt.Sprintf("127.0.0.1:%d", deadGRPC)),
		harness.WithExternalHTTPRouter("http://127.0.0.1:1"),
	)
	backend := h.AddBackend(t, "default-backend", "default")

	resp, body := postStatement(t, h, "SELECT 1", nil)
	defer resp.Body.Close()
	require.Equalf(t, http.StatusOK, resp.StatusCode,
		"both routers down must fall back to defaultGroup, got status=%d body=%s",
		resp.StatusCode, string(body))

	assert.Len(t, backend.QueryIDs(), 1)
}

// TestE2E_ExternalGRPC_RouteRequestEquivalence asserts that the proto
// RouteRequest the gateway sends contains the expected request metadata:
// method, request URI, and the parsed X-Trino-User header.
func TestE2E_ExternalGRPC_RouteRequestEquivalence(t *testing.T) {
	router := newGRPCRouter(t, "default")
	h := harness.New(t, harness.WithExternalGRPCRouter(router.Addr()))
	h.AddBackend(t, "default-backend", "default")

	hdr := http.Header{}
	hdr.Set("X-Trino-User", "alice")
	resp, body := postStatement(t, h, "SELECT 1", hdr)
	defer resp.Body.Close()
	require.Equalf(t, http.StatusOK, resp.StatusCode, "status=%d body=%s", resp.StatusCode, string(body))

	req := router.LastRequest()
	require.NotNil(t, req, "router must have received a RouteRequest")

	assert.Equal(t, "POST", req.GetMethod(), "RouteRequest.method must be POST")
	assert.Equal(t, "/v1/statement", req.GetRequestUri(), "RouteRequest.request_uri")

	rtu := req.GetTrinoRequestUser()
	require.NotNil(t, rtu, "RouteRequest.trino_request_user must be set when X-Trino-User present")
	assert.Equal(t, "alice", rtu.GetUser(), "trino_request_user.user must match X-Trino-User")
}

// allocAndCloseTCP returns a localhost TCP port that was bound briefly and
// released, so subsequent dial attempts fail with "connection refused".
func allocAndCloseTCP(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}
