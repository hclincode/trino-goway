package metrics

import (
	"fmt"
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/hclincode/trino-goway/internal/monitor"
)

// backendStatusLabels enumerates the status label values for the per-backend
// status gauge. One series per (backend, status) is maintained so a backend's
// health shows as 1 on exactly one status and 0 on the others, mirroring the
// Java {cluster}_TrinoStatus* gauges.
var backendStatusLabels = []string{"healthy", "unhealthy", "pending"}

// BackendMetrics tracks backend health and activation gauges. It implements the
// monitor.StatusObserver interface and is injected into the monitor by the
// composition root. Per-backend series are pruned when a backend leaves the set
// so stale series do not linger (mirrors the Java ClusterMetricsStatsExporter
// lifecycle).
type BackendMetrics struct {
	status     *prometheus.GaugeVec // {backend,status}
	activation *prometheus.GaugeVec // {backend}
	total      *prometheus.GaugeVec // {status} aggregate
	active     prometheus.Gauge     // count of active (known) backends

	mu   sync.Mutex
	seen map[string]struct{} // backend URLs with live series, for pruning
}

// NewBackendMetrics registers the backend metric families on the registry.
func NewBackendMetrics(reg prometheus.Registerer) (*BackendMetrics, error) {
	m := &BackendMetrics{
		status: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: "backend",
			Name:      "status",
			Help:      "Per-backend health: 1 on the current status label (healthy|unhealthy|pending), 0 otherwise.",
		}, []string{"backend", "status"}),
		activation: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: "backend",
			Name:      "activation_status",
			Help:      "Per-backend activation: 1 active, 0 inactive, -1 unknown (mirror ClusterMetricsStats.getActivationStatus).",
		}, []string{"backend"}),
		total: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "backends",
			Help:      "Number of known backends by health status (healthy|unhealthy|pending).",
		}, []string{"status"}),
		active: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "backends_active",
			Help:      "Number of active (monitored) backends.",
		}),
		seen: make(map[string]struct{}),
	}
	for _, c := range []prometheus.Collector{m.status, m.activation, m.total, m.active} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("metrics: register backend metrics: %w", err)
		}
	}
	return m, nil
}

// ObserveStatuses updates the gauges from a full backend snapshot, pruning series
// for backends that are no longer present. Implements monitor.StatusObserver.
func (m *BackendMetrics) ObserveStatuses(statuses map[string]monitor.TrinoStatus, _ map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Prune series for backends that left the set since the last snapshot.
	for url := range m.seen {
		if _, ok := statuses[url]; !ok {
			for _, label := range backendStatusLabels {
				m.status.DeleteLabelValues(url, label)
			}
			m.activation.DeleteLabelValues(url)
			delete(m.seen, url)
		}
	}

	counts := map[string]float64{"healthy": 0, "unhealthy": 0, "pending": 0}
	for url, st := range statuses {
		current := statusLabel(st)
		for _, label := range backendStatusLabels {
			v := 0.0
			if label == current {
				v = 1.0
			}
			m.status.WithLabelValues(url, label).Set(v)
		}
		// Every backend the monitor knows about is in the active set (the
		// composition root pushes only active backends), so activation is 1.
		m.activation.WithLabelValues(url).Set(1)
		counts[current]++
		m.seen[url] = struct{}{}
	}

	for label, n := range counts {
		m.total.WithLabelValues(label).Set(n)
	}
	m.active.Set(float64(len(statuses)))
}

// statusLabel maps a monitor.TrinoStatus to one of the gauge status labels.
// StatusUnknown is reported as "pending" — both mean "no confirmed health yet".
func statusLabel(s monitor.TrinoStatus) string {
	switch s {
	case monitor.StatusHealthy:
		return "healthy"
	case monitor.StatusUnhealthy:
		return "unhealthy"
	default:
		return "pending"
	}
}
