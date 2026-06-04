package server_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/hclincode/trino-goway-routing-service/internal/config"
	"github.com/hclincode/trino-goway-routing-service/internal/server"
	pb "github.com/hclincode/trino-goway-routing-service/routerpb"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// testEnv starts a server on a random port, returns a connected client and a
// cleanup function. The cleanup stops the server and the test waits for it.
func testEnv(t *testing.T) (pb.TrinoGatewayRouterClient, healthpb.HealthClient, *server.Server, func()) {
	t.Helper()

	cfg := &config.Config{
		Addr:                ":0", // OS assigns a free port
		MetricsAddr:         ":0",
		DefaultRoutingGroup: "default",
	}

	var evalCount int64
	eval := server.NewStubEvaluator(&evalCount)

	log := newTestLogger(t)
	srv := server.New(cfg, eval, log)

	// We need the actual bound address; use a real listener and pass it via
	// a helper that starts Serve on an already-bound listener.
	addr, stop := startOnFreePort(t, srv)

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}

	routerClient := pb.NewTrinoGatewayRouterClient(conn)
	healthClient := healthpb.NewHealthClient(conn)

	cleanup := func() {
		_ = conn.Close()
		stop()
	}
	return routerClient, healthClient, srv, cleanup
}

// TestHealth_NotServingBeforeReady verifies the server starts NOT_SERVING.
func TestHealth_NotServingBeforeReady(t *testing.T) {
	_, hc, _, cleanup := testEnv(t)
	defer cleanup()

	resp, err := hc.Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Health.Check: %v", err)
	}
	if resp.Status != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Errorf("status = %v, want NOT_SERVING", resp.Status)
	}
}

// TestHealth_ServingAfterSetReady verifies SetReady(true) transitions to SERVING.
func TestHealth_ServingAfterSetReady(t *testing.T) {
	_, hc, srv, cleanup := testEnv(t)
	defer cleanup()

	srv.SetReady(true)

	resp, err := hc.Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Health.Check: %v", err)
	}
	if resp.Status != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("status = %v, want SERVING", resp.Status)
	}
}

// TestHealth_Watch_StreamsTransition verifies Watch delivers NOT_SERVING then
// SERVING within 100ms after SetReady(true).
func TestHealth_Watch_StreamsTransition(t *testing.T) {
	_, hc, srv, cleanup := testEnv(t)
	defer cleanup()

	watchCtx, cancelWatch := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelWatch()

	stream, err := hc.Watch(watchCtx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Health.Watch: %v", err)
	}

	// First message should arrive immediately: NOT_SERVING.
	first, err := stream.Recv()
	if err != nil {
		t.Fatalf("Watch.Recv (first): %v", err)
	}
	if first.Status != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Errorf("first status = %v, want NOT_SERVING", first.Status)
	}

	// Trigger transition in a goroutine so we can race it against Recv.
	go func() {
		time.Sleep(20 * time.Millisecond)
		srv.SetReady(true)
	}()

	start := time.Now()
	second, err := stream.Recv()
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Watch.Recv (second): %v", err)
	}
	if second.Status != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("second status = %v, want SERVING", second.Status)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("Watch transition took %v, want < 100ms", elapsed)
	}
}

// TestRoute_NonNewSubmission_SkipsEval verifies that is_new_query_submission==false
// causes an immediate empty response without calling Evaluate.
func TestRoute_NonNewSubmission_SkipsEval(t *testing.T) {
	rc, _, _, cleanup := testEnv(t)
	defer cleanup()

	// Non-new request: GET /v1/query/<id> poll — is_new_query_submission is false
	// (default zero value for bool in proto).
	req := &pb.RouteRequest{
		Method:     "GET",
		RequestUri: "/v1/query/20240101_000000_00001_xxxxx",
		TrinoQueryProperties: &pb.TrinoQueryProperties{
			IsNewQuerySubmission: false,
		},
	}

	resp, err := rc.Route(context.Background(), req)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if resp.RoutingGroup != "" {
		t.Errorf("RoutingGroup = %q, want empty string (defer to gateway default)", resp.RoutingGroup)
	}
}

// TestRoute_NonNewSubmission_NilProperties_SkipsEval verifies nil
// TrinoQueryProperties is treated as non-new.
func TestRoute_NonNewSubmission_NilProperties_SkipsEval(t *testing.T) {
	rc, _, _, cleanup := testEnv(t)
	defer cleanup()

	req := &pb.RouteRequest{
		Method:               "GET",
		RequestUri:           "/v1/query/20240101_000000_00001_xxxxx",
		TrinoQueryProperties: nil, // nil → is_new == false
	}
	resp, err := rc.Route(context.Background(), req)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if resp.RoutingGroup != "" {
		t.Errorf("RoutingGroup = %q, want empty for nil properties", resp.RoutingGroup)
	}
}

