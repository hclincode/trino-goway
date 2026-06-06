package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/hclincode/trino-goway/internal/config"
)

func TestDuration_UnmarshalYAML(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{name: "10s", input: "10s", want: 10 * time.Second},
		{name: "1m", input: "1m", want: time.Minute},
		{name: "1h", input: "1h", want: time.Hour},
		{name: "invalid", input: "invalid", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			type wrapper struct {
				D config.Duration `yaml:"d"`
			}
			var w wrapper
			err := yaml.Unmarshal([]byte("d: "+tc.input+"\n"), &w)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, w.D.D)
		})
	}
}

func TestDataSize_UnmarshalYAML(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		input   string
		want    int64
		wantErr bool
	}{
		{name: "1KiB", input: "1KiB", want: 1024},
		{name: "1MiB", input: "1MiB", want: 1_048_576},
		{name: "512KB", input: "512KB", want: 512_000},
		{name: "invalid", input: "invalid", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			type wrapper struct {
				S config.DataSize `yaml:"s"`
			}
			var w wrapper
			err := yaml.Unmarshal([]byte("s: "+tc.input+"\n"), &w)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, w.S.Bytes)
		})
	}
}

func TestLoad_ValidConfig(t *testing.T) {
	t.Parallel()
	content := `
proxy:
  port: 8080
  responseSize: 1MiB
  requestTimeout: 30s
admin:
  port: 8090
monitor:
  interval: 30s
  checkTimeout: 5s
db:
  driver: postgres
  dsn: "postgres://localhost/trino"
routing:
  type: EXTERNAL
  defaultGroup: default
  external:
    url: "http://router:8080"
    grpcAddr: "router:9090"
    timeout: 1s
auth:
  type: NOOP
cookie:
  secret: "supersecret"
  ttl: 10m
  wireCompat: true
`
	path := writeTempFile(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, 8080, cfg.Proxy.Port)
	assert.Equal(t, 8090, cfg.Admin.Port)
	assert.Equal(t, int64(1_048_576), cfg.Proxy.ResponseSize.Bytes)
	assert.Equal(t, 30*time.Second, cfg.Proxy.RequestTimeout.D)
	assert.Equal(t, 30*time.Second, cfg.Monitor.Interval.D)
	assert.Equal(t, 5*time.Second, cfg.Monitor.CheckTimeout.D)
	assert.Equal(t, "postgres", cfg.DB.Driver)
	assert.Equal(t, "EXTERNAL", cfg.Routing.Type)
	assert.Equal(t, "NOOP", cfg.Auth.Type)
	assert.Equal(t, "supersecret", cfg.Cookie.Secret)
	assert.Equal(t, 10*time.Minute, cfg.Cookie.TTL.D)
	assert.True(t, cfg.Cookie.WireCompat)
}

func TestLoad_Defaults(t *testing.T) {
	t.Parallel()
	// Minimal config — all defaultable fields omitted.
	content := `
routing:
  type: EXTERNAL
`
	path := writeTempFile(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, 8080, cfg.Proxy.Port)
	assert.Equal(t, 8090, cfg.Admin.Port)
	assert.Equal(t, int64(1_048_576), cfg.Proxy.ResponseSize.Bytes)
	assert.Equal(t, 30*time.Second, cfg.Proxy.RequestTimeout.D)
	assert.Equal(t, 30*time.Second, cfg.Monitor.Interval.D)
	assert.Equal(t, 5*time.Second, cfg.Monitor.CheckTimeout.D)
	assert.Equal(t, 15*time.Second, cfg.Monitor.RefreshInterval.D)
	assert.Equal(t, 1*time.Second, cfg.Routing.External.Timeout.D)
	assert.Equal(t, 10*time.Minute, cfg.Cookie.TTL.D)
	assert.True(t, cfg.Cookie.WireCompat)
	assert.Equal(t, "NOOP", cfg.Auth.Type)
	assert.Equal(t, "EXTERNAL", cfg.Routing.Type)
	assert.Equal(t, "uid", cfg.Auth.LDAP.UserAttr)
	assert.Equal(t, 300, cfg.Auth.OIDC.JWKSTTLSecs)
	assert.True(t, cfg.Metrics.Enabled)
	assert.Equal(t, "/metrics", cfg.Metrics.Path)
}

