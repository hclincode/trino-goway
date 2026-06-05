package server_test

import (
	"context"
	"net"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/hclincode/trino-goway-routing-service/internal/engine"
	exprovider "github.com/hclincode/trino-goway-routing-service/internal/engine/providers/expr"
	"github.com/hclincode/trino-goway-routing-service/internal/server"
	pb "github.com/hclincode/trino-goway-routing-service/routerpb"
)

// --- helpers ---

// newExprMethod compiles an expr method that routes source=="a" to group,
// deferring otherwise. Built via the production provider for fidelity.
func newExprMethod(t *testing.T, group string) engine.RoutingMethod {
	t.Helper()
	m := exprovider.New()
	// Use the block-scalar form ("program: |") the provider's config parser
	// expects; an inline quoted scalar would be read as a literal string.
	raw := []byte("type: expr\nprogram: |\n  request.source == \"a\" ? \"" + group + "\" : \"\"\n")
	if err := m.LoadConfig(raw); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return m
}

// pipelineWithMethods builds a real pipeline. The expr method always uses
// Type()=="expr"; to have two distinguishable methods we wrap the second in a
// renaming adapter so DisabledMethods can address them separately.
func newTwoMethodPipeline(t *testing.T) *engine.Pipeline {
	t.Helper()
	first := newExprMethod(t, "from-expr")
	second := &renamed{inner: newExprMethod(t, "from-second"), name: "second"}
	return engine.NewPipeline([]engine.RoutingMethod{first, second}, "default", newTestLogger(t))
}

// renamed wraps a RoutingMethod to report a different Type(), so a pipeline can
// hold two methods that the kill-switch addresses independently.
type renamed struct {
	inner engine.RoutingMethod
	name  string
}

func (r *renamed) Type() string                  { return r.name }
func (r *renamed) LoadConfig(raw []byte) error   { return r.inner.LoadConfig(raw) }
func (r *renamed) Evaluate(ctx context.Context, in *engine.RouteInput) (engine.Decision, error) {
	return r.inner.Evaluate(ctx, in)
}

// startAdmin starts an AdminServer on a free port and returns a connected
// client. The server stops on cleanup.
func startAdmin(t *testing.T, ks server.KillSwitch) pb.RoutingServiceAdminClient {
	t.Helper()
	a := server.NewAdmin(ks, newTestLogger(t))

	lis, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := a.StartOnListener(ctx, lis); err != nil {
			t.Logf("admin StartOnListener: %v", err)
		}
	}()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		cancel()
		<-done
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		cancel()
		<-done
	})
	return pb.NewRoutingServiceAdminClient(conn)
}

// --- tests: direct pipeline (sub-ms, no restart) ---

func TestKillSwitch_DisableSkipsMethod_NextCall(t *testing.T) {
	p := newTwoMethodPipeline(t)
	in := &engine.RouteInput{Source: "a", IsNew: true}

	// Both methods decide on source=="a"; the first (expr) wins.
	if got := p.Evaluate(context.Background(), in); got != "from-expr" {
		t.Fatalf("before disable: got %q, want from-expr", got)
	}

	// Disable expr, then immediately (same goroutine, no sleep) evaluate: the
	// second method must now decide — proving next-call effect, no restart.
	p.DisableMethod("expr")
	if got := p.Evaluate(context.Background(), in); got != "from-second" {
		t.Fatalf("after disable expr: got %q, want from-second", got)
	}
}

func TestKillSwitch_DisableBoth_FallsToDefault(t *testing.T) {
	p := newTwoMethodPipeline(t)
	in := &engine.RouteInput{Source: "a", IsNew: true}

	p.DisableMethod("expr")
	p.DisableMethod("second")
	if got := p.Evaluate(context.Background(), in); got != "default" {
		t.Fatalf("both disabled: got %q, want default", got)
	}
}

// --- tests: admin RPC over the wire ---

func TestAdmin_DisableEnableRoundTrip(t *testing.T) {
	p := newTwoMethodPipeline(t)
	client := startAdmin(t, p)
	ctx := context.Background()
	in := &engine.RouteInput{Source: "a", IsNew: true}

	// Disable via RPC → first method skipped on the next pipeline call.
	resp, err := client.DisableMethod(ctx, &pb.DisableMethodRequest{Type: "expr"})
	if err != nil {
		t.Fatalf("DisableMethod RPC: %v", err)
	}
	if !resp.GetOk() || !contains(resp.GetDisabled(), "expr") {
		t.Fatalf("DisableMethod resp = %+v, want ok + expr disabled", resp)
	}
	if got := p.Evaluate(ctx, in); got != "from-second" {
		t.Fatalf("after RPC disable: got %q, want from-second", got)
	}

	// ListDisabled reflects state.
	lst, err := client.ListDisabled(ctx, &pb.ListDisabledRequest{})
	if err != nil {
		t.Fatalf("ListDisabled RPC: %v", err)
	}
	if !contains(lst.GetDisabled(), "expr") || len(lst.GetDisabled()) != 1 {
		t.Fatalf("ListDisabled = %v, want [expr]", lst.GetDisabled())
	}

	// Enable via RPC → first method decides again.
	er, err := client.EnableMethod(ctx, &pb.EnableMethodRequest{Type: "expr"})
	if err != nil {
		t.Fatalf("EnableMethod RPC: %v", err)
	}
	if !er.GetOk() || contains(er.GetDisabled(), "expr") {
		t.Fatalf("EnableMethod resp = %+v, want ok + expr not disabled", er)
	}
	if got := p.Evaluate(ctx, in); got != "from-expr" {
		t.Fatalf("after RPC enable: got %q, want from-expr", got)
	}
}

