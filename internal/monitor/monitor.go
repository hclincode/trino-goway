package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hclincode/trino-goway/internal/clusterstats"
	"github.com/hclincode/trino-goway/internal/config"
)

// Backend is the minimal interface the monitor needs from persistence.
// Defined here (consumer package) per conventions. It is a superset of
// clusterstats.Backend so the same backend value drives both health probes and
// stats collection.
type Backend interface {
	GetName() string
	GetURL() string
	GetExternalURL() string
	GetRoutingGroup() string
}

// StatsObserver is notified of the per-tick cluster-stats snapshot, keyed by
// backend name. Defined here (consumer owns the interface); nil-safe like
// StatusObserver — when the monitor has no stats observer the notification is
// skipped. This is the one place internal/monitor imports internal/clusterstats
// (one-way: monitor → clusterstats).
type StatsObserver interface {
	ObserveStats(map[string]clusterstats.ClusterStats)
}

// StatusObserver is notified of the full backend health snapshot after every
// probe cycle and whenever the backend set changes. Defined here (consumer owns
// the interface) per conventions; nil-safe — when the monitor's observer is nil
// every notification is skipped.
//
// statuses is keyed by backend URL; names maps backend URL → backend name so the
// observer can label per-backend series and prune series for backends no longer
// present.
type StatusObserver interface {
	ObserveStatuses(statuses map[string]TrinoStatus, names map[string]string)
}

// SimpleBackend is a value type satisfying Backend, used by tests and the composition root.
type SimpleBackend struct {
	Name         string
	URL          string
	ExternalURL  string
	RoutingGroup string
}

// GetName returns the backend's name.
func (b SimpleBackend) GetName() string { return b.Name }

// GetURL returns the backend's URL (proxyTo).
func (b SimpleBackend) GetURL() string { return b.URL }

// GetExternalURL returns the backend's external URL (may be empty).
func (b SimpleBackend) GetExternalURL() string { return b.ExternalURL }

// GetRoutingGroup returns the backend's routing group (may be empty).
func (b SimpleBackend) GetRoutingGroup() string { return b.RoutingGroup }

// Monitor periodically probes backends and maintains a health map.
type Monitor struct {
	cfg    config.MonitorConfig
	client *http.Client
	log    *slog.Logger

	// status is updated atomically after each probe cycle.
	// Readers call Status() which does atomic.Pointer load — zero lock contention.
	status atomic.Pointer[map[string]TrinoStatus]

	backends []Backend
	mu       sync.RWMutex // protects backends slice
	cancel   context.CancelFunc
	done     chan struct{}

	// onFirstTick is called once after the first probe cycle completes (even if no backends).
	// Set via SetOnFirstTick before Start.
	onFirstTick   func()
	firstTickOnce sync.Once

	// observer is notified of status snapshots; nil-safe. Set via SetObserver before Start.
	observer StatusObserver

	// collector collects per-backend cluster stats on the same probe tick; nil-safe.
	// Set via SetClusterStatsCollector before Start.
	collector clusterstats.Collector
	// statsObserver receives the per-tick name-keyed stats snapshot; nil-safe.
	// Set via SetStatsObserver before Start.
	statsObserver StatsObserver
}

// New creates a new Monitor with the given config, HTTP client, and logger.
// The provided client should have an appropriate timeout for monitor probes.
func New(cfg config.MonitorConfig, client *http.Client, log *slog.Logger) *Monitor {
	m := &Monitor{
		cfg:    cfg,
		client: client,
		log:    log,
		done:   make(chan struct{}),
	}
	// Initialize with an empty status map.
	empty := make(map[string]TrinoStatus)
	m.status.Store(&empty)
	return m
}

// SetOnFirstTick registers fn to be called once after the first probe cycle completes.
// Must be called before Start.
func (m *Monitor) SetOnFirstTick(fn func()) {
	m.onFirstTick = fn
}

// SetObserver registers a StatusObserver notified of status snapshots after each
// probe cycle and whenever the backend set changes. Must be called before Start.
func (m *Monitor) SetObserver(o StatusObserver) {
	m.observer = o
}

// SetClusterStatsCollector registers the collector that gathers per-backend
// cluster stats on each probe tick. Nil-safe (a nil collector disables stats
// collection). Must be called before Start.
func (m *Monitor) SetClusterStatsCollector(c clusterstats.Collector) {
	m.collector = c
}