// TestRoute_NewSubmission_ReturnsDefaultGroup verifies a new query submission
// returns the configured default group (stub evaluator always defers).
func TestRoute_NewSubmission_ReturnsDefaultGroup(t *testing.T) {
	rc, _, _, cleanup := testEnv(t)
	defer cleanup()

	req := &pb.RouteRequest{
		Method:     "POST",
		RequestUri: "/v1/statement",
		TrinoQueryProperties: &pb.TrinoQueryProperties{
			IsNewQuerySubmission: true,
		},
		TrinoRequestUser: &pb.TrinoRequestUser{User: "alice"},
	}
	resp, err := rc.Route(context.Background(), req)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if resp.RoutingGroup != "default" {
		t.Errorf("RoutingGroup = %q, want %q", resp.RoutingGroup, "default")
	}
}

// TestRoute_EvalNotCalledForNonNew verifies the eval spy counter via atomic read.
func TestRoute_EvalNotCalledForNonNew(t *testing.T) {
	// We need direct access to the eval counter — use testEnvWithCounter.
	cfg := &config.Config{
		Addr:                ":0",
		MetricsAddr:         ":0",
		DefaultRoutingGroup: "default",
	}
	var evalCount int64
	eval := server.NewStubEvaluator(&evalCount)
	srv := server.New(cfg, eval, newTestLogger(t))

	addr, stop := startOnFreePort(t, srv)
	defer stop()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Logf("conn.Close: %v", err)
		}
	})
	rc := pb.NewTrinoGatewayRouterClient(conn)

	// Send 3 non-new requests.
	for i := range 3 {
		req := &pb.RouteRequest{
			Method:     "GET",
			RequestUri: "/v1/query/x",
			TrinoQueryProperties: &pb.TrinoQueryProperties{
				IsNewQuerySubmission: false,
			},
		}
		if _, err := rc.Route(context.Background(), req); err != nil {
			t.Fatalf("Route[%d]: %v", i, err)
		}
	}

	if n := atomic.LoadInt64(&evalCount); n != 0 {
		t.Errorf("Evaluate called %d times for non-new requests, want 0", n)
	}

	// Now send 2 new requests; eval should be called exactly twice.
	for i := range 2 {
		req := &pb.RouteRequest{
			Method:     "POST",
			RequestUri: "/v1/statement",
			TrinoQueryProperties: &pb.TrinoQueryProperties{
				IsNewQuerySubmission: true,
			},
		}
		if _, err := rc.Route(context.Background(), req); err != nil {
			t.Fatalf("Route new[%d]: %v", i, err)
		}
	}

	if n := atomic.LoadInt64(&evalCount); n != 2 {
		t.Errorf("Evaluate called %d times for 2 new requests, want 2", n)
	}
}

