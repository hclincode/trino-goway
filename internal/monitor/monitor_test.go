package monitor_test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/config"
	"github.com/hclincode/trino-goway/internal/monitor"
	"github.com/hclincode/trino-goway/internal/testutil"
)

func TestMain(m *testing.M) {
	testutil.VerifyTestMain(m)
}

func newTestMonitor(t *testing.T) *monitor.Monitor {
	t.Helper()
	cfg := config.MonitorConfig{
		Interval:     config.Duration{D: 10 * time.Millisecond},
		CheckTimeout: config.Duration{D: 1 * time.Second},
	}
	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return monitor.New(cfg, client, newDiscardLogger())
}

// TestMonitor_HealthyBackend verifies that a healthy backend is detected within one tick.
func TestMonitor_HealthyBackend(t *testing.T) {
	backend := testutil.NewFakeBackend(t) // default: responds 200 {"starting":false}

	m := newTestMonitor(t)
	m.SetBackends([]monitor.Backend{
		monitor.SimpleBackend{Name: "test", URL: backend.URL},
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		require.NoError(t, m.Stop(stopCtx))
	})

	require.NoError(t, m.Start(ctx))

	require.Eventually(t, func() bool {
		return m.Status(backend.URL) == monitor.StatusHealthy
	}, time.Second, 10*time.Millisecond, "expected backend to become healthy")
}

// TestMonitor_UnhealthyBackend verifies that a backend returning 500 is marked unhealthy.
func TestMonitor_UnhealthyBackend(t *testing.T) {
	backend := testutil.NewFakeBackend(t, testutil.WithStatusCode(http.StatusInternalServerError))

	m := newTestMonitor(t)
	m.SetBackends([]monitor.Backend{
		monitor.SimpleBackend{Name: "test", URL: backend.URL},
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		require.NoError(t, m.Stop(stopCtx))
	})

	require.NoError(t, m.Start(ctx))

	require.Eventually(t, func() bool {
		return m.Status(backend.URL) == monitor.StatusUnhealthy
	}, time.Second, 10*time.Millisecond, "expected backend to become unhealthy")
}

// TestMonitor_SetBackends verifies that a backend added after Start is probed on the next tick.
func TestMonitor_SetBackends(t *testing.T) {
	m := newTestMonitor(t)
	// Start with no backends.

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		require.NoError(t, m.Stop(stopCtx))
	})

	require.NoError(t, m.Start(ctx))

	// All statuses should be empty initially.
	assert.Empty(t, m.AllStatuses())

	// Now add a backend.
	backend := testutil.NewFakeBackend(t)
	m.SetBackends([]monitor.Backend{
		monitor.SimpleBackend{Name: "added", URL: backend.URL},
	})

	require.Eventually(t, func() bool {
		return m.Status(backend.URL) == monitor.StatusHealthy
	}, time.Second, 10*time.Millisecond, "expected added backend to become healthy")
}

// TestMonitor_OnFirstTick verifies that the callback fires exactly once after the first probe.
func TestMonitor_OnFirstTick(t *testing.T) {
	backend := testutil.NewFakeBackend(t)
	m := newTestMonitor(t)
	m.SetBackends([]monitor.Backend{monitor.SimpleBackend{Name: "test", URL: backend.URL}})

	var called int32
	m.SetOnFirstTick(func() { atomic.AddInt32(&called, 1) })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		require.NoError(t, m.Stop(stopCtx))
	})

	require.NoError(t, m.Start(ctx))

	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&called) == 1
	}, time.Second, 10*time.Millisecond, "OnFirstTick not fired")

	// Wait a couple more ticks to confirm it fires exactly once.
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(1), atomic.LoadInt32(&called), "OnFirstTick fired more than once")
}

// TestMonitor_OnFirstTick_NoBackends verifies that the callback still fires when no backends are configured.
func TestMonitor_OnFirstTick_NoBackends(t *testing.T) {
	m := newTestMonitor(t)
	// No backends set.

	var called int32
	m.SetOnFirstTick(func() { atomic.AddInt32(&called, 1) })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		require.NoError(t, m.Stop(stopCtx))
	})

	require.NoError(t, m.Start(ctx))

	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&called) == 1
	}, time.Second, 10*time.Millisecond, "OnFirstTick not fired with empty backend list")
}

// TestSimpleBackend_Accessors verifies GetName and GetURL.
func TestSimpleBackend_Accessors(t *testing.T) {
	b := monitor.SimpleBackend{Name: "my-backend", URL: "http://trino:8080"}
	assert.Equal(t, "my-backend", b.GetName())
	assert.Equal(t, "http://trino:8080", b.GetURL())
}

// TestMonitor_SetBackendStatus verifies the copy-on-write atomic update.
func TestMonitor_SetBackendStatus(t *testing.T) {
	m := newTestMonitor(t)

	m.SetBackendStatus("http://a:8080", monitor.StatusHealthy)
	assert.Equal(t, monitor.StatusHealthy, m.Status("http://a:8080"))

	m.SetBackendStatus("http://b:8080", monitor.StatusUnhealthy)
	assert.Equal(t, monitor.StatusUnhealthy, m.Status("http://b:8080"))

	// Updating one key does not disturb the other.
	assert.Equal(t, monitor.StatusHealthy, m.Status("http://a:8080"))
}

