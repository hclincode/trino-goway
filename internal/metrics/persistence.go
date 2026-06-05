package metrics

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

// PersistenceMetrics records persistence and backend-refresh metrics. Its
// HistoryInsert method satisfies the persistence package's Metrics interface;
// SetDBUp and BackendRefresh are driven directly by the composition root.
type PersistenceMetrics struct {
	dbUp           prometheus.Gauge
	historyInserts *prometheus.CounterVec // {result}
	backendRefresh *prometheus.CounterVec // {result}
}

// NewPersistenceMetrics registers the persistence metric families on the registry.
func NewPersistenceMetrics(reg prometheus.Registerer) (*PersistenceMetrics, error) {
	m := &PersistenceMetrics{
		dbUp: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "db_up",
			Help:      "Whether the database is reachable: 1 up, 0 down.",
		}),
		historyInserts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "query_history_inserts_total",
			Help:      "Query-history insert attempts, by result (ok|error).",
		}, []string{"result"}),
		backendRefresh: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "backend_refresh_total",
			Help:      "Backend-list reloads from the database, by result (ok|error).",
		}, []string{"result"}),
	}
	for _, c := range []prometheus.Collector{m.dbUp, m.historyInserts, m.backendRefresh} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("metrics: register persistence metrics: %w", err)
		}
	}
	return m, nil
}

// HistoryInsert records a query-history insert result ("ok" or "error").
func (m *PersistenceMetrics) HistoryInsert(result string) {
	m.historyInserts.WithLabelValues(result).Inc()
}

// SetDBUp sets the db_up gauge.
func (m *PersistenceMetrics) SetDBUp(up bool) {
	if up {
		m.dbUp.Set(1)
		return
	}
	m.dbUp.Set(0)
}

// BackendRefresh records a backend-refresh result ("ok" or "error").
func (m *PersistenceMetrics) BackendRefresh(result string) {
	m.backendRefresh.WithLabelValues(result).Inc()
}