func TestAdmin_DisableUnknownType_NoOp(t *testing.T) {
	p := newTwoMethodPipeline(t)
	client := startAdmin(t, p)
	ctx := context.Background()

	resp, err := client.DisableMethod(ctx, &pb.DisableMethodRequest{Type: "nope"})
	if err != nil {
		t.Fatalf("DisableMethod(unknown) RPC: %v", err)
	}
	if !resp.GetOk() {
		t.Errorf("resp.Ok = false, want true (idempotent no-op)")
	}
	if resp.GetMessage() != "unknown method type" {
		t.Errorf("message = %q, want %q", resp.GetMessage(), "unknown method type")
	}
	if len(resp.GetDisabled()) != 0 {
		t.Errorf("disabled = %v, want empty after no-op", resp.GetDisabled())
	}
	// Pipeline still routes normally.
	if got := p.Evaluate(ctx, &engine.RouteInput{Source: "a", IsNew: true}); got != "from-expr" {
		t.Errorf("after unknown disable: got %q, want from-expr", got)
	}
}

func TestAdmin_EnableNotDisabled_NoOp(t *testing.T) {
	p := newTwoMethodPipeline(t)
	client := startAdmin(t, p)
	ctx := context.Background()

	resp, err := client.EnableMethod(ctx, &pb.EnableMethodRequest{Type: "expr"})
	if err != nil {
		t.Fatalf("EnableMethod RPC: %v", err)
	}
	if resp.GetMessage() != "not disabled" {
		t.Errorf("message = %q, want %q", resp.GetMessage(), "not disabled")
	}
}

func TestAdmin_DisableTwice_AlreadyDisabled(t *testing.T) {
	p := newTwoMethodPipeline(t)
	client := startAdmin(t, p)
	ctx := context.Background()

	if _, err := client.DisableMethod(ctx, &pb.DisableMethodRequest{Type: "expr"}); err != nil {
		t.Fatalf("first DisableMethod: %v", err)
	}
	resp, err := client.DisableMethod(ctx, &pb.DisableMethodRequest{Type: "expr"})
	if err != nil {
		t.Fatalf("second DisableMethod: %v", err)
	}
	if resp.GetMessage() != "already disabled" {
		t.Errorf("message = %q, want %q", resp.GetMessage(), "already disabled")
	}
	if n := len(resp.GetDisabled()); n != 1 {
		t.Errorf("disabled count = %d, want 1 (no duplicate)", n)
	}
}

// TestAdmin_StartAndStop exercises the addr-binding Start path and the public
// Stop alias (graceful shutdown) end to end.
func TestAdmin_StartAndStop(t *testing.T) {
	p := newTwoMethodPipeline(t)
	a := server.NewAdmin(p, newTestLogger(t))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		// :0 lets the OS pick a free port; Start binds and serves until ctx done.
		if err := a.Start(ctx, "127.0.0.1:0"); err != nil {
			t.Errorf("admin.Start: %v", err)
		}
	}()

	// Cancel and confirm Start returns; then Stop() is a safe idempotent no-op.
	cancel()
	<-done
	a.Stop()
}

// TestAdmin_StartBindError surfaces a listen failure as an error from Start.
func TestAdmin_StartBindError(t *testing.T) {
	p := newTwoMethodPipeline(t)
	a := server.NewAdmin(p, newTestLogger(t))
	// An obviously invalid address fails net.Listen.
	if err := a.Start(context.Background(), "bad:addr:99999"); err == nil {
		t.Fatal("Start with invalid addr = nil error, want bind error")
	}
}

// --- concurrency / race ---

func TestAdmin_ConcurrentDisableEnable_Safe(t *testing.T) {
	p := newTwoMethodPipeline(t)
	client := startAdmin(t, p)
	ctx := context.Background()
	in := &engine.RouteInput{Source: "a", IsNew: true}

	var wg sync.WaitGroup
	// Toggle expr from several goroutines while others evaluate the pipeline.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if (i+j)%2 == 0 {
					_, _ = client.DisableMethod(ctx, &pb.DisableMethodRequest{Type: "expr"})
				} else {
					_, _ = client.EnableMethod(ctx, &pb.EnableMethodRequest{Type: "expr"})
				}
			}
		}(i)
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				// Either from-expr (enabled) or from-second (expr disabled) — never
				// empty or a torn value.
				got := p.Evaluate(ctx, in)
				if got != "from-expr" && got != "from-second" {
					t.Errorf("torn/unexpected group during concurrent toggle: %q", got)
					return
				}
			}
		}()
	}
	wg.Wait()

	// Drive to a known terminal state and verify it sticks.
	if _, err := client.EnableMethod(ctx, &pb.EnableMethodRequest{Type: "expr"}); err != nil {
		t.Fatalf("final EnableMethod: %v", err)
	}
	lst, err := client.ListDisabled(ctx, &pb.ListDisabledRequest{})
	if err != nil {
		t.Fatalf("ListDisabled: %v", err)
	}
	if contains(lst.GetDisabled(), "expr") {
		t.Errorf("expr still disabled after final enable: %v", lst.GetDisabled())
	}
	if got := p.Evaluate(ctx, in); got != "from-expr" {
		t.Errorf("final route = %q, want from-expr", got)
	}
}

// contains mirrors the package-internal helper for test assertions.
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
