package routing

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const headProbeTimeout = 3 * time.Second

// HistoryLookup is the consumer-defined interface for history-based backend lookup.
// Defined here per convention: consumer package owns the interface.
type HistoryLookup interface {
	LookupByQueryID(ctx context.Context, queryID string) (string, error)
}

// BackendLister is the consumer-defined interface for listing active backends.
type BackendLister interface {
	ListActive(ctx context.Context) ([]ActiveBackend, error)
}

// ActiveBackend is a minimal backend descriptor used by the recovery chain.
type ActiveBackend struct {
	Name         string
	URL          string
	RoutingGroup string
}

// recoveryChain implements the 3-step cache-miss recovery chain:
// 1. History DB lookup (LookupByQueryID)
// 2. HEAD probe fan-out to all active backends
// 3. First-active default
type recoveryChain struct {
	history     HistoryLookup
	backends    BackendLister
	probeClient *http.Client
	metrics     RouterMetrics
	sf          singleFlightGroup
}

// singleFlightGroup wraps sync to coalesce concurrent misses for the same queryID.
type singleFlightGroup struct {
	mu       sync.Mutex
	inflight map[string]*call
}

type call struct {
	wg  sync.WaitGroup
	val string
	err error
}

// do executes fn for key, coalescing concurrent callers.
func (g *singleFlightGroup) do(key string, fn func() (string, error)) (string, error) {
	g.mu.Lock()
	if g.inflight == nil {
		g.inflight = make(map[string]*call)
	}
	if c, ok := g.inflight[key]; ok {
		g.mu.Unlock()
		c.wg.Wait()
		return c.val, c.err
	}
	c := &call{}
	c.wg.Add(1)
	g.inflight[key] = c
	g.mu.Unlock()

	c.val, c.err = fn()
	c.wg.Done()

	g.mu.Lock()
	delete(g.inflight, key)
	g.mu.Unlock()

	return c.val, c.err
}

// recoverBackend runs the 3-step recovery chain for a queryID.
// Returns the backend URL or "" if no recovery path succeeds.
func (r *recoveryChain) recoverBackend(ctx context.Context, queryID string) string {
	// Step 1: history DB lookup (coalesced via singleflight)
	url, _ := r.sf.do(queryID, func() (string, error) {
		return r.history.LookupByQueryID(ctx, queryID)
	})
	if url != "" {
		r.recordStep(RecoveryStepHistory)
		return url
	}

	// Step 2: concurrent HEAD probe fan-out
	backends, err := r.backends.ListActive(ctx)
	if err != nil || len(backends) == 0 {
		return ""
	}
	if url := r.headProbeFanOut(ctx, queryID, backends); url != "" {
		r.recordStep(RecoveryStepProbe)
		return url
	}

	// Step 3: first-active default
	r.recordStep(RecoveryStepDefault)
	return backends[0].URL
}

// recordStep records a recovery-chain step, tolerating a nil metrics recorder
// (the recovery chain may be constructed directly in tests).
func (r *recoveryChain) recordStep(step string) {
	if r.metrics != nil {
		r.metrics.RecoveryStep(step)
	}
}

// headProbeFanOut sends HEAD /v1/query/<queryID> to all backends concurrently and
// returns the URL of the first one that responds 200.
func (r *recoveryChain) headProbeFanOut(ctx context.Context, queryID string, backends []ActiveBackend) string {
	probeCtx, cancel := context.WithTimeout(ctx, headProbeTimeout)
	defer cancel()

	type result struct {
		url string
	}
	ch := make(chan result, len(backends))

	var wg sync.WaitGroup
	for _, b := range backends {
		wg.Add(1)
		go func(b ActiveBackend) {
			defer wg.Done()
			if r.probeOne(probeCtx, b.URL, queryID) {
				ch <- result{url: b.URL}
			}
		}(b)
	}
	// Close channel when all probes finish.
	go func() {
		wg.Wait()
		close(ch)
	}()

	for res := range ch {
		if res.url != "" {
			return res.url
		}
	}
	return ""
}

func (r *recoveryChain) probeOne(ctx context.Context, baseURL, queryID string) bool {
	url := fmt.Sprintf("%s/v1/query/%s", baseURL, queryID)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return false
	}
	resp, err := r.probeClient.Do(req)
	if err != nil {
		return false
	}
	_, _ = resp.Body.Read(make([]byte, 0))
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
