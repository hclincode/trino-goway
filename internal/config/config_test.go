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
	assert.Equal(t, 1*time.Second, cfg.Routing.External.Timeout.D)
	assert.Equal(t, 10*time.Minute, cfg.Cookie.TTL.D)
	assert.True(t, cfg.Cookie.WireCompat)
	assert.Equal(t, "NOOP", cfg.Auth.Type)
	assert.Equal(t, "EXTERNAL", cfg.Routing.Type)
	assert.Equal(t, "uid", cfg.Auth.LDAP.UserAttr)
	assert.Equal(t, 300, cfg.Auth.OIDC.JWKSTTLSecs)
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

// writeTempFile creates a temp file with the given content and registers cleanup.
func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}
