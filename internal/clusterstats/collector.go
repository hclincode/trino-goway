package clusterstats

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/hclincode/trino-goway/internal/clusterstatus"
	"github.com/hclincode/trino-goway/internal/config"
)

// Backend is the minimal view of a backend a Collector needs. It is satisfied by
// the monitor's SimpleBackend (consumer-owned interface, defined here).
type Backend interface {
	GetName() string
	GetURL() string // proxyTo
	GetExternalURL() string
	GetRoutingGroup() string
}

// Collector collects live cluster statistics for a single backend. Implementations
// are selected by config via NewCollector; each is one file (noop, infoapi, uiapi,
// metrics). Collect must never panic: on any error it returns a best-effort
// partial ClusterStats (at minimum ClusterID + the persistence-derived fields).
type Collector interface {
	Collect(ctx context.Context, b Backend) ClusterStats
}

// StatusFunc reuses the health monitor's verdict for a backend URL so the
// INFO_API/NOOP collectors can set TrinoStatus without issuing any extra HTTP.
type StatusFunc func(url string) clusterstatus.Status

// NewCollector selects and constructs the Collector for the configured monitor
// type. INFO_API is the default (also chosen for an empty type). UI_API and
// METRICS issue authenticated stats requests over httpClient; NOOP and INFO_API
// issue no stats HTTP and reuse monitorStatus. JDBC/JMX (and any other value)
// return an error — defense-in-depth behind config.Validate (R8).
func NewCollector(
	cfg config.ClusterStatsConfig,
	mon config.MonitorConfig,
	bs config.BackendStateConfig,
	monitorStatus StatusFunc,
	httpClient *http.Client,
	log *slog.Logger,
) (Collector, error) {
	if log == nil {
		log = slog.Default()
	}
	switch cfg.MonitorType {
	case "NOOP":
		return newNoopCollector(monitorStatus), nil
	case "", "INFO_API":
		return newInfoAPICollector(monitorStatus), nil
	case "UI_API":
		return newUIAPICollector(mon, bs, httpClient, log), nil
	case "METRICS":
		return newMetricsCollector(mon, bs, httpClient, log), nil
	default:
		return nil, fmt.Errorf("clusterstats: monitor type %q not supported", cfg.MonitorType)
	}
}

// statsBuilder returns a ClusterStats pre-populated with the persistence-derived
// identity fields shared by every collector (ClusterID, ProxyTo, ExternalURL,
// RoutingGroup), mirroring Java's ClusterStatsMonitor.getClusterStatsBuilder.
func statsBuilder(b Backend) ClusterStats {
	return ClusterStats{
		ClusterID:    b.GetName(),
		ProxyTo:      b.GetURL(),
		ExternalURL:  b.GetExternalURL(),
		RoutingGroup: b.GetRoutingGroup(),
	}
}
