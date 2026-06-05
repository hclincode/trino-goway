//go:build e2e

package e2e_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/hclincode/trino-goway/internal/e2e/harness"
)

// newHarnessNoLeaks starts a harness and registers a goleak check for the test.
//
// The gateway runs out-of-process, so its goroutines never appear in this test
// process. The only harness-owned goroutines here are the os/exec stdout/stderr
// pipe copiers, which unblock asynchronously when the OS closes the child's pipe
// fds after exit — racing any leak check's poll window. We snapshot the current
// goroutine set with IgnoreCurrent right after the harness has spawned the child
// (so those copiers are already running and get ignored). The check then catches
// any goroutine the test body itself leaks (e.g. an un-closed HTTP client) while
// staying immune to subprocess teardown timing, regardless of cleanup ordering.
func newHarnessNoLeaks(t *testing.T, opts ...harness.Option) *harness.Harness {
	t.Helper()
	h := harness.New(t, opts...)
	ignore := goleak.IgnoreCurrent()
	t.Cleanup(func() { goleak.VerifyNone(t, ignore) })
	return h
}

// scrapeMetrics performs GET admin /metrics with the given Accept header and
// returns the response plus body. The caller asserts on the returned values.
func scrapeMetrics(t *testing.T, adminURL, accept string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, adminURL+"/metrics", nil)
	require.NoError(t, err)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err, "GET /metrics")
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	return resp, body
}

// acceptOpenMetrics negotiates the OpenMetrics exposition format.
//
// Only the OpenMetrics Content-Type is asserted today, not the OpenMetrics body
// itself: prometheus/common's decoder does not fully parse OpenMetrics (see
// prometheus/common#812), so the clean-parse assertion uses the text scrape
// (acceptText). Once #812 lands, parse the OpenMetrics body here directly.
const acceptOpenMetrics = "application/openmetrics-text"

// acceptText negotiates the classic Prometheus text exposition format, which the
// prometheus/common/expfmt decoder fully supports (its decoder only partially
// supports OpenMetrics — see prometheus/common#812 — so family-parsing tests
// scrape the text format while the content-type test asserts OpenMetrics).
const acceptText = "text/plain;version=0.0.4"

// scrapeFamilies scrapes the text exposition format and decodes it into families.
func scrapeFamilies(t *testing.T, adminURL string) map[string]*dto.MetricFamily {
	t.Helper()
	resp, body := scrapeMetrics(t, adminURL, acceptText)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return parseFamilies(t, resp, body)
}

// expositionFormat derives the expfmt.Format for a scrape response. It selects
// the format from the Content-Type media type directly — expfmt.ResponseFormat
// can misclassify the OpenMetrics media type as the legacy text format across
// versions, which then trips the text parser on OpenMetrics-only constructs
// (e.g. repeated HELP lines). Driving off Content-Type keeps the decoder aligned
// with what the server actually emitted.
func expositionFormat(t *testing.T, resp *http.Response) expfmt.Format {
	t.Helper()
	ct := resp.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "application/openmetrics-text"):
		f, err := expfmt.NewOpenMetricsFormat(expfmt.OpenMetricsVersion_1_0_0)
		require.NoError(t, err, "build OpenMetrics format")
		return f
	case strings.HasPrefix(ct, "text/plain"):
		return expfmt.NewFormat(expfmt.TypeTextPlain)
	default:
		if f := expfmt.ResponseFormat(resp.Header); f.FormatType() != expfmt.TypeUnknown {
			return f
		}
		t.Fatalf("unrecognized exposition Content-Type %q", ct)
		return ""
	}
}

// parseFamilies decodes an exposition body into MetricFamily protos. It fails the
// test if the payload does not parse cleanly.
func parseFamilies(t *testing.T, resp *http.Response, body []byte) map[string]*dto.MetricFamily {
	t.Helper()
	format := expositionFormat(t, resp)

	dec := expfmt.NewDecoder(strings.NewReader(string(body)), format)
	families := map[string]*dto.MetricFamily{}
	for {
		var mf dto.MetricFamily
		err := dec.Decode(&mf)
		if err == io.EOF {
			break
		}
		require.NoError(t, err, "exposition body must parse cleanly")
		families[mf.GetName()] = &mf
	}
	require.NotEmpty(t, families, "scrape must contain at least one metric family")
	return families
}

