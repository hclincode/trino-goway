package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Namespace is the common prefix for every gateway metric.
const Namespace = "trino_goway"

// Registry wraps a dedicated *prometheus.Registry. It is constructed explicitly
// and injected by the composition root; collectors register against r.Registerer().
type Registry struct {
	reg *prometheus.Registry
}

// New returns a Registry backed by a fresh, isolated *prometheus.Registry.
// It does not touch prometheus.DefaultRegisterer.
func New() *Registry {
	return &Registry{reg: prometheus.NewRegistry()}
}

// Registerer returns the prometheus.Registerer collectors register against.
func (r *Registry) Registerer() prometheus.Registerer {
	return r.reg
}

// Gatherer returns the prometheus.Gatherer used for exposition.
func (r *Registry) Gatherer() prometheus.Gatherer {
	return r.reg
}

// Handler returns the /metrics exposition handler with OpenMetrics enabled.
func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.reg, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})
}
