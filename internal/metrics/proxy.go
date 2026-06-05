package metrics

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

// ProxyMetrics records proxy forwarding metrics. It structurally satisfies the
// proxy package's MetricsRecorder interface and is injected into the proxy by the
// composition root.
type ProxyMetrics struct {
	requests    *prometheus.CounterVec
	upstreamDur *prometheus.HistogramVec
	oversized   prometheus.Counter
	cacheWrites prometheus.Counter
}

// NewProxyMetrics registers the proxy metric families on the registry.
func NewProxyMetrics(reg prometheus.Registerer) (*ProxyMetrics, error) {
	m := &ProxyMetrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "proxy",
			Name:      "requests_total",
			Help:      "Total proxied requests, by selected backend, routing group, and outcome (ok|fallback|error|kill_query).",
		}, []string{"backend", "routing_group", "outcome"}),
		upstreamDur: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace,
			Subsystem: "proxy",
			Name:      "upstream_duration_seconds",
			Help:      "Upstream backend round-trip duration in seconds, by backend.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"backend"}),
		oversized: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "proxy",
			Name:      "oversized_responses_total",
			Help:      "POST /v1/statement responses that exceeded the buffer limit and failed loud with 502.",
		}),
		cacheWrites: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "proxy",
			Name:      "statement_cache_writes_total",
			Help:      "Sticky-routing cache writes performed before flushing the response (Hard Invariant #3).",
		}),
	}
	for _, c := range []prometheus.Collector{m.requests, m.upstreamDur, m.oversized, m.cacheWrites} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("metrics: register proxy metrics: %w", err)
		}
	}
	return m, nil
}

// RequestHandled records a completed proxy request with its routing outcome.
func (m *ProxyMetrics) RequestHandled(backend, routingGroup, outcome string) {
	m.requests.WithLabelValues(backend, routingGroup, outcome).Inc()
}

// UpstreamDuration records the upstream backend round-trip duration.
func (m *ProxyMetrics) UpstreamDuration(backend string, seconds float64) {
	m.upstreamDur.WithLabelValues(backend).Observe(seconds)
}

// OversizedResponse records an oversized /v1/statement response.
func (m *ProxyMetrics) OversizedResponse() {
	m.oversized.Inc()
}

// StatementCacheWrite records a sticky-routing cache write.
func (m *ProxyMetrics) StatementCacheWrite() {
	m.cacheWrites.Inc()
}
