package clusterstats

import "github.com/hclincode/trino-goway/internal/clusterstatus"

// ClusterStats is the internal per-backend statistics model, mirroring Java's
// ClusterStats record. It is keyed by ClusterID (the backend name) in the store
// and mapped to the admin ClusterStatsResponse wire type at the M7 boundary.
//
// Per monitor type:
//   - INFO_API (default): TrinoStatus only; counts 0, NumWorkerNodes 0,
//     UserQueuedCount nil.
//   - UI_API: all fields live, including UserQueuedCount.
//   - METRICS: TrinoStatus + Running/QueuedQueryCount live; no NumWorkerNodes,
//     no UserQueuedCount.
//   - NOOP: zero counts, TrinoStatus from the reused health verdict.
type ClusterStats struct {
	// ClusterID is the backend name (matches Java's BackendStateManager keying).
	ClusterID         string
	RunningQueryCount int
	QueuedQueryCount  int
	NumWorkerNodes    int
	TrinoStatus       clusterstatus.Status
	ProxyTo           string
	ExternalURL       string
	RoutingGroup      string
	// UserQueuedCount maps session user → queued query count. Nil until a UI_API
	// collection populates it (matching Java's null default).
	UserQueuedCount map[string]int
}
