// Package metrics owns the routing-service Prometheus collectors and the
// /metrics HTTP endpoint. It uses its OWN *prometheus.Registry — never the
// global default — so the service has no hidden global state and tests get a
// clean registry per instance.
package metrics

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Outcome classifies a single Route evaluation for the requests counter.
type Outcome string

const (
	// OutcomeDecided: a method returned a definitive routing group.
	OutcomeDecided Outcome = "decided"
	// OutcomeFallback: every method deferred; the default group was used.
	OutcomeFallback Outcome = "fallback"
	// OutcomeError: a method errored (the pipeline skipped it). Recorded once
	// per request that hit at least one method error; not double-counted as
	// fallback.
	OutcomeError Outcome = "error"
)

// ReloadResult labels the config-reload counter.
type ReloadResult string

const (
	ReloadOK    ReloadResult = "ok"
	ReloadError ReloadResult = "error"
)

// Metrics holds the collectors and their registry. Construct with New; pass it
// explicitly to the server and reload watcher (no globals).
type Metrics struct {
	reg *prometheus.Registry

	requests       *prometheus.CounterVec   // by source, routing_group, method_type, outcome
	fallback       prometheus.Counter       // total fallbacks (alertable rate)
	decisionDur    *prometheus.HistogramVec // by method_type
	reloadTotal    *prometheus.CounterVec   // by result
	configVersion  *prometheus.GaugeVec     // by hash; value always 1 for the active hash
	methodDisabled *prometheus.GaugeVec     // by type; 1 disabled, 0 enabled
}

// New builds the collectors on a fresh registry and registers them.
func New() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		reg: reg,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "routing_service_requests_total",
			Help: "Total Route evaluations by source, routing group, deciding method type, and outcome.",
		}, []string{"source", "routing_group", "method_type", "outcome"}),
		fallback: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "routing_service_fallback_total",
			Help: "Total requests that fell back to the default group (no method decided).",
		}),
		decisionDur: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "routing_service_decision_duration_seconds",
			Help: "Routing decision latency in seconds, by deciding method type.",
			// Buckets tuned for an in-memory eval target of p99 ≤ 1ms.
			Buckets: []float64{
				0.000_01, 0.000_025, 0.000_05, 0.000_1, 0.000_25,
				0.000_5, 0.001, 0.0025, 0.005, 0.01, 0.05,
			},
		}, []string{"method_type"}),
		reloadTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "routing_service_config_reload_total",
			Help: "Config hot-reload attempts by result (ok|error).",
		}, []string{"result"}),
		configVersion: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "routing_service_config_version",
			Help: "Active config content hash; the labelled series is set to 1.",
		}, []string{"hash"}),
		methodDisabled: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "routing_service_method_disabled",
			Help: "1 if a routing method is currently disabled (kill-switch), else 0.",
		}, []string{"type"}),
	}
	reg.MustRegister(
		m.requests, m.fallback, m.decisionDur,
		m.reloadTotal, m.configVersion, m.methodDisabled,
	)
	return m
}

// Registry exposes the underlying registry (for tests / custom gatherers).
func (m *Metrics) Registry() *prometheus.Registry { return m.reg }

// RecordDecision records one Route evaluation. methodType is the deciding
// method's type, or "" when no method decided (fallback). latency is the
// decision wall time.
func (m *Metrics) RecordDecision(source, routingGroup, methodType string, outcome Outcome, latency time.Duration) {
	m.requests.WithLabelValues(source, routingGroup, methodType, string(outcome)).Inc()
	if outcome == OutcomeFallback {
		m.fallback.Inc()
	}
	// Latency is bucketed by method type; use "default" when none decided so the
	// fallback path is still observable.
	mt := methodType
	if mt == "" {
		mt = "default"
	}
	m.decisionDur.WithLabelValues(mt).Observe(latency.Seconds())
}

// RecordReload increments the reload counter for the given result.
func (m *Metrics) RecordReload(result ReloadResult) {
	m.reloadTotal.WithLabelValues(string(result)).Inc()
}

// SetConfigVersion marks hash as the active config version. It resets the gauge
// vector so only the current hash carries the value 1.
func (m *Metrics) SetConfigVersion(hash string) {
	m.configVersion.Reset()
	m.configVersion.WithLabelValues(hash).Set(1)
}

// SetMethodDisabled sets the disabled gauge for a method type (true → 1).
func (m *Metrics) SetMethodDisabled(methodType string, disabled bool) {
	v := 0.0
	if disabled {
		v = 1
	}
	m.methodDisabled.WithLabelValues(methodType).Set(v)
}

// SyncDisabled reconciles the method_disabled gauge from the authoritative set
// of currently-disabled method types. Pass every known method type in allTypes
// so re-enabled methods are reset to 0.
func (m *Metrics) SyncDisabled(allTypes, disabled []string) {
	dset := make(map[string]struct{}, len(disabled))
	for _, d := range disabled {
		dset[d] = struct{}{}
	}
	for _, t := range allTypes {
		_, off := dset[t]
		m.SetMethodDisabled(t, off)
	}
}

// Handler returns the /metrics HTTP handler bound to this registry, serving
// OpenMetrics text.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{EnableOpenMetrics: true})
}

// Server wraps an http.Server serving /metrics on a dedicated listener.
type Server struct {
	httpd *http.Server
}

// NewServer builds a metrics HTTP server exposing m.Handler() at /metrics.
func NewServer(m *Metrics) *Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", m.Handler())
	return &Server{httpd: &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}}
}

// Start serves /metrics on addr, blocking until ctx is cancelled, then shuts
// down gracefully. Returns nil on a clean ctx-driven shutdown.
func (s *Server) Start(ctx context.Context, addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.StartOnListener(ctx, lis)
}

// StartOnListener serves on an already-bound listener (tests use :0).
func (s *Server) StartOnListener(ctx context.Context, lis net.Listener) error {
	serveErr := make(chan error, 1)
	go func() {
		if err := s.httpd.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.httpd.Shutdown(shutCtx)
		return nil
	case err := <-serveErr:
		return err
	}
}
