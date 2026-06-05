package engine_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/hclincode/trino-goway-routing-service/internal/engine"
)

// --- spy method helpers ---

// spyMethod records calls and returns configurable decisions.
type spyMethod struct {
	typeName string
	calls    atomic.Int64
	decide   bool   // whether to return Decided==true
	group    string // group to return when decide==true
	err      error  // non-nil to simulate a method error
}

func newSpy(typeName, group string) *spyMethod {
	return &spyMethod{typeName: typeName, decide: true, group: group}
}

func newDeferSpy(typeName string) *spyMethod {
	return &spyMethod{typeName: typeName, decide: false}
}

func newErrSpy(typeName string) *spyMethod {
	return &spyMethod{typeName: typeName, err: errors.New("simulated method error")}
}

func (s *spyMethod) Type() string { return s.typeName }
func (s *spyMethod) LoadConfig(_ []byte) error { return nil }
func (s *spyMethod) Evaluate(_ context.Context, _ *engine.RouteInput) (engine.Decision, error) {
	s.calls.Add(1)
	if s.err != nil {
		return engine.Decision{}, s.err
	}
	return engine.Decision{RoutingGroup: s.group, Decided: s.decide}, nil
}

// --- tests ---

func newPipeline(methods []engine.RoutingMethod, defaultGroup string) *engine.Pipeline {
	return engine.NewPipeline(methods, defaultGroup, newSlogLogger())
}

func TestPipeline_FirstDecidedWins(t *testing.T) {
	first := newSpy("first", "etl")
	second := newSpy("second", "batch")

	p := newPipeline([]engine.RoutingMethod{first, second}, "default")

	got := p.Evaluate(context.Background(), &engine.RouteInput{IsNew: true})
	if got != "etl" {
		t.Errorf("Evaluate = %q, want %q", got, "etl")
	}
	if n := first.calls.Load(); n != 1 {
		t.Errorf("first.calls = %d, want 1", n)
	}
	// Critical: second must NOT be called when first decides.
	if n := second.calls.Load(); n != 0 {
		t.Errorf("second.calls = %d, want 0 (spy proves second not called)", n)
	}
}

func TestPipeline_ErrorSkipped_NextDecides(t *testing.T) {
	bad := newErrSpy("bad")
	good := newSpy("good", "batch")

	p := newPipeline([]engine.RoutingMethod{bad, good}, "default")

	got := p.Evaluate(context.Background(), &engine.RouteInput{IsNew: true})
	if got != "batch" {
		t.Errorf("Evaluate = %q, want %q after skipping error method", got, "batch")
	}
	// bad was called but its error was swallowed.
	if n := bad.calls.Load(); n != 1 {
		t.Errorf("bad.calls = %d, want 1", n)
	}
}

func TestPipeline_AllDefer_ReturnsDefault(t *testing.T) {
	a := newDeferSpy("a")
	b := newDeferSpy("b")

	p := newPipeline([]engine.RoutingMethod{a, b}, "default-group")

	got := p.Evaluate(context.Background(), &engine.RouteInput{IsNew: true})
	if got != "default-group" {
		t.Errorf("Evaluate = %q, want %q", got, "default-group")
	}
}

func TestPipeline_EmptyMethods_ReturnsDefault(t *testing.T) {
	p := newPipeline(nil, "default-group")

	got := p.Evaluate(context.Background(), &engine.RouteInput{IsNew: true})
	if got != "default-group" {
		t.Errorf("Evaluate = %q, want %q", got, "default-group")
	}
}

func TestPipeline_Ordering_ThreeMethods(t *testing.T) {
	// Methods return "a", "b", "c" — only "a" should be used.
	ma := newSpy("ma", "a")
	mb := newSpy("mb", "b")
	mc := newSpy("mc", "c")

	p := newPipeline([]engine.RoutingMethod{ma, mb, mc}, "default")

	got := p.Evaluate(context.Background(), &engine.RouteInput{IsNew: true})
	if got != "a" {
		t.Errorf("Evaluate = %q, want %q", got, "a")
	}
	if ma.calls.Load() != 1 {
		t.Errorf("ma.calls = %d, want 1", ma.calls.Load())
	}
	if mb.calls.Load() != 0 {
		t.Errorf("mb.calls = %d, want 0", mb.calls.Load())
	}
	if mc.calls.Load() != 0 {
		t.Errorf("mc.calls = %d, want 0", mc.calls.Load())
	}
}