// SetStatsObserver registers the observer that receives the per-tick name-keyed
// stats snapshot. Nil-safe. Must be called before Start.
func (m *Monitor) SetStatsObserver(o StatsObserver) {
	m.statsObserver = o
}

// notifyObserver builds the current snapshot (URL → status, URL → name) and
// notifies the observer. Backends in the set that have not been probed yet are
// reported as StatusPending. Safe to call with a nil observer (no-op).
func (m *Monitor) notifyObserver() {
	if m.observer == nil {
		return
	}
	m.mu.RLock()
	names := make(map[string]string, len(m.backends))
	for _, b := range m.backends {
		names[b.GetURL()] = b.GetName()
	}
	m.mu.RUnlock()

	statusPtr := m.status.Load()
	statuses := make(map[string]TrinoStatus, len(names))
	for url := range names {
		if statusPtr != nil {
			if s, ok := (*statusPtr)[url]; ok {
				statuses[url] = s
				continue
			}
		}
		statuses[url] = StatusPending
	}
	m.observer.ObserveStatuses(statuses, names)
}

// SetBackends replaces the current backend list.
// Called when backends are added or removed via the admin API.
func (m *Monitor) SetBackends(backends []Backend) {
	cp := make([]Backend, len(backends))
	copy(cp, backends)
	m.mu.Lock()
	m.backends = cp
	m.mu.Unlock()
	// Refresh observer series so added/removed backends are reflected before the
	// next probe cycle (newly-added backends report as StatusPending).
	m.notifyObserver()
}

// Status returns the current health status for a backend URL.
// Returns StatusUnknown if the backend has not been probed yet.
func (m *Monitor) Status(url string) TrinoStatus {
	p := m.status.Load()
	if p == nil {
		return StatusUnknown
	}
	s, ok := (*p)[url]
	if !ok {
		return StatusUnknown
	}
	return s
}

// SetBackendStatus sets the health status for a specific backend URL directly.
// Used by the admin API when a backend is activated or deactivated.
func (m *Monitor) SetBackendStatus(url string, status TrinoStatus) {
	old := m.status.Load()
	next := make(map[string]TrinoStatus, len(*old)+1)
	for k, v := range *old {
		next[k] = v
	}
	next[url] = status
	m.status.Store(&next)
	m.notifyObserver()
}

// AllStatuses returns a snapshot of all backend statuses keyed by URL.
func (m *Monitor) AllStatuses() map[string]TrinoStatus {
	p := m.status.Load()
	if p == nil {
		return map[string]TrinoStatus{}
	}
	snap := make(map[string]TrinoStatus, len(*p))
	for k, v := range *p {
		snap[k] = v
	}
	return snap
}

// Start begins the monitoring loop. Runs until ctx is cancelled.
// Returns an error if the monitor is misconfigured.
func (m *Monitor) Start(ctx context.Context) error {
	if m.cfg.Interval.D == 0 {
		return fmt.Errorf("monitor: start: interval must be > 0")
	}
	if m.cfg.CheckTimeout.D == 0 {
		return fmt.Errorf("monitor: start: checkTimeout must be > 0")
	}

	ctx, cancel := context.WithCancel(ctx)
	m.cancel = cancel

	// goroutine exits when ctx is cancelled
	go func() {
		defer close(m.done)
		m.run(ctx)
	}()

	return nil
}

// Stop signals the monitor to stop and waits for it to exit.
// The provided ctx bounds how long we wait for the monitor to drain.
func (m *Monitor) Stop(ctx context.Context) error {
	if m.cancel != nil {
		m.cancel()
	}
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("monitor: stop: %w", ctx.Err())
	}
}

