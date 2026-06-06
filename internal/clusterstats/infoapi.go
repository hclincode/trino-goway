package clusterstats

import "context"

// infoAPICollector is the default collector. It sets TrinoStatus ONLY, reusing
// the health monitor's /v1/info verdict (via StatusFunc) so it issues NO extra
// HTTP — byte-for-byte identical to trino-goway's pre-Phase-12 "always 0"
// behavior, and to Java's ClusterStatsInfoApiMonitor (counts 0, no worker count,
// no per-user breakdown).
type infoAPICollector struct {
	status StatusFunc
}

func newInfoAPICollector(status StatusFunc) *infoAPICollector {
	return &infoAPICollector{status: status}
}

// Collect returns the persistence-derived identity fields plus the reused health
// verdict; counts stay 0 and no HTTP is issued.
func (c *infoAPICollector) Collect(_ context.Context, b Backend) ClusterStats {
	cs := statsBuilder(b)
	if c.status != nil {
		cs.TrinoStatus = c.status(b.GetURL())
	}
	return cs
}
