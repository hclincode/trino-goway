package clusterstats

import (
	"net/http"
	"sync/atomic"
	"time"

	"github.com/hclincode/trino-goway/internal/clusterstatus"
	"github.com/hclincode/trino-goway/internal/config"
)

// fakeBackend is a static clusterstats.Backend for collector tests.
type fakeBackend struct {
	name, url, externalURL, routingGroup string
}

func (b fakeBackend) GetName() string         { return b.name }
func (b fakeBackend) GetURL() string          { return b.url }
func (b fakeBackend) GetExternalURL() string  { return b.externalURL }
func (b fakeBackend) GetRoutingGroup() string { return b.routingGroup }

// newBackend builds a fakeBackend whose proxyTo (GetURL) is the given server URL
// and whose persistence-derived identity fields are populated so tests can assert
// statsBuilder propagation.
func newBackend(name, url string) fakeBackend {
	return fakeBackend{
		name:         name,
		url:          url,
		externalURL:  "https://external.example/" + name,
		routingGroup: "adhoc",
	}
}

// staticStatus returns a StatusFunc that always reports s, ignoring the URL. Used
// to drive the infoapi/noop collectors' verdict without any HTTP.
func staticStatus(s clusterstatus.Status) StatusFunc {
	return func(string) clusterstatus.Status { return s }
}

// countingTransport is an http.RoundTripper that counts every request it sees and
// fails the round-trip. A collector wired to a client using this transport that
// issues NO HTTP leaves Calls() at 0; any stray request both increments the count
// and surfaces as a transport error (so the assertion can't be silently bypassed).
type countingTransport struct {
	calls atomic.Int64
}

func (t *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return nil, errNoHTTPExpected
}

func (t *countingTransport) Calls() int64 { return t.calls.Load() }

// errNoHTTPExpected is returned by countingTransport so a collector that
// unexpectedly issues HTTP sees a transport error rather than a real response.
var errNoHTTPExpected = &noHTTPError{}

type noHTTPError struct{}

func (*noHTTPError) Error() string { return "clusterstats test: no HTTP expected from this collector" }

// recordingClient returns an *http.Client backed by a countingTransport plus the
// transport itself so the test can assert the call count.
func recordingClient() (*http.Client, *countingTransport) {
	tr := &countingTransport{}
	return &http.Client{Transport: tr}, tr
}

// uiBackendStateConfig builds a BackendStateConfig for the UI_API collector tests.
func uiBackendStateConfig(username, password string, xForwarded bool) config.BackendStateConfig {
	return config.BackendStateConfig{
		Username:              username,
		Password:              password,
		XForwardedProtoHeader: xForwarded,
	}
}

// clusterStatsCfg builds a ClusterStatsConfig selecting the given monitor type.
func clusterStatsCfg(monitorType string) config.ClusterStatsConfig {
	return config.ClusterStatsConfig{MonitorType: monitorType}
}

// backendStateCfg returns an empty BackendStateConfig (INFO_API/NOOP need none).
func backendStateCfg() config.BackendStateConfig { return config.BackendStateConfig{} }

// monitorCfg returns a MonitorConfig with the Java-default stats knobs, mirroring
// config.applyDefaults so the METRICS collector resolves its metric names.
func monitorCfg() config.MonitorConfig {
	return config.MonitorConfig{
		StatsTimeout:             config.Duration{D: 10 * time.Second},
		Retries:                  0,
		MetricsEndpoint:          "/metrics",
		RunningQueriesMetricName: "trino_execution_name_QueryManager_RunningQueries",
		QueuedQueriesMetricName:  "trino_execution_name_QueryManager_QueuedQueries",
		MetricMinimumValues:      map[string]float64{"trino_metadata_name_DiscoveryNodeManager_ActiveNodeCount": 1},
		MetricMaximumValues:      map[string]float64{},
	}
}
