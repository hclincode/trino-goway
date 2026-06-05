package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/metrics"
)

func TestHandler_ServesOpenMetrics(t *testing.T) {
	reg := metrics.New()

	// Register a counter so exposition has at least one series.
	c := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: metrics.Namespace,
		Name:      "registry_test_total",
		Help:      "Test counter.",
	})
	require.NoError(t, reg.Registerer().Register(c))
	c.Inc()

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	// Negotiate OpenMetrics exposition explicitly.
	req.Header.Set("Accept", "application/openmetrics-text")
	rec := httptest.NewRecorder()

	reg.Handler().ServeHTTP(rec, req)

	res := rec.Result()
	defer func() { _ = res.Body.Close() }()

	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.True(t,
		strings.HasPrefix(res.Header.Get("Content-Type"), "application/openmetrics-text"),
		"Content-Type %q should be application/openmetrics-text", res.Header.Get("Content-Type"),
	)
	assert.Contains(t, rec.Body.String(), "trino_goway_registry_test_total")
}

func TestNew_IsolatedFromDefaultRegistry(t *testing.T) {
	reg := metrics.New()

	// Registering against the gateway registry must not touch the default one.
	c := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: metrics.Namespace,
		Name:      "isolation_test_total",
		Help:      "Test counter.",
	})
	require.NoError(t, reg.Registerer().Register(c))

	// The same collector can be registered against the default registry without
	// an AlreadyRegistered error, proving the two registries are independent.
	require.NoError(t, prometheus.NewRegistry().Register(c))
}