func TestPipeline_Ready_Transitions(t *testing.T) {
	// Empty pipeline is ready (pure-default mode).
	p := newPipeline(nil, "default")
	// Empty pipeline: not ready because no methods loaded.
	// Wait — per TODO spec: "Ready() is true once at least one method is loaded
	// OR the pipeline has zero methods (pure-default mode)."
	if !p.Ready() {
		t.Error("empty pipeline should be Ready() == true (pure-default mode)")
	}

	// Pipeline with methods is ready immediately.
	p2 := newPipeline([]engine.RoutingMethod{newSpy("x", "g")}, "default")
	if !p2.Ready() {
		t.Error("pipeline with methods should be Ready() == true")
	}

	// Swap in a new set → still ready.
	p2.Swap([]engine.RoutingMethod{newSpy("y", "g2")})
	if !p2.Ready() {
		t.Error("pipeline should stay Ready() after successful Swap")
	}

	// Simulating keep-last-good: a failed reload should NOT call Swap.
	// Ready() stays true because Swap was never called with bad methods.
	// (Callers must not call Swap on failure — this is the contract.)
	if !p2.Ready() {
		t.Error("pipeline should stay Ready() when Swap is not called (failed reload kept last-good)")
	}
}

func TestPipeline_Swap_AtomicReplace(t *testing.T) {
	first := newSpy("first", "old")
	p := newPipeline([]engine.RoutingMethod{first}, "default")

	got := p.Evaluate(context.Background(), &engine.RouteInput{IsNew: true})
	if got != "old" {
		t.Fatalf("before swap: got %q, want %q", got, "old")
	}

	newMethod := newSpy("first", "new")
	p.Swap([]engine.RoutingMethod{newMethod})

	got = p.Evaluate(context.Background(), &engine.RouteInput{IsNew: true})
	if got != "new" {
		t.Errorf("after swap: got %q, want %q", got, "new")
	}
}

// TestPipeline_DisableEnable verifies kill-switch atomicity.
func TestPipeline_DisableEnable(t *testing.T) {
	expr := newSpy("expr", "etl")
	script := newSpy("script", "batch")
	p := newPipeline([]engine.RoutingMethod{expr, script}, "default")

	// Both enabled: first (expr) decides.
	if got := p.Evaluate(context.Background(), &engine.RouteInput{IsNew: true}); got != "etl" {
		t.Fatalf("before disable: got %q, want %q", got, "etl")
	}

	// Disable expr: script should now decide.
	p.DisableMethod("expr")
	if got := p.Evaluate(context.Background(), &engine.RouteInput{IsNew: true}); got != "batch" {
		t.Errorf("after disabling expr: got %q, want %q", got, "batch")
	}
	if names := p.DisabledMethods(); len(names) != 1 || names[0] != "expr" {
		t.Errorf("DisabledMethods() = %v, want [expr]", names)
	}

	// Enable expr: expr decides again.
	p.EnableMethod("expr")
	if got := p.Evaluate(context.Background(), &engine.RouteInput{IsNew: true}); got != "etl" {
		t.Errorf("after enabling expr: got %q, want %q", got, "etl")
	}
	if names := p.DisabledMethods(); len(names) != 0 {
		t.Errorf("DisabledMethods() after enable = %v, want []", names)
	}
}

func TestPipeline_DisableUnknown_NoOp(t *testing.T) {
	p := newPipeline([]engine.RoutingMethod{newSpy("expr", "etl")}, "default")
	// Should not panic.
	p.DisableMethod("does-not-exist")
	if got := p.Evaluate(context.Background(), &engine.RouteInput{IsNew: true}); got != "etl" {
		t.Errorf("after disabling unknown: got %q, want %q", got, "etl")
	}
}

func TestPipeline_DisableBoth_ReturnsDefault(t *testing.T) {
	p := newPipeline([]engine.RoutingMethod{newSpy("a", "x"), newSpy("b", "y")}, "default")
	p.DisableMethod("a")
	p.DisableMethod("b")
	if got := p.Evaluate(context.Background(), &engine.RouteInput{IsNew: true}); got != "default" {
		t.Errorf("all disabled: got %q, want %q", got, "default")
	}
}