func (m *Monitor) run(ctx context.Context) {
	ticker := time.NewTicker(m.cfg.Interval.D)
	defer ticker.Stop()

	// Probe immediately on start.
	m.probe(ctx)

	for {
		select {
		case <-ticker.C:
			m.probe(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (m *Monitor) probe(ctx context.Context) {
	defer m.firstTickOnce.Do(func() {
		if m.onFirstTick != nil {
			m.onFirstTick()
		}
	})

	m.mu.RLock()
	backends := make([]Backend, len(m.backends))
	copy(backends, m.backends)
	m.mu.RUnlock()

	if len(backends) == 0 {
		return
	}

	// Fan-out: one goroutine per backend.
	results := make(map[string]TrinoStatus, len(backends))
	var resultMu sync.Mutex
	var wg sync.WaitGroup

	for _, b := range backends {
		wg.Add(1)
		// goroutine exits when probe completes or probeCtx times out
		go func(b Backend) {
			defer wg.Done()
			probeCtx, cancel := context.WithTimeout(ctx, m.cfg.CheckTimeout.D)
			defer cancel()
			status := m.checkOne(probeCtx, b.GetURL())
			resultMu.Lock()
			results[b.GetURL()] = status
			resultMu.Unlock()
		}(b)
	}
	wg.Wait()

	// Single atomic swap.
	next := results
	m.status.Store(&next)

	m.notifyObserver()

	// Cluster-stats collection rides the same tick (no second scheduler, R1). It
	// runs AFTER the health swap so collectors that reuse the monitor verdict
	// (INFO_API/NOOP) observe this tick's status. Nil collector or nil observer
	// disables it. Stats must never gate or abort readiness (R7) — this block has
	// no effect on m.status or onFirstTick.
	m.collectStats(ctx, backends)

	m.log.Debug("monitor: probe complete", "backends", len(backends))
}

// collectStats fans out the configured collector across backends (reusing the
// per-backend pattern from the health probe) and hands one name-keyed snapshot to
// the stats observer. No-op when either the collector or the observer is nil.
func (m *Monitor) collectStats(ctx context.Context, backends []Backend) {
	if m.collector == nil || m.statsObserver == nil {
		return
	}

	// Stats requests get their own (typically longer) deadline; the collectors
	// also apply StatsTimeout internally, so this outer bound just caps a stuck
	// Collect. Falls back to CheckTimeout when StatsTimeout is unset.
	statsTimeout := m.cfg.StatsTimeout.D
	if statsTimeout <= 0 {
		statsTimeout = m.cfg.CheckTimeout.D
	}

	snapshot := make(map[string]clusterstats.ClusterStats, len(backends))
	var snapMu sync.Mutex
	var wg sync.WaitGroup

	for _, b := range backends {
		wg.Add(1)
		// goroutine exits when Collect returns or probeCtx times out
		go func(b Backend) {
			defer wg.Done()
			probeCtx, cancel := context.WithTimeout(ctx, statsTimeout)
			defer cancel()
			cs := m.collector.Collect(probeCtx, b)
			snapMu.Lock()
			snapshot[b.GetName()] = cs
			snapMu.Unlock()
		}(b)
	}
	wg.Wait()

	m.statsObserver.ObserveStats(snapshot)
}

// trinoInfoResponse is the JSON shape returned by Trino's /v1/info endpoint.
type trinoInfoResponse struct {
	Starting bool `json:"starting"`
}

// checkOne probes a single backend's /v1/info endpoint.
// Returns StatusHealthy if the backend responds with 200 and {"starting": false}.
// Returns StatusPending if the cluster is reachable but still starting
// ({"starting": true}), matching Java's ClusterStatsInfoApiMonitor which maps
// starting → PENDING. Returns StatusUnhealthy for any transport/decode error or
// a non-200 response.
func (m *Monitor) checkOne(ctx context.Context, url string) TrinoStatus {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/v1/info", nil)
	if err != nil {
		m.log.Warn("monitor: checkOne: build request failed", "url", url, "err", err)
		return StatusUnhealthy
	}

	resp, err := m.client.Do(req)
	if err != nil {
		m.log.Debug("monitor: checkOne: request failed", "url", url, "err", err)
		return StatusUnhealthy
	}
	defer func() {
		// Drain and close to allow connection reuse; discard read errors — unactionable here.
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		m.log.Debug("monitor: checkOne: non-200 status", "url", url, "status", resp.StatusCode)
		return StatusUnhealthy
	}

	var info trinoInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		m.log.Warn("monitor: checkOne: decode response failed", "url", url, "err", err)
		return StatusUnhealthy
	}

	if info.Starting {
		m.log.Debug("monitor: checkOne: backend still starting", "url", url)
		return StatusPending
	}

	return StatusHealthy
}
