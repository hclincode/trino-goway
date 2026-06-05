package metrics

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

// RouterMetrics records routing-path metrics. It structurally satisfies the
// routing package's RouterMetrics interface and is injected into the router by
// the composition root.
type RouterMetrics struct {
	calls         *prometheus.CounterVec   // {transport,outcome}
	callDuration  *prometheus.HistogramVec // {transport}
	cacheEvents   *prometheus.CounterVec   // {event}
	recoverySteps *prometheus.CounterVec   // {step}
	killQuery     prometheus.Counter
}

// NewRouterMetrics registers the routing metric families on the registry.
func NewRouterMetrics(reg prometheus.Registerer) (*RouterMetrics, error) {
	m := &RouterMetrics{
		calls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "router",
			Name:      "calls_total",
			Help:      "External routing calls, by transport (http|grpc) and outcome (ok|error|timeout|fallback).",
		}, []string{"transport", "outcome"}),
		callDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace,
			Subsystem: "router",
			Name:      "call_duration_seconds",
			Help:      "External routing call duration in seconds, by transport.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"transport"}),
		cacheEvents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "routing",
			Name:      "cache_events_total",
			Help:      "Sticky-routing cache events, by event (hit|miss).",
		}, []string{"event"}),
		recoverySteps: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "recovery_chain",
			Name:      "steps_total",
			Help:      "Recovery-chain steps that resolved a backend, by step (history|probe|default).",
		}, []string{"step"}),
		killQuery: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "kill_query_routes_total",
			Help:      "Requests routed by the KILL QUERY regex to the query's owning backend.",
		}),
	}
	collectors := []prometheus.Collector{m.calls, m.callDuration, m.cacheEvents, m.recoverySteps, m.killQuery}
	for _, c := range collectors {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("metrics: register router metrics: %w", err)
		}
	}
	return m, nil
}

// RouterCall records one external routing call.
func (m *RouterMetrics) RouterCall(transport, outcome string, seconds float64) {
	m.calls.WithLabelValues(transport, outcome).Inc()
	m.callDuration.WithLabelValues(transport).Observe(seconds)
}

// CacheEvent records a sticky-routing cache lookup.
func (m *RouterMetrics) CacheEvent(event string) {
	m.cacheEvents.WithLabelValues(event).Inc()
}

// RecoveryStep records a recovery-chain step taken.
func (m *RouterMetrics) RecoveryStep(step string) {
	m.recoverySteps.WithLabelValues(step).Inc()
}

// KillQueryRoute records a KILL QUERY regex-routed request.
func (m *RouterMetrics) KillQueryRoute() {
	m.killQuery.Inc()
}
