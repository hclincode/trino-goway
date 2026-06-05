package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/metrics"
)

// findMetric returns the dto.Metric whose label set contains all wanted pairs,
// or nil if none matches.
func findMetric(t *testing.T, fams []*dto.MetricFamily, name string, want map[string]string) *dto.Metric {
	t.Helper()
	for _, f := range fams {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			match := true
			for k, v := range want {
				if labels[k] != v {
					match = false
					break
				}
			}
			if match {
				return m
			}
		}
	}
	return nil
}

func TestHTTPMetrics_Middleware(t *testing.T) {
	reg := metrics.New()
	hm, err := metrics.NewHTTPMetrics(reg.Registerer())
	require.NoError(t, err)

	r := chi.NewRouter()
	r.Use(hm.Middleware("proxy"))
	r.Get("/v1/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	// Two requests against the same route pattern.
	for i := 0; i < 2; i++ {
		res, err := http.Get(srv.URL + "/v1/abc")
		require.NoError(t, err)
		require.NoError(t, res.Body.Close())
		assert.Equal(t, http.StatusCreated, res.StatusCode)
	}

	fams, err := reg.Gatherer().Gather()
	require.NoError(t, err)

	// requests_total counts both requests, labelled with the route PATTERN not the path.
	reqWant := map[string]string{
		"listener": "proxy", "method": "GET", "pattern": "/v1/{id}", "code": "201",
	}
	reqMetric := findMetric(t, fams, "trino_goway_http_requests_total", reqWant)
	require.NotNil(t, reqMetric, "requests_total series not found")
	assert.Equal(t, float64(2), reqMetric.GetCounter().GetValue())

	// No series should carry the raw path.
	assert.Nil(t, findMetric(t, fams, "trino_goway_http_requests_total",
		map[string]string{"pattern": "/v1/abc"}), "raw path must not appear as a pattern")

	// duration histogram observed both requests.
	durWant := map[string]string{"listener": "proxy", "method": "GET", "pattern": "/v1/{id}"}
	durMetric := findMetric(t, fams, "trino_goway_http_request_duration_seconds", durWant)
	require.NotNil(t, durMetric, "duration histogram series not found")
	assert.Equal(t, uint64(2), durMetric.GetHistogram().GetSampleCount())

	// in-flight balances back to zero after requests complete.
	inflightMetric := findMetric(t, fams, "trino_goway_http_requests_in_flight",
		map[string]string{"listener": "proxy"})
	require.NotNil(t, inflightMetric, "in_flight series not found")
	assert.Equal(t, float64(0), inflightMetric.GetGauge().GetValue())
}
