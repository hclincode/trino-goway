package script_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/hclincode/trino-goway-routing-service/internal/engine"
	scriptprovider "github.com/hclincode/trino-goway-routing-service/internal/engine/providers/script"
)

// BenchmarkStarlarkEvaluate runs the full PRD §6.2 Starlark route() function.
// Use: go test -bench=. -benchtime=5s
func BenchmarkStarlarkEvaluate(b *testing.B) {
	p := scriptprovider.New()
	if err := p.LoadConfig(makeConfig(`
def route(req):
  if req.source == "airflow":
    return "etl"
  if req.source == "superset":
    if hashPct(req.user) < 5:
      return "interactive-canary"
    return "interactive"
  if "tier=premium" in req.client_tags:
    return "premium"
  if req.user.endswith("@analytics.acme.com"):
    parts = req.user.split("@")
    domain_parts = parts[1].split(".")
    return "etl-" + domain_parts[0]
  return None
`)); err != nil {
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

// TestStarlarkEvaluate_P99Under1ms runs a time-bounded latency histogram
// and asserts the 99th-percentile stays below 1ms (PRD budget).
func TestStarlarkEvaluate_P99Under1ms(t *testing.T) {
	p := scriptprovider.New()
	if err := p.LoadConfig(makeConfig(`
def route(req):
  if req.source == "airflow":
    return "etl"
  if req.source == "superset":
    if hashPct(req.user) < 5:
      return "interactive-canary"
    return "interactive"
  if "tier=premium" in req.client_tags:
    return "premium"
  if req.user.endswith("@analytics.acme.com"):
    parts = req.user.split("@")
    domain_parts = parts[1].split(".")
    return "etl-" + domain_parts[0]
  return None
`)); err != nil {
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

	const budget = 1 * time.Millisecond
	if p99 > budget {
		t.Errorf("Starlark p99 latency = %v, want < %v", p99, budget)
	}
	t.Logf("Starlark p99 latency: %v (budget: %v)", p99, budget)
}