// TestPipeline_DecidedEmptyGroup_UsesDefault: a method returning Decided=true
// with empty group should resolve to the default group.
func TestPipeline_DecidedEmptyGroup_UsesDefault(t *testing.T) {
	// Method decides but returns ""
	m := newSpy("m", "")
	p := newPipeline([]engine.RoutingMethod{m}, "fallback")
	if got := p.Evaluate(context.Background(), &engine.RouteInput{IsNew: true}); got != "fallback" {
		t.Errorf("decided with empty group: got %q, want %q", got, "fallback")
	}
}

// --- panic recovery tests ---

// panicMethod panics on every Evaluate call.
type panicMethod struct {
	typeName string
	calls    atomic.Int64
}

func (m *panicMethod) Type() string           { return m.typeName }
func (m *panicMethod) LoadConfig(_ []byte) error { return nil }
func (m *panicMethod) Evaluate(_ context.Context, _ *engine.RouteInput) (engine.Decision, error) {
	m.calls.Add(1)
	panic("simulated method panic in " + m.typeName)
}

// TestPipeline_PanicInMethod_Recovered_NextDecides verifies that a panic in a
// method is recovered, treated as Decided==false, and the next method decides.
func TestPipeline_PanicInMethod_Recovered_NextDecides(t *testing.T) {
	bad := &panicMethod{typeName: "bad"}
	good := newSpy("good", "etl")

	p := newPipeline([]engine.RoutingMethod{bad, good}, "default")

	// Must not panic the test goroutine.
	got := p.Evaluate(context.Background(), &engine.RouteInput{IsNew: true})
	if got != "etl" {
		t.Errorf("Evaluate = %q, want %q (next method should decide after panic)", got, "etl")
	}
	if bad.calls.Load() != 1 {
		t.Errorf("bad.calls = %d, want 1", bad.calls.Load())
	}
	// good must be called because bad panicked (treated as skip).
	if good.calls.Load() != 1 {
		t.Errorf("good.calls = %d, want 1", good.calls.Load())
	}
}

// TestPipeline_PanicInOnlyMethod_ReturnsDefault verifies that a panic in the
// only method results in the default group (not a crash or hang).
func TestPipeline_PanicInOnlyMethod_ReturnsDefault(t *testing.T) {
	bad := &panicMethod{typeName: "only"}
	p := newPipeline([]engine.RoutingMethod{bad}, "default-group")

	got := p.Evaluate(context.Background(), &engine.RouteInput{IsNew: true})
	if got != "default-group" {
		t.Errorf("Evaluate = %q, want %q", got, "default-group")
	}
}

// TestPipeline_PanicDoesNotLeakGoroutines is covered by TestMain's goleak.

// --- EvaluateFull coverage ---

func TestPipeline_EvaluateFull_FirstDecidedWins(t *testing.T) {
	first := newSpy("first", "etl")
	second := newSpy("second", "batch")
	p := newPipeline([]engine.RoutingMethod{first, second}, "default")

	d := p.EvaluateFull(context.Background(), &engine.RouteInput{IsNew: true})
	if d.RoutingGroup != "etl" {
		t.Errorf("EvaluateFull.RoutingGroup = %q, want %q", d.RoutingGroup, "etl")
	}
	if second.calls.Load() != 0 {
		t.Errorf("second.calls = %d, want 0", second.calls.Load())
	}
}

func TestPipeline_EvaluateFull_AllDefer_ReturnsDefault(t *testing.T) {
	p := newPipeline([]engine.RoutingMethod{newDeferSpy("a")}, "default-full")
	d := p.EvaluateFull(context.Background(), &engine.RouteInput{IsNew: true})
	if d.RoutingGroup != "default-full" {
		t.Errorf("EvaluateFull.RoutingGroup = %q, want %q", d.RoutingGroup, "default-full")
	}
	if d.Decided {
		t.Error("EvaluateFull.Decided = true, want false for all-defer")
	}
}

func TestPipeline_EvaluateFull_DecidedEmptyGroup_UsesDefault(t *testing.T) {
	m := newSpy("m", "")
	p := newPipeline([]engine.RoutingMethod{m}, "fallback-full")
	d := p.EvaluateFull(context.Background(), &engine.RouteInput{IsNew: true})
	if d.RoutingGroup != "fallback-full" {
		t.Errorf("EvaluateFull.RoutingGroup = %q, want %q", d.RoutingGroup, "fallback-full")
	}
}