// TestMonitor_AllStatuses_Snapshot verifies AllStatuses returns a copy.
func TestMonitor_AllStatuses_Snapshot(t *testing.T) {
	m := newTestMonitor(t)
	m.SetBackendStatus("http://a:8080", monitor.StatusHealthy)
	m.SetBackendStatus("http://b:8080", monitor.StatusUnhealthy)

	snap := m.AllStatuses()
	assert.Equal(t, monitor.StatusHealthy, snap["http://a:8080"])
	assert.Equal(t, monitor.StatusUnhealthy, snap["http://b:8080"])

	// Mutating the snapshot does not affect the monitor.
	snap["http://a:8080"] = monitor.StatusUnhealthy
	assert.Equal(t, monitor.StatusHealthy, m.Status("http://a:8080"), "snapshot mutation must not affect monitor")
}

// TestMonitor_Start_Errors verifies that Start rejects zero-value config fields.
func TestMonitor_Start_Errors(t *testing.T) {
	t.Run("zero interval", func(t *testing.T) {
		m := monitor.New(config.MonitorConfig{
			CheckTimeout: config.Duration{D: time.Second},
		}, &http.Client{}, newDiscardLogger())
		err := m.Start(context.Background())
		assert.Error(t, err)
	})

	t.Run("zero checkTimeout", func(t *testing.T) {
		m := monitor.New(config.MonitorConfig{
			Interval: config.Duration{D: 10 * time.Millisecond},
		}, &http.Client{}, newDiscardLogger())
		err := m.Start(context.Background())
		assert.Error(t, err)
	})
}

// TestMonitor_StartingBackend verifies that a backend returning {"starting":true} is marked unhealthy.
func TestMonitor_StartingBackend(t *testing.T) {
	backend := testutil.NewFakeBackend(t, testutil.WithBody(`{"starting":true}`))

	m := newTestMonitor(t)
	m.SetBackends([]monitor.Backend{monitor.SimpleBackend{Name: "test", URL: backend.URL}})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		require.NoError(t, m.Stop(stopCtx))
	})

	require.NoError(t, m.Start(ctx))

	require.Eventually(t, func() bool {
		return m.Status(backend.URL) == monitor.StatusUnhealthy
	}, time.Second, 10*time.Millisecond, "backend reporting starting=true must be marked unhealthy")
}

// TestMonitor_MalformedJSONBackend verifies that a backend returning malformed JSON is marked unhealthy.
func TestMonitor_MalformedJSONBackend(t *testing.T) {
	backend := testutil.NewFakeBackend(t, testutil.WithBody(`{not valid json}`))

	m := newTestMonitor(t)
	m.SetBackends([]monitor.Backend{monitor.SimpleBackend{Name: "test", URL: backend.URL}})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		require.NoError(t, m.Stop(stopCtx))
	})

	require.NoError(t, m.Start(ctx))

	require.Eventually(t, func() bool {
		return m.Status(backend.URL) == monitor.StatusUnhealthy
	}, time.Second, 10*time.Millisecond, "backend returning malformed JSON must be marked unhealthy")
}

// TestTrinoStatus_String verifies all TrinoStatus String() values.
func TestTrinoStatus_String(t *testing.T) {
	assert.Equal(t, "healthy", monitor.StatusHealthy.String())
	assert.Equal(t, "unhealthy", monitor.StatusUnhealthy.String())
	assert.Equal(t, "pending", monitor.StatusPending.String())
	assert.Equal(t, "unknown", monitor.StatusUnknown.String())
}

// TestMonitor_GoroutineLeak verifies that Stop cleans up all goroutines.
func TestMonitor_GoroutineLeak(t *testing.T) {
	m := newTestMonitor(t)

	ctx := context.Background()
	require.NoError(t, m.Start(ctx))

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, m.Stop(stopCtx))
}

// TestMonitor_Stop_ContextTimeout verifies that Stop returns an error if the
// caller's context expires before the monitor goroutine drains.
func TestMonitor_Stop_ContextTimeout(t *testing.T) {
	m := newTestMonitor(t)
	require.NoError(t, m.Start(context.Background()))

	// Context already cancelled — Stop must return immediately with ctx.Err().
	stopCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err := m.Stop(stopCtx)
	assert.Error(t, err)

	// Drain the real goroutine so the test does not leak.
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer drainCancel()
	_ = m.Stop(drainCtx)
}

// TestMonitor_CheckOne_MalformedURL verifies that a backend with a URL that
// fails http.NewRequestWithContext (e.g. invalid scheme/control char) is
// marked unhealthy rather than crashing the probe loop.
func TestMonitor_CheckOne_MalformedURL(t *testing.T) {
	m := newTestMonitor(t)
	// A URL containing a control character makes http.NewRequest fail.
	badURL := "http://bad\x7fhost"
	m.SetBackends([]monitor.Backend{monitor.SimpleBackend{Name: "bad", URL: badURL}})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		require.NoError(t, m.Stop(stopCtx))
	})

	require.NoError(t, m.Start(ctx))

	require.Eventually(t, func() bool {
		return m.Status(badURL) == monitor.StatusUnhealthy
	}, time.Second, 10*time.Millisecond, "malformed URL must be marked unhealthy, not panic")
}
