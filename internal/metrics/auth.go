package metrics

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

// AuthMetrics records authentication metrics. It structurally satisfies the auth
// package's Metrics interface and is injected into the auth middleware by the
// composition root.
type AuthMetrics struct {
	requests    *prometheus.CounterVec // {type,result}
	jwksRefresh *prometheus.CounterVec // {result}
	jwksKeys    prometheus.Gauge
}

// NewAuthMetrics registers the auth metric families on the registry.
func NewAuthMetrics(reg prometheus.Registerer) (*AuthMetrics, error) {
	m := &AuthMetrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "auth",
			Name:      "requests_total",
			Help:      "Authentication decisions, by type (oidc|ldap|noop) and result (allow|deny).",
		}, []string{"type", "result"}),
		jwksRefresh: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "jwks_refresh_total",
			Help:      "JWKS refresh attempts, by result (success|error).",
		}, []string{"result"}),
		jwksKeys: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "jwks_keys",
			Help:      "Number of keys currently loaded from the JWKS endpoint.",
		}),
	}
	for _, c := range []prometheus.Collector{m.requests, m.jwksRefresh, m.jwksKeys} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("metrics: register auth metrics: %w", err)
		}
	}
	return m, nil
}

// AuthRequest records one authentication decision.
func (m *AuthMetrics) AuthRequest(authType, result string) {
	m.requests.WithLabelValues(authType, result).Inc()
}

// JWKSRefresh records a JWKS refresh attempt.
func (m *AuthMetrics) JWKSRefresh(result string) {
	m.jwksRefresh.WithLabelValues(result).Inc()
}

// JWKSKeys sets the number of keys currently loaded from the JWKS endpoint.
func (m *AuthMetrics) JWKSKeys(n int) {
	m.jwksKeys.Set(float64(n))
}
