package persistence

// Metrics records persistence metrics. Defined here (consumer owns the
// interface) per project conventions; nil-safe via noopMetrics so call sites
// never nil-check.
type Metrics interface {
	// HistoryInsert records a query-history insert: result is "ok" or "error".
	HistoryInsert(result string)
}

// Persistence metric label values shared by DAOs and the metrics implementation.
const (
	ResultOK    = "ok"
	ResultError = "error"
)

// noopMetrics is the nil-safe default Metrics used when none is injected.
type noopMetrics struct{}

func (noopMetrics) HistoryInsert(string) {}

// orNoop returns m, or a no-op Metrics when m is nil.
func orNoop(m Metrics) Metrics {
	if m == nil {
		return noopMetrics{}
	}
	return m
}