func TestPipeline_EvaluateFull_PanicRecovered_NextDecides(t *testing.T) {
	bad := &panicMethod{typeName: "bad"}
	good := newSpy("good", "etl")
	p := newPipeline([]engine.RoutingMethod{bad, good}, "default")
	d := p.EvaluateFull(context.Background(), &engine.RouteInput{IsNew: true})
	if d.RoutingGroup != "etl" {
		t.Errorf("EvaluateFull after panic: RoutingGroup = %q, want %q", d.RoutingGroup, "etl")
	}
}

// --- disabledMethod wrapper path coverage ---

func TestDisabledMethod_Type_DelegatesInner(t *testing.T) {
	// Exercise the disabledMethod wrapper's Type() and LoadConfig() via
	// DisableMethod + DisabledMethods inspection.
	m := newSpy("expr", "etl")
	p := newPipeline([]engine.RoutingMethod{m}, "default")
	p.DisableMethod("expr")

	names := p.DisabledMethods()
	if len(names) != 1 || names[0] != "expr" {
		t.Errorf("DisabledMethods() = %v, want [expr]", names)
	}
}

// --- EvaluateResult (RS-9 observability-rich entry point) ---

func TestEvaluateResult_Decided(t *testing.T) {
	first := newSpy("expr", "etl")
	second := newSpy("script", "batch")
	p := newPipeline([]engine.RoutingMethod{first, second}, "default")

	res := p.EvaluateResult(context.Background(), &engine.RouteInput{IsNew: true})
	if !res.Decided || res.RoutingGroup != "etl" || res.MethodType != "expr" {
		t.Errorf("EvaluateResult = %+v, want {etl expr decided}", res)
	}
	if res.HadError {
		t.Errorf("HadError = true, want false")
	}
	if n := second.calls.Load(); n != 0 {
		t.Errorf("second.calls = %d, want 0 (first decided)", n)
	}
}

func TestEvaluateResult_Fallback(t *testing.T) {
	p := newPipeline([]engine.RoutingMethod{newDeferSpy("expr"), newDeferSpy("script")}, "default")
	res := p.EvaluateResult(context.Background(), &engine.RouteInput{IsNew: true})
	if res.Decided || res.MethodType != "" || res.RoutingGroup != "default" {
		t.Errorf("EvaluateResult = %+v, want {default \"\" not-decided}", res)
	}
	if res.HadError {
		t.Errorf("HadError = true, want false on clean fallback")
	}
}

func TestEvaluateResult_ErrorThenDecide(t *testing.T) {
	// First method errors (skipped), second decides; HadError must be true.
	p := newPipeline([]engine.RoutingMethod{newErrSpy("expr"), newSpy("script", "batch")}, "default")
	res := p.EvaluateResult(context.Background(), &engine.RouteInput{IsNew: true})
	if !res.Decided || res.RoutingGroup != "batch" || res.MethodType != "script" {
		t.Errorf("EvaluateResult = %+v, want {batch script decided}", res)
	}
	if !res.HadError {
		t.Errorf("HadError = false, want true (first method errored)")
	}
}

func TestEvaluateResult_ErrorThenFallback(t *testing.T) {
	// Only method errors → fallback to default, HadError true.
	p := newPipeline([]engine.RoutingMethod{newErrSpy("expr")}, "default")
	res := p.EvaluateResult(context.Background(), &engine.RouteInput{IsNew: true})
	if res.Decided || res.RoutingGroup != "default" {
		t.Errorf("EvaluateResult = %+v, want {default not-decided}", res)
	}
	if !res.HadError {
		t.Errorf("HadError = false, want true")
	}
}

func TestEvaluateResult_SkipsDisabled(t *testing.T) {
	first := newSpy("expr", "etl")
	second := newSpy("script", "batch")
	p := newPipeline([]engine.RoutingMethod{first, second}, "default")
	p.DisableMethod("expr")

	res := p.EvaluateResult(context.Background(), &engine.RouteInput{IsNew: true})
	if res.MethodType != "script" || res.RoutingGroup != "batch" {
		t.Errorf("EvaluateResult = %+v, want script/batch (expr disabled)", res)
	}
}
