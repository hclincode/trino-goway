package expr_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/hclincode/trino-goway-routing-service/internal/engine"
	exprovider "github.com/hclincode/trino-goway-routing-service/internal/engine/providers/expr"
)

// prdWorkedExampleProgram is the exact PRD §6.2 expr program, using the documented
// lowercase/snake_case field names: request.source, request.user, request.client_tags.
const prdWorkedExampleProgram = `request.source == "airflow" ? "etl"
  : request.source == "superset" ? (hashPct(request.user) < 5 ? "interactive-canary" : "interactive")
  : "tier=premium" in request.client_tags ? "premium"
  : hasSuffix(request.user, "@analytics.acme.com") ? "etl-" + split(split(request.user, "@")[1], ".")[0]
  : ""`

// BenchmarkExprEvaluate runs the full PRD §6.2 program and reports p99 latency.
// This is the standard Go benchmark; use `go test -bench=. -benchtime=5s` to run it.
func BenchmarkExprEvaluate(b *testing.B) {
	p := exprovider.New()
	if err := p.LoadConfig(makeConfig(prdWorkedExampleProgram)); err != nil {
		b.Fatalf("LoadConfig: %v", err)
	}

	in := &engine.RouteInput{
		Source:     "superset",
		User:       "alice@example.com",
		ClientTags: []string{"tier=standard"},
		IsNew:      true,
	}
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = p.Evaluate(ctx, in)
	}
}

// TestExprEvaluate_P99Under50us runs a time-bounded latency histogram and
// asserts the 99th-percentile is below 50 µs. This catches performance
// regressions without requiring `go test -bench`.
func TestExprEvaluate_P99Under50us(t *testing.T) {
	p := exprovider.New()
	if err := p.LoadConfig(makeConfig(prdWorkedExampleProgram)); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	in := &engine.RouteInput{
		Source:     "superset",
		User:       "alice@example.com",
		ClientTags: []string{"tier=standard"},
		IsNew:      true,
	}
	ctx := context.Background()

	const n = 10_000
	timings := make([]time.Duration, n)
	for i := range n {
		start := time.Now()
		_, _ = p.Evaluate(ctx, in)
		timings[i] = time.Since(start)
	}

	sort.Slice(timings, func(i, j int) bool { return timings[i] < timings[j] })
	p99 := timings[int(float64(n)*0.99)]

	const budget = 50 * time.Microsecond
	if p99 > budget {
		t.Errorf("p99 latency = %v, want < %v", p99, budget)
	}
	t.Logf("expr p99 latency: %v (budget: %v)", p99, budget)
}
