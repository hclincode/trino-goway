package clusterstats

import "context"

// noopCollector issues no HTTP and reports zero counts. TrinoStatus is taken from
// the reused health verdict so the M7 wire shape still carries a meaningful
// status. Mirrors Java's NoopClusterStatsMonitor.
type noopCollector struct {
	status StatusFunc
}

func newNoopCollector(status StatusFunc) *noopCollector {
	return &noopCollector{status: status}
}

// Collect returns the persistence-derived identity fields plus the reused health
// verdict; counts stay 0 and no HTTP is issued.
func (c *noopCollector) Collect(_ context.Context, b Backend) ClusterStats {
	cs := statsBuilder(b)
	if c.status != nil {
		cs.TrinoStatus = c.status(b.GetURL())
	}
	return cs
}
