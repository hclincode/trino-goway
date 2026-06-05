package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/metrics"
)

func TestRegisterRuntime_ExposesGoAndProcessFamilies(t *testing.T) {
	reg := metrics.New()
	require.NoError(t, reg.RegisterRuntime())

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	// Go runtime collector families.
	assert.Contains(t, body, "go_goroutines")
	assert.Contains(t, body, "go_gc_duration_seconds")
	assert.Contains(t, body, "go_memstats_alloc_bytes")
	// Process collector families.
	assert.Contains(t, body, "process_cpu_seconds_total")
	assert.Contains(t, body, "process_start_time_seconds")
}

func TestRegisterRuntime_SecondCallReturnsError(t *testing.T) {
	reg := metrics.New()
	require.NoError(t, reg.RegisterRuntime())
	// Re-registering the same collectors must surface a wrapped error, not panic.
	require.Error(t, reg.RegisterRuntime())
}
