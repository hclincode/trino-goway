package engine

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync/atomic"
)

// Pipeline evaluates an ordered chain of RoutingMethods for each request.
// The first method that returns Decided==true wins; if none decide, the
// configured defaultGroup is returned. Method errors are swallowed (logged +
// skip) and never surfaced as gRPC errors — fail-safe by design.
//
// Pipeline satisfies the server.Evaluator interface.
type Pipeline struct {
	// methods is an atomic pointer to the current []RoutingMethod slice.
	// Hot-reload swaps it atomically; no lock needed for reads.
	methods      atomic.Pointer[[]RoutingMethod]
	defaultGroup string
	log          *slog.Logger
	// ready is 1 once at least one successful LoadConfig has occurred.
	ready atomic.Int32
}

// NewPipeline constructs a Pipeline with the given methods and default group.
// methods may be nil or empty (pure-default mode: every request returns defaultGroup).
func NewPipeline(methods []RoutingMethod, defaultGroup string, log *slog.Logger) *Pipeline {
	p := &Pipeline{
		defaultGroup: defaultGroup,
		log:          log,
	}
	p.swap(methods)
	// Ready immediately: either there are real methods loaded, or this is
	// pure-default mode (zero methods) which is a valid operational state.
	p.ready.Store(1)
	return p
}

// Evaluate runs the pipeline and returns the chosen routing group (or the
// default group if no method decides). Satisfies server.Evaluator.
func (p *Pipeline) Evaluate(ctx context.Context, in *RouteInput) string {
	ms := p.load()
	for _, m := range ms {
		if isDisabled(p, m) {
			continue
		}
		d, err := safeEvaluate(ctx, m, in, p.log)
		if err != nil {
			p.log.Warn("engine: pipeline: method error, skipping",
				"type", m.Type(), "err", err)
			continue
		}
		if d.Decided {
			if d.RoutingGroup == "" {
				return p.defaultGroup
			}
			return d.RoutingGroup
		}
	}
	return p.defaultGroup
}

// EvaluateFull runs the pipeline and returns the full Decision (including
// ExternalHeaders and Errors). Used by server.Route when those fields matter.
// For RS-2/RS-3 the server uses the simpler Evaluate; this is available for
// RS-4+ providers that return headers.
func (p *Pipeline) EvaluateFull(ctx context.Context, in *RouteInput) Decision {
	ms := p.load()
	for _, m := range ms {
		if isDisabled(p, m) {
			continue
		}
		d, err := safeEvaluate(ctx, m, in, p.log)
		if err != nil {
			p.log.Warn("engine: pipeline: method error, skipping",
				"type", m.Type(), "err", err)
			continue
		}
		if d.Decided {
			if d.RoutingGroup == "" {
				d.RoutingGroup = p.defaultGroup
			}
			return d
		}
	}
	return Decision{RoutingGroup: p.defaultGroup, Decided: false}
}

// Ready reports whether the pipeline has a valid loaded method set.
// Returns true if at least one successful method load has occurred,
// or if the pipeline was constructed with zero methods (pure-default mode).
// Satisfies server.Evaluator.
func (p *Pipeline) Ready() bool {
	return p.ready.Load() == 1
}

// Swap atomically replaces the method slice and marks the pipeline ready.
// Called by the hot-reload watcher after all methods have been successfully
// loaded. Never call this with a partially-valid set.
func (p *Pipeline) Swap(methods []RoutingMethod) {
	p.swap(methods)
	p.ready.Store(1)
}

// swap is the internal atomic store. Allocates a new slice header on the heap
// so the pointer is stable.
func (p *Pipeline) swap(methods []RoutingMethod) {
	ms := make([]RoutingMethod, len(methods))
	copy(ms, methods)
	p.methods.Store(&ms)
}

// load returns the current method slice. Never returns nil.
func (p *Pipeline) load() []RoutingMethod {
	if ptr := p.methods.Load(); ptr != nil {
		return *ptr
	}
	return nil
}

// disabled is a sentinel that wraps a method to mark it as disabled.
// DisableMethod replaces the live entry with a disabledMethod wrapper;
// EnableMethod unwraps it.
type disabledMethod struct{ inner RoutingMethod }

func (d *disabledMethod) Type() string                                          { return d.inner.Type() }
func (d *disabledMethod) LoadConfig(raw []byte) error                           { return d.inner.LoadConfig(raw) }
func (d *disabledMethod) Evaluate(_ context.Context, _ *RouteInput) (Decision, error) {
	return Decision{}, nil
}

// isDisabled reports whether m is a disabledMethod wrapper.
func isDisabled(_ *Pipeline, m RoutingMethod) bool {
	_, ok := m.(*disabledMethod)
	return ok
}

// DisableMethod atomically disables the first method with the matching type.
// Takes effect on the next Evaluate call. No-op if the type is not found.
func (p *Pipeline) DisableMethod(typeName string) {
	p.updateMethod(typeName, func(m RoutingMethod) RoutingMethod {
		if _, already := m.(*disabledMethod); already {
			return m // already disabled
		}
		return &disabledMethod{inner: m}
	})
}

// EnableMethod re-enables a previously disabled method. No-op if not found or
// not currently disabled.
func (p *Pipeline) EnableMethod(typeName string) {
	p.updateMethod(typeName, func(m RoutingMethod) RoutingMethod {
		if d, ok := m.(*disabledMethod); ok {
			return d.inner
		}
		return m // already enabled
	})
}

// DisabledMethods returns the type names of all currently disabled methods.
func (p *Pipeline) DisabledMethods() []string {
	ms := p.load()
	var out []string
	for _, m := range ms {
		if _, ok := m.(*disabledMethod); ok {
			out = append(out, m.Type())
		}
	}
	return out
}

// updateMethod applies fn to the first method whose Type() matches typeName,
// then atomically swaps the slice. fn must return a non-nil replacement.
func (p *Pipeline) updateMethod(typeName string, fn func(RoutingMethod) RoutingMethod) {
	for {
		old := p.methods.Load()
		if old == nil || len(*old) == 0 {
			return
		}
		ms := make([]RoutingMethod, len(*old))
		copy(ms, *old)
		found := false
		for i, m := range ms {
			if m.Type() == typeName {
				ms[i] = fn(m)
				found = true
				break
			}
		}
		if !found {
			return
		}
		// CAS: only apply if the slice hasn't changed under us.
		if p.methods.CompareAndSwap(old, &ms) {
			return
		}
		// Another goroutine changed the slice; retry.
	}
}

// safeEvaluate calls m.Evaluate and recovers from any panic.
// A panic is converted to an error with Decided==false so the pipeline
// can skip to the next method rather than crashing — matches PRD:
// "runtime panic → Decided:false, no crash".
func safeEvaluate(ctx context.Context, m RoutingMethod, in *RouteInput, log *slog.Logger) (d Decision, err error) {
	defer func() {
		if r := recover(); r != nil {
			stack := debug.Stack()
			log.Error("engine: pipeline: panic in method — recovered",
				"type", m.Type(),
				"panic", r,
				"stack", string(stack),
			)
			d = Decision{}
			err = fmt.Errorf("panic in method %q: %v", m.Type(), r)
		}
	}()
	return m.Evaluate(ctx, in)
}