func TestLoad_MetricsDisabled(t *testing.T) {
	t.Parallel()
	content := `
routing:
  type: EXTERNAL
metrics:
  enabled: false
`
	path := writeTempFile(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.False(t, cfg.Metrics.Enabled)
	// Path default still applies even when disabled.
	assert.Equal(t, "/metrics", cfg.Metrics.Path)
}

func TestValidate_MetricsPathMustStartWithSlash(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Proxy:   config.ProxyConfig{Port: 8080, ResponseSize: config.DataSize{Bytes: 1}},
		Admin:   config.AdminConfig{Port: 8090},
		Routing: config.RoutingConfig{Type: "EXTERNAL"},
		Auth:    config.AuthConfig{Type: "NOOP"},
		Metrics: config.MetricsConfig{Enabled: true, Path: "metrics"},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metrics.path must start with")
}

func TestValidate_AdminPortEqualsProxyPort(t *testing.T) {
	t.Parallel()
	content := `
proxy:
  port: 8080
admin:
  port: 8080
routing:
  type: EXTERNAL
`
	path := writeTempFile(t, content)
	_, err := config.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "proxy.port and admin.port must differ")
}

func TestValidate_ResponseSizeZero(t *testing.T) {
	t.Parallel()
	// We can't set responseSize to 0 via YAML since 0B is a valid parse but
	// validate should catch it. Use a negative-equivalent: we test Validate directly.
	cfg := &config.Config{
		Proxy: config.ProxyConfig{
			Port:         8080,
			ResponseSize: config.DataSize{Bytes: 0},
		},
		Admin: config.AdminConfig{Port: 8090},
		Routing: config.RoutingConfig{
			Type: "EXTERNAL",
		},
		Auth: config.AuthConfig{Type: "NOOP"},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "proxy.responseSize must be > 0")
}

func TestValidate_InvalidDBDriver(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Proxy:   config.ProxyConfig{Port: 8080, ResponseSize: config.DataSize{Bytes: 1}},
		Admin:   config.AdminConfig{Port: 8090},
		Routing: config.RoutingConfig{Type: "EXTERNAL"},
		Auth:    config.AuthConfig{Type: "NOOP"},
		DB:      config.DBConfig{Driver: "sqlite"},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "db.driver must be")
}

func TestValidate_OIDCMissingJWKSURL(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Proxy:   config.ProxyConfig{Port: 8080, ResponseSize: config.DataSize{Bytes: 1}},
		Admin:   config.AdminConfig{Port: 8090},
		Routing: config.RoutingConfig{Type: "EXTERNAL"},
		Auth:    config.AuthConfig{Type: "OIDC"},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth.oidc.jwksUrl")
}

func TestValidate_LDAPMissingURL(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Proxy:   config.ProxyConfig{Port: 8080, ResponseSize: config.DataSize{Bytes: 1}},
		Admin:   config.AdminConfig{Port: 8090},
		Routing: config.RoutingConfig{Type: "EXTERNAL"},
		Auth:    config.AuthConfig{Type: "LDAP", LDAP: config.LDAPConfig{UserBase: "dc=example,dc=com"}},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth.ldap.url")
}

func TestConfig_ClusterStats_DefaultsToInfoAPI(t *testing.T) {
	t.Parallel()
	content := `
routing:
  type: EXTERNAL
`
	path := writeTempFile(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "INFO_API", cfg.ClusterStats.MonitorType)
	// Stats knobs seeded with Java-value defaults.
	assert.Equal(t, 10*time.Second, cfg.Monitor.StatsTimeout.D)
	assert.Equal(t, 0, cfg.Monitor.Retries)
}

