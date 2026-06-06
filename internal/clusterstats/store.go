package clusterstats

// NOTE: clusterstats must NOT import internal/monitor. The dependency direction
// is one-way (monitor → clusterstats). The shared status enum lives in the
// dependency-free internal/clusterstatus leaf package.

import "sync/atomic"

// StatsStore holds the latest per-backend ClusterStats snapshot, keyed by backend
// NAME (matching Java's BackendStateManager which keys by clusterId). It is the
// write side for the monitor's StatsObserver and the read side for the admin
// StatsProvider.
//
// The snapshot is held in an atomic.Pointer and replaced in a single swap per
// probe tick, so concurrent readers never block writers and never observe a
// partially-built map.
type StatsStore struct {
	snap atomic.Pointer[map[string]ClusterStats]
}

// NewStatsStore returns an empty StatsStore. Until the first ObserveStats call,
// Stats returns the zero (uncollected) ClusterStats for every name.
func NewStatsStore() *StatsStore {
	s := &StatsStore{}
	empty := make(map[string]ClusterStats)
	s.snap.Store(&empty)
	return s
}

// ObserveStats replaces the current snapshot with stats in a single atomic swap.
// The map is taken as-is (the caller hands ownership of a freshly-built per-tick
// map), so callers must not mutate it after the call.
func (s *StatsStore) ObserveStats(stats map[string]ClusterStats) {
	s.snap.Store(&stats)
}

// Stats returns the latest ClusterStats for the named backend. For a backend that
// has not been collected yet it returns a zero ClusterStats with ClusterID set to
// name (counts 0, TrinoStatus Unknown, UserQueuedCount nil) — never an error
// (R7). The admin boundary fills proxyTo/externalUrl/routingGroup from persistence
// for this uncollected-default case (choice b).
func (s *StatsStore) Stats(name string) ClusterStats {
	p := s.snap.Load()
	if p != nil {
		if cs, ok := (*p)[name]; ok {
			return cs
		}
	}
	return ClusterStats{ClusterID: name}
}