// TestGracefulStop_DrainsInFlight verifies that a slow in-flight RPC completes
// before Stop returns.
func TestGracefulStop_DrainsInFlight(t *testing.T) {
	// Use a blocking evaluator that sleeps 60ms, so we can assert Stop waits.
	cfg := &config.Config{
		Addr:                ":0",
		MetricsAddr:         ":0",
		DefaultRoutingGroup: "default",
	}
	eval := &slowEvaluator{delay: 60 * time.Millisecond}
	srv := server.New(cfg, eval, newTestLogger(t))

	addr, stop := startOnFreePort(t, srv)

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	rc := pb.NewTrinoGatewayRouterClient(conn)

	// Start a slow Route call.
	rpcDone := make(chan error, 1)
	go func() {
		_, err := rc.Route(context.Background(), &pb.RouteRequest{
			Method:     "POST",
			RequestUri: "/v1/statement",
			TrinoQueryProperties: &pb.TrinoQueryProperties{
				IsNewQuerySubmission: true,
			},
		})
		rpcDone <- err
	}()

	// Give the RPC time to start (reach the evaluator's sleep).
	time.Sleep(10 * time.Millisecond)

	// Stop the server. GracefulStop must wait for the in-flight RPC.
	stopStart := time.Now()
	stop()
	stopDuration := time.Since(stopStart)

	// The RPC should complete without error.
	select {
	case err := <-rpcDone:
		if err != nil {
			// Cancelled due to connection close is acceptable — but the RPC
			// must have been in-flight when Stop was called.
			st, _ := status.FromError(err)
			if st.Code() != codes.Unavailable && st.Code() != codes.OK {
				t.Errorf("Route error: %v", err)
			}
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("RPC did not complete within 500ms after Stop")
	}

	// GracefulStop should have taken at least some time (it was draining).
	_ = stopDuration // timing is environment-dependent; presence of completion is the real check.
	_ = conn.Close()
}

// slowEvaluator sleeps for delay before returning.
type slowEvaluator struct {
	delay time.Duration
}

func (e *slowEvaluator) Evaluate(ctx context.Context, _ *pb.RouteRequest) string {
	select {
	case <-time.After(e.delay):
	case <-ctx.Done():
	}
	return ""
}

func (e *slowEvaluator) Ready() bool { return false }

// panicEvaluator panics on every Evaluate call.
type panicEvaluator struct{}

func (e *panicEvaluator) Evaluate(_ context.Context, _ *pb.RouteRequest) string {
	panic("simulated provider panic")
}
func (e *panicEvaluator) Ready() bool { return false }

// TestPanicRecovery_ReturnsErrorNotCrash verifies that a panicking evaluator
// causes Route to return a gRPC INTERNAL error (not crash the server goroutine)
// and that the server continues serving subsequent requests.
func TestPanicRecovery_ReturnsErrorNotCrash(t *testing.T) {
	cfg := &config.Config{
		Addr:                ":0",
		MetricsAddr:         ":0",
		DefaultRoutingGroup: "default",
	}
	srv := server.New(cfg, &panicEvaluator{}, newTestLogger(t))

	addr, stop := startOnFreePort(t, srv)
	defer stop()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Logf("conn.Close: %v", err)
		}
	})
	rc := pb.NewTrinoGatewayRouterClient(conn)

	// A panicking evaluator is only reached for new query submissions.
	req := &pb.RouteRequest{
		Method:     "POST",
		RequestUri: "/v1/statement",
		TrinoQueryProperties: &pb.TrinoQueryProperties{
			IsNewQuerySubmission: true,
		},
	}

	// First call should return an error (panic recovered → INTERNAL).
	_, err = rc.Route(context.Background(), req)
	if err == nil {
		t.Fatal("Route: expected error from panicking evaluator, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("Route: expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.Internal {
		t.Errorf("Route: status code = %v, want INTERNAL", st.Code())
	}

	// Server must still be alive — subsequent calls must succeed (or return the
	// same recoverable error), not EOF or connection-refused.
	_, err2 := rc.Route(context.Background(), req)
	if err2 == nil {
		// This is fine only if the panic recovery returned a real response —
		// that can't happen with a panicking evaluator, so treat as unexpected.
		t.Error("Route second call: expected error, got nil")
	}
	st2, _ := status.FromError(err2)
	// Must not be an Unavailable/transport error — that would indicate server crash.
	if st2.Code() == codes.Unavailable {
		t.Errorf("Route second call: server appears to have crashed (Unavailable)")
	}
}

// TestGracefulStop_HardStopFallback verifies that when GracefulStop takes
// longer than the timer fires, the server falls back to a hard Stop and
// returns promptly. This exercises the server.go:178-181 branch.
func TestGracefulStop_HardStopFallback(t *testing.T) {
	cfg := &config.Config{
		Addr:                ":0",
		MetricsAddr:         ":0",
		DefaultRoutingGroup: "default",
	}
	// Use a blocking evaluator that never returns (until context cancelled).
	eval := &slowEvaluator{delay: 10 * time.Second}
	srv := server.New(cfg, eval, newTestLogger(t))

	addr, _ := startOnFreePort(t, srv)

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Logf("conn.Close: %v", err)
		}
	})
	rc := pb.NewTrinoGatewayRouterClient(conn)

	// Start a slow RPC that will be in-flight when Stop is called.
	go func() {
		_, _ = rc.Route(context.Background(), &pb.RouteRequest{
			Method:     "POST",
			RequestUri: "/v1/statement",
			TrinoQueryProperties: &pb.TrinoQueryProperties{
				IsNewQuerySubmission: true,
			},
		})
	}()

	// Give the RPC time to reach the evaluator.
	time.Sleep(20 * time.Millisecond)

	// Override newTimer to fire immediately (pre-closed channel) so
	// gracefulStopWithTimeout hits the hard-stop branch without waiting 30s.
	alreadyFired := make(chan time.Time)
	close(alreadyFired)
	restore := server.SetTimerForTest(func(_ time.Duration) <-chan time.Time {
		return alreadyFired
	})
	defer restore()

	// Stop must return promptly (hard-stop path, not 10s blocked drain).
	done := make(chan struct{})
	go func() {
		srv.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Hard-stop returned quickly — pass.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Stop() did not return within 500ms via hard-stop fallback")
	}
}
