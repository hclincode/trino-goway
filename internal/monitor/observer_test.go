package monitor_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/monitor"
	"github.com/hclincode/trino-goway/internal/testutil"
)

// recordingObserver captures the most recent status snapshot for assertions.
type recordingObserver struct {
	mu       sync.Mutex
	statuses map[string]monitor.TrinoStatus
	names    map[string]string
	calls    int
}

func (o *recordingObserver) ObserveStatuses(statuses map[string]monitor.TrinoStatus, names map[string]string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.statuses = statuses
	o.names = names
	o.calls++
}

func (o *recordingObserver) snapshot() (map[string]monitor.TrinoStatus, int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	cp := make(map[string]monitor.TrinoStatus, len(o.statuses))
	for k, v := range o.statuses {
		cp[k] = v
	}
	return cp, o.calls
}

func TestMonitor_Observer_SetBackendsReportsPending(t *testing.T) {
	m := newTestMonitor(t)
	obs := &recordingObserver{}
	m.SetObserver(obs)

	m.SetBackends([]monitor.Backend{monitor.SimpleBackend{Name: "c1", URL: "http://b1:8080"}})

	statuses, calls := obs.snapshot()
	assert.GreaterOrEqual(t, calls, 1)
	assert.Equal(t, monitor.StatusPending, statuses["http://b1:8080"],
		"unprobed backend must be reported as pending")
}

func TestMonitor_Observer_ProbeReportsHealthy(t *testing.T) {
	backend := testutil.NewFakeBackend(t)
	m := newTestMonitor(t)
	obs := &recordingObserver{}
	m.SetObserver(obs)
	m.SetBackends([]monitor.Backend{monitor.SimpleBackend{Name: "c1", URL: backend.URL}})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		require.NoError(t, m.Stop(stopCtx))
	})
	require.NoError(t, m.Start(ctx))

	require.Eventually(t, func() bool {
		statuses, _ := obs.snapshot()
		return statuses[backend.URL] == monitor.StatusHealthy
	}, time.Second, 10*time.Millisecond, "observer must see backend become healthy")
}

func TestMonitor_Observer_NilIsNoOp(t *testing.T) {
	m := newTestMonitor(t)
	// No observer set; these must not panic.
	m.SetBackends([]monitor.Backend{monitor.SimpleBackend{Name: "c1", URL: "http://b1:8080"}})
	m.SetBackendStatus("http://b1:8080", monitor.StatusHealthy)
}