// TestE2E_Metrics_Endpoint_Scrape verifies the admin /metrics endpoint returns
// 200 with an OpenMetrics content-type and a body that parses cleanly.
func TestE2E_Metrics_Endpoint_Scrape(t *testing.T) {
	h := newHarnessNoLeaks(t)

	// OpenMetrics negotiation: 200 + OpenMetrics content-type.
	omResp, _ := scrapeMetrics(t, h.AdminURL, acceptOpenMetrics)
	assert.Equal(t, http.StatusOK, omResp.StatusCode)
	assert.True(t,
		strings.HasPrefix(omResp.Header.Get("Content-Type"), "application/openmetrics-text"),
		"Content-Type %q must be application/openmetrics-text", omResp.Header.Get("Content-Type"),
	)

	// The text exposition must parse cleanly with the Prometheus exposition parser.
	families := scrapeFamilies(t, h.AdminURL)
	assert.NotEmpty(t, families)
}

// TestE2E_Metrics_GoRuntimeFamilies verifies the Go runtime and process
// collectors are exposed (the Go-native replacement for the Java JVM metrics).
func TestE2E_Metrics_GoRuntimeFamilies(t *testing.T) {
	h := newHarnessNoLeaks(t)
	families := scrapeFamilies(t, h.AdminURL)

	assert.Contains(t, families, "go_goroutines", "go runtime collector must be registered")
	assert.Contains(t, families, "process_cpu_seconds_total", "process collector must be registered")
	assert.Contains(t, families, "process_start_time_seconds", "process collector must be registered")
}

// TestE2E_Metrics_AppFamilies verifies that after registering a backend and
// driving a proxied request, the application metric families are present with
// expected labels: trino_goway_proxy_requests_total and trino_goway_backend_status.
func TestE2E_Metrics_AppFamilies(t *testing.T) {
	h := newHarnessNoLeaks(t, harness.WithMonitorInterval(1*time.Second))

	// Register a backend so backend_status has a series and the proxy has a target.
	h.AddBackend(t, "trino-1", "default")

	// Drive a proxied request so proxy_requests_total increments.
	req, err := http.NewRequest(http.MethodPost, h.ProxyURL+"/v1/statement", strings.NewReader("SELECT 1"))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("X-Trino-User", "metrics-e2e")
	resp, err := h.ProxyClient().Do(req)
	require.NoError(t, err, "POST /v1/statement through proxy")
	_, _ = io.Copy(io.Discard, resp.Body)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode, "proxied statement must succeed")

	// Poll the scrape until both app families are present. backend_status is
	// driven by the monitor's view of the active backend set, which the gateway
	// reloads from the DB on its backend-refresh cycle (~15s) — independent of the
	// admin upsert that AddBackend waited on. So the deadline must comfortably
	// exceed one refresh interval. proxy_requests_total appears immediately after
	// the proxied request; backend_status appears once the first post-refresh probe
	// observes the backend.
	var families map[string]*dto.MetricFamily
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		families = scrapeFamilies(t, h.AdminURL)
		_, hasProxy := families["trino_goway_proxy_requests_total"]
		_, hasBackend := families["trino_goway_backend_status"]
		if hasProxy && hasBackend {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	require.Contains(t, families, "trino_goway_proxy_requests_total",
		"proxy_requests_total must appear after a proxied request")
	require.Contains(t, families, "trino_goway_backend_status",
		"backend_status must appear after a backend is registered and probed")

	// proxy_requests_total must carry the backend, routing_group, and outcome labels.
	prt := families["trino_goway_proxy_requests_total"]
	require.NotEmpty(t, prt.GetMetric(), "proxy_requests_total must have at least one series")
	series := prt.GetMetric()[0]
	labelNames := map[string]bool{}
	for _, l := range series.GetLabel() {
		labelNames[l.GetName()] = true
	}
	assert.True(t, labelNames["backend"], "proxy_requests_total must have a backend label")
	assert.True(t, labelNames["routing_group"], "proxy_requests_total must have a routing_group label")
	assert.True(t, labelNames["outcome"], "proxy_requests_total must have an outcome label")

	// backend_status must expose the healthy/unhealthy/pending status label set.
	bs := families["trino_goway_backend_status"]
	hasStatusLabel := false
	for _, m := range bs.GetMetric() {
		for _, l := range m.GetLabel() {
			if l.GetName() == "status" {
				hasStatusLabel = true
			}
		}
	}
	assert.True(t, hasStatusLabel, "backend_status must carry a status label")
}

// TestE2E_Metrics_Disabled verifies that with metrics.enabled=false the /metrics
// route is not registered and returns 404.
func TestE2E_Metrics_Disabled(t *testing.T) {
	h := newHarnessNoLeaks(t, harness.WithMetricsDisabled())

	resp, err := http.Get(h.AdminURL + "/metrics")
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, http.StatusNotFound, resp.StatusCode,
		"/metrics must return 404 when metrics.enabled=false")
}
