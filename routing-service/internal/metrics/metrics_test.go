package metrics_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"go.uber.org/goleak"

	"github.com/hclincode/trino-goway-routing-service/internal/metrics"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// counterValue gathers the registry and returns the value of the named counter
// with the given labels, or 0 if absent.
func counterValue(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if labelsMatch(m, labels) {
				switch {
				case m.GetCounter() != nil:
					return m.GetCounter().GetValue()
				case m.GetGauge() != nil:
					return m.GetGauge().GetValue()
				}
			}
		}
	}
	return 0
}

// histogramSampleCount returns the observation count for the named histogram
// with the given labels.
func histogramSampleCount(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) uint64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if labelsMatch(m, labels) && m.GetHistogram() != nil {
				return m.GetHistogram().GetSampleCount()
			}
		}
	}
	return 0
}

func labelsMatch(m *dto.Metric, want map[string]string) bool {
	got := make(map[string]string, len(m.GetLabel()))
	for _, l := range m.GetLabel() {
		got[l.GetName()] = l.GetValue()
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

func TestRecordDecision_DecidedCounts(t *testing.T) {
	m := metrics.New()
	for i := 0; i < 10; i++ {
		m.RecordDecision("airflow", "etl", "expr", metrics.OutcomeDecided, time.Millisecond)
	}
	got := counterValue(t, m.Registry(), "routing_service_requests_total", map[string]string{
		"source": "airflow", "routing_group": "etl", "method_type": "expr", "outcome": "decided",
	})
	if got != 10 {
		t.Errorf("decided count = %v, want 10", got)
	}
	// Histogram observed under method_type=expr.
	if n := histogramSampleCount(t, m.Registry(), "routing_service_decision_duration_seconds",
		map[string]string{"method_type": "expr"}); n != 10 {
		t.Errorf("histogram sample count = %d, want 10", n)
	}
}

func TestRecordDecision_FallbackCountsBoth(t *testing.T) {
	m := metrics.New()
	for i := 0; i < 5; i++ {
		m.RecordDecision("superset", "default", "", metrics.OutcomeFallback, time.Millisecond)
	}
	if got := counterValue(t, m.Registry(), "routing_service_fallback_total", nil); got != 5 {
		t.Errorf("fallback_total = %v, want 5", got)
	}
	if got := counterValue(t, m.Registry(), "routing_service_requests_total", map[string]string{
		"outcome": "fallback",
	}); got != 5 {
		t.Errorf("requests_total{outcome=fallback} = %v, want 5", got)
	}
}

func TestRecordDecision_ErrorNotCountedAsFallback(t *testing.T) {
	m := metrics.New()
	for i := 0; i < 3; i++ {
		m.RecordDecision("dbt", "default", "", metrics.OutcomeError, time.Millisecond)
	}
	if got := counterValue(t, m.Registry(), "routing_service_requests_total", map[string]string{
		"outcome": "error",
	}); got != 3 {
		t.Errorf("requests_total{outcome=error} = %v, want 3", got)
	}
	// Errors must NOT increment the fallback counter.
	if got := counterValue(t, m.Registry(), "routing_service_fallback_total", nil); got != 0 {
		t.Errorf("fallback_total = %v, want 0 (errors are not fallbacks)", got)
	}
}

func TestRecordReload(t *testing.T) {
	m := metrics.New()
	m.RecordReload(metrics.ReloadOK)
	m.RecordReload(metrics.ReloadOK)
	m.RecordReload(metrics.ReloadError)
	if got := counterValue(t, m.Registry(), "routing_service_config_reload_total", map[string]string{"result": "ok"}); got != 2 {
		t.Errorf("reload ok = %v, want 2", got)
	}
	if got := counterValue(t, m.Registry(), "routing_service_config_reload_total", map[string]string{"result": "error"}); got != 1 {
		t.Errorf("reload error = %v, want 1", got)
	}
}

func TestRecordSQLParse(t *testing.T) {
	m := metrics.New()
	m.RecordSQLParse(metrics.SQLParseOK, 50*time.Microsecond, false)
	m.RecordSQLParse(metrics.SQLParseOK, 60*time.Microsecond, true) // truncated
	m.RecordSQLParse(metrics.SQLParseEmpty, 10*time.Microsecond, false)

	if got := counterValue(t, m.Registry(), "routing_service_sql_parse_total", map[string]string{"result": "ok"}); got != 2 {
		t.Errorf("sql_parse ok = %v, want 2", got)
	}
	if got := counterValue(t, m.Registry(), "routing_service_sql_parse_total", map[string]string{"result": "empty"}); got != 1 {
		t.Errorf("sql_parse empty = %v, want 1", got)
	}
	if got := counterValue(t, m.Registry(), "routing_service_sql_parse_truncated_total", nil); got != 1 {
		t.Errorf("sql_parse truncated = %v, want 1", got)
	}
	if got := histogramSampleCount(t, m.Registry(), "routing_service_sql_parse_duration_seconds", nil); got != 3 {
		t.Errorf("sql_parse duration count = %v, want 3", got)
	}
}

func TestMethodDisabledGauge(t *testing.T) {
	m := metrics.New()
	m.SetMethodDisabled("expr", true)
	if got := counterValue(t, m.Registry(), "routing_service_method_disabled", map[string]string{"type": "expr"}); got != 1 {
		t.Errorf("method_disabled{expr} = %v, want 1", got)
	}
	m.SetMethodDisabled("expr", false)
	if got := counterValue(t, m.Registry(), "routing_service_method_disabled", map[string]string{"type": "expr"}); got != 0 {
		t.Errorf("method_disabled{expr} = %v, want 0 after enable", got)
	}
}

func TestSyncDisabled(t *testing.T) {
	m := metrics.New()
	m.SyncDisabled([]string{"expr", "script"}, []string{"script"})
	if got := counterValue(t, m.Registry(), "routing_service_method_disabled", map[string]string{"type": "script"}); got != 1 {
		t.Errorf("script disabled = %v, want 1", got)
	}
	if got := counterValue(t, m.Registry(), "routing_service_method_disabled", map[string]string{"type": "expr"}); got != 0 {
		t.Errorf("expr disabled = %v, want 0", got)
	}
}

func TestSetConfigVersion_SingleActiveHash(t *testing.T) {
	m := metrics.New()
	m.SetConfigVersion("aaaa1111")
	m.SetConfigVersion("bbbb2222") // supersedes the first
	if got := counterValue(t, m.Registry(), "routing_service_config_version", map[string]string{"hash": "bbbb2222"}); got != 1 {
		t.Errorf("config_version{bbbb2222} = %v, want 1", got)
	}
	// The previous hash series must be gone (Reset), not lingering at 1.
	if got := counterValue(t, m.Registry(), "routing_service_config_version", map[string]string{"hash": "aaaa1111"}); got != 0 {
		t.Errorf("config_version{aaaa1111} = %v, want 0 (reset)", got)
	}
}

func TestMetricsEndpoint_ServesOpenMetrics(t *testing.T) {
	m := metrics.New()
	m.RecordDecision("airflow", "etl", "expr", metrics.OutcomeDecided, time.Millisecond)

	srv := metrics.NewServer(m)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.StartOnListener(ctx, lis); err != nil {
			t.Errorf("metrics server: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	resp, err := http.Get("http://" + lis.Addr().String() + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	// promhttp negotiates OpenMetrics or Prometheus text; either is acceptable.
	if !strings.Contains(ct, "openmetrics-text") && !strings.Contains(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want OpenMetrics or text/plain", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	// The exposition body is produced by promhttp from this registry. Confirm it
	// is well-formed: it carries our metric and (for OpenMetrics) the # EOF
	// terminator, and parses into the same family set the registry gathers.
	text := string(body)
	if !strings.Contains(text, "routing_service_requests_total") {
		t.Fatalf("/metrics body missing routing_service_requests_total:\n%s", text)
	}
	if strings.Contains(ct, "openmetrics-text") && !strings.Contains(text, "# EOF") {
		t.Errorf("OpenMetrics body missing # EOF terminator")
	}
	// Cross-check: the registry gathers a non-empty, consistent family set.
	mfs, err := m.Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	if len(mfs) == 0 {
		t.Fatal("registry gathered zero metric families")
	}
}
