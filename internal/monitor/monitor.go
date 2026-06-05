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

	"github.com/hclincode/trino-goway/internal/config"
)

// Backend is the minimal interface the monitor needs from persistence.
// Defined here (consumer package) per conventions.
type Backend interface {
	GetName() string
	GetURL() string
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
	Name string
	URL  string
}

// GetName returns the backend's name.
func (b SimpleBackend) GetName() string { return b.Name }

// GetURL returns the backend's URL.
func (b SimpleBackend) GetURL() string { return b.URL }

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

	m.log.Debug("monitor: probe complete", "backends", len(backends))
}

// trinoInfoResponse is the JSON shape returned by Trino's /v1/info endpoint.
type trinoInfoResponse struct {
	Starting bool `json:"starting"`
}

// checkOne probes a single backend's /v1/info endpoint.
// Returns StatusHealthy if the backend responds with 200 and {"starting": false}.
// Returns StatusUnhealthy for any error or if the cluster is still starting.
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
		return StatusUnhealthy
	}

	return StatusHealthy
}
