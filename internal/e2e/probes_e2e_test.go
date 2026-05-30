//go:build e2e

package e2e_test

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/e2e/harness"
)

// getStatus issues GET on the given URL and returns the HTTP status code,
// discarding the body. Fails the test on transport errors.
func getStatus(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err, "GET %s", url)
	_, _ = io.Copy(io.Discard, resp.Body)
	require.NoError(t, resp.Body.Close())
	return resp.StatusCode
}

// TestE2E_Livez_AlwaysOK verifies that /trino-gateway/livez returns 200 both
// immediately after startup and again after time has passed. Livez is the
// unconditional liveness signal and must never gate on dependencies.
func TestE2E_Livez_AlwaysOK(t *testing.T) {
	h := harness.New(t)

	assert.Equal(t, http.StatusOK, getStatus(t, h.AdminURL+"/trino-gateway/livez"),
		"livez must return 200 immediately after startup")

	time.Sleep(500 * time.Millisecond)

	assert.Equal(t, http.StatusOK, getStatus(t, h.AdminURL+"/trino-gateway/livez"),
		"livez must return 200 after time has passed")
}

// TestE2E_Readyz_200AfterFirstProbe verifies that after registering a backend
// and waiting for the monitor's first probe cycle, /trino-gateway/readyz
// returns 200. This is the post-first-probe ready state.
func TestE2E_Readyz_200AfterFirstProbe(t *testing.T) {
	h := harness.New(t, harness.WithMonitorInterval(2*time.Second))
	h.AddBackend(t, "trino-1", "default")

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if getStatus(t, h.AdminURL+"/trino-gateway/readyz") == http.StatusOK {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("readyz never returned 200 within 15s after first probe cycle")
}

// TestE2E_Readyz_503BeforeFirstProbe verifies that readyz returns 503 before
// the monitor's first probe completes, while livez still returns 200.
//
// We push the first probe far into the future (5m interval) and skip the
// harness's built-in readyz polling so we can observe the initial 503 state.
func TestE2E_Readyz_503BeforeFirstProbe(t *testing.T) {
	h := harness.New(t,
		harness.WithSkipReadyzWait(),
		harness.WithMonitorInterval(5*time.Minute),
	)

	// readyz must return 503 — SetReady is only invoked after the first probe.
	assert.Equal(t, http.StatusServiceUnavailable,
		getStatus(t, h.AdminURL+"/trino-gateway/readyz"),
		"readyz must return 503 before the first probe completes")

	// livez must still return 200 — it is independent of the probe cycle.
	assert.Equal(t, http.StatusOK,
		getStatus(t, h.AdminURL+"/trino-gateway/livez"),
		"livez must return 200 even before the first probe completes")
}