func TestConfig_ClusterStats_MetricNameDefaults(t *testing.T) {
	t.Parallel()
	content := `
routing:
  type: EXTERNAL
`
	path := writeTempFile(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "/metrics", cfg.Monitor.MetricsEndpoint)
	assert.Equal(t, "trino_execution_name_QueryManager_RunningQueries", cfg.Monitor.RunningQueriesMetricName)
	assert.Equal(t, "trino_execution_name_QueryManager_QueuedQueries", cfg.Monitor.QueuedQueriesMetricName)
	assert.Equal(t, map[string]float64{"trino_metadata_name_DiscoveryNodeManager_ActiveNodeCount": 1}, cfg.Monitor.MetricMinimumValues)
	assert.Empty(t, cfg.Monitor.MetricMaximumValues)
}

func TestConfig_ClusterStats_RoundTrip(t *testing.T) {
	t.Parallel()
	content := `
routing:
  type: EXTERNAL
clusterStats:
  monitorType: UI_API
backendState:
  username: admin
  password: secret
  ssl: true
  xForwardedProtoHeader: true
monitor:
  statsTimeout: 7s
  retries: 2
`
	path := writeTempFile(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "UI_API", cfg.ClusterStats.MonitorType)
	assert.Equal(t, "admin", cfg.BackendState.Username)
	assert.Equal(t, "secret", cfg.BackendState.Password)
	assert.True(t, cfg.BackendState.SSL)
	assert.True(t, cfg.BackendState.XForwardedProtoHeader)
	assert.Equal(t, 7*time.Second, cfg.Monitor.StatsTimeout.D)
	assert.Equal(t, 2, cfg.Monitor.Retries)
}

func TestConfig_ClusterStats_BackendStateRequired(t *testing.T) {
	t.Parallel()
	for _, mt := range []string{"UI_API", "METRICS"} {
		t.Run(mt+"_without_backendState_rejected", func(t *testing.T) {
			t.Parallel()
			content := "routing:\n  type: EXTERNAL\nclusterStats:\n  monitorType: " + mt + "\n"
			path := writeTempFile(t, content)
			_, err := config.Load(path)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "backendState.username must be non-empty")
		})
	}
	for _, mt := range []string{"INFO_API", "NOOP"} {
		t.Run(mt+"_without_backendState_accepted", func(t *testing.T) {
			t.Parallel()
			content := "routing:\n  type: EXTERNAL\nclusterStats:\n  monitorType: " + mt + "\n"
			path := writeTempFile(t, content)
			_, err := config.Load(path)
			require.NoError(t, err)
		})
	}
}

func TestConfig_ClusterStats_RejectsJDBC(t *testing.T) {
	t.Parallel()
	content := `
routing:
  type: EXTERNAL
clusterStats:
  monitorType: JDBC
backendState:
  username: admin
`
	path := writeTempFile(t, content)
	_, err := config.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"JDBC" not supported in v1`)
}

func TestConfig_ClusterStats_RejectsJMX(t *testing.T) {
	t.Parallel()
	content := `
routing:
  type: EXTERNAL
clusterStats:
  monitorType: JMX
backendState:
  username: admin
`
	path := writeTempFile(t, content)
	_, err := config.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"JMX" not supported in v1`)
}

func TestConfig_ClusterStats_UnknownType(t *testing.T) {
	t.Parallel()
	content := `
routing:
  type: EXTERNAL
clusterStats:
  monitorType: BOGUS
`
	path := writeTempFile(t, content)
	_, err := config.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "clusterStats.monitorType must be one of")
}

func TestConfig_ClusterStats_ExplicitEmptyMinimumsPreserved(t *testing.T) {
	t.Parallel()
	// An explicit empty map disables the minimum gate and is NOT re-seeded with
	// the ActiveNodeCount default.
	content := `
routing:
  type: EXTERNAL
monitor:
  metricMinimumValues: {}
`
	path := writeTempFile(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.NotNil(t, cfg.Monitor.MetricMinimumValues)
	assert.Empty(t, cfg.Monitor.MetricMinimumValues)
}

// writeTempFile creates a temp file with the given content and registers cleanup.
func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}
