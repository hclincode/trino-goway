package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration and implements yaml.Unmarshaler to accept Go duration strings.
type Duration struct {
	D time.Duration
}

// UnmarshalYAML parses a Go duration string such as "10s", "1m", "1h".
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("config: duration: decode string: %w", err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("config: duration: parse %q: %w", s, err)
	}
	d.D = parsed
	return nil
}

// dataSizePattern matches a number followed by an optional unit suffix.
var dataSizePattern = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*([A-Za-z]*)$`)

// DataSize wraps int64 (bytes) and implements yaml.Unmarshaler.
// Accepted units: B, KB, KiB, MB, MiB, GB, GiB.
type DataSize struct {
	Bytes int64
}

// UnmarshalYAML parses size strings such as "512B", "1KiB", "1MiB".
func (ds *DataSize) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("config: dataSize: decode string: %w", err)
	}
	m := dataSizePattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return fmt.Errorf("config: dataSize: invalid format %q", s)
	}
	num, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return fmt.Errorf("config: dataSize: parse number %q: %w", m[1], err)
	}
	unit := m[2]
	var multiplier int64
	switch unit {
	case "", "B":
		multiplier = 1
	case "KB":
		multiplier = 1_000
	case "KiB":
		multiplier = 1_024
	case "MB":
		multiplier = 1_000_000
	case "MiB":
		multiplier = 1_048_576
	case "GB":
		multiplier = 1_000_000_000
	case "GiB":
		multiplier = 1_073_741_824
	default:
		return fmt.Errorf("config: dataSize: unknown unit %q", unit)
	}
	ds.Bytes = int64(num * float64(multiplier))
	return nil
}

// Config is the top-level gateway configuration.
type Config struct {
	Proxy   ProxyConfig   `yaml:"proxy"`
	Admin   AdminConfig   `yaml:"admin"`
	Monitor MonitorConfig `yaml:"monitor"`
	DB      DBConfig      `yaml:"db"`
	Routing RoutingConfig `yaml:"routing"`
	Auth    AuthConfig    `yaml:"auth"`
	Cookie  CookieConfig  `yaml:"cookie"`
	Metrics MetricsConfig `yaml:"metrics"`
	UI      UIConfig      `yaml:"ui"`
}

// UIConfig holds web-UI feature flags surfaced by getUIConfiguration.
type UIConfig struct {
	// DisablePages lists page keys (e.g. "dashboard", "cluster", "history",
	// "routingRules") globally hidden from the UI sidebar, mirroring Java's
	// uiConfiguration.disablePages.
	DisablePages []string `yaml:"disablePages"`
}

// ProxyConfig holds configuration for the proxy listener.
type ProxyConfig struct {
	Port            int      `yaml:"port"`            // default 8080
	ResponseSize    DataSize `yaml:"responseSize"`    // default 1MiB; max body to buffer on POST /v1/statement
	RequestTimeout  Duration `yaml:"requestTimeout"`  // default 30s
	PropagateErrors bool     `yaml:"propagateErrors"` // when true, non-empty routing errors return HTTP 400 instead of falling back
}

// AdminConfig holds configuration for the admin listener.
type AdminConfig struct {
	Port int `yaml:"port"` // default 8090; must != Proxy.Port
}

// MonitorConfig holds configuration for the backend health monitor.
type MonitorConfig struct {
	Interval     Duration `yaml:"interval"`     // default 30s
	CheckTimeout Duration `yaml:"checkTimeout"` // default 5s
}

// DBConfig holds database connection configuration.
type DBConfig struct {
	Driver string `yaml:"driver"` // "postgres" or "mysql"
	DSN    string `yaml:"dsn"`
}

// RoutingConfig holds routing configuration.
type RoutingConfig struct {
	DefaultGroup string         `yaml:"defaultGroup"` // fallback routing group
	Type         string         `yaml:"type"`         // "EXTERNAL" (only supported type)
	External     ExternalConfig `yaml:"external"`
}

// ExternalConfig holds configuration for the external routing transport.
type ExternalConfig struct {
	URL            string   `yaml:"url"`            // HTTP transport URL
	GRPCAddr       string   `yaml:"grpcAddr"`       // gRPC address (host:port)
	Timeout        Duration `yaml:"timeout"`        // default 1s
	ExcludeHeaders []string `yaml:"excludeHeaders"` // headers stripped from routing requests and external-header responses
}

// AuthConfig holds authentication configuration.
type AuthConfig struct {
	Type          string              `yaml:"type"` // "OIDC", "LDAP", "NOOP"
	OIDC          OIDCConfig          `yaml:"oidc"`
	LDAP          LDAPConfig          `yaml:"ldap"`
	Authorization AuthorizationConfig `yaml:"authorization"`
}

// AuthorizationConfig holds regex patterns for role resolution.
// Each field is a Java-compatible regex matched against the principal's memberOf string.
type AuthorizationConfig struct {
	AdminRegex string `yaml:"admin"` // regex for ADMIN role
	UserRegex  string `yaml:"user"`  // regex for USER role
	APIRegex   string `yaml:"api"`   // regex for API role

	// PagePermissions maps a role name (ADMIN/USER/API) to an underscore-separated
	// list of UI page keys that role may see (e.g. "dashboard_cluster_history").
	// Mirrors Java's pagePermissions. The resolved per-user union is returned in
	// /userinfo's permissions; a role with no entry grants all pages.
	PagePermissions map[string]string `yaml:"pagePermissions"`
}

// OIDCConfig holds OpenID Connect authentication configuration.
type OIDCConfig struct {
	IssuerURL    string   `yaml:"issuerUrl"`
	ClientID     string   `yaml:"clientId"`
	ClientSecret string   `yaml:"clientSecret"`
	JWKSURL      string   `yaml:"jwksUrl"`
	JWKSTTLSecs  int      `yaml:"jwksTtlSecs"` // default 300
	Scopes       []string `yaml:"scopes"`

	// RedirectURL is the absolute URL of the gateway's /oidc/callback endpoint,
	// registered with the IdP as an allowed redirect URI. Required for the
	// interactive Web-UI login (authorization-code) flow.
	RedirectURL string `yaml:"redirectUrl"`

	// AuthorizationEndpoint and TokenEndpoint override OIDC discovery. When empty,
	// they are resolved from {issuerUrl}/.well-known/openid-configuration.
	AuthorizationEndpoint string `yaml:"authorizationEndpoint"`
	TokenEndpoint         string `yaml:"tokenEndpoint"`
}

// LDAPConfig holds LDAP authentication configuration.
type LDAPConfig struct {
	URL      string `yaml:"url"`
	BindDN   string `yaml:"bindDn"`
	BindPass string `yaml:"bindPassword"`
	UserBase string `yaml:"userBase"`
	UserAttr string `yaml:"userAttr"` // default "uid"
}

// MetricsConfig holds Prometheus metrics exposition configuration.
type MetricsConfig struct {
	Enabled bool   `yaml:"enabled"` // default true; when false the /metrics route is not registered (404)
	Path    string `yaml:"path"`    // default "/metrics"
}

// CookieConfig holds session cookie configuration.
type CookieConfig struct {
	Secret     string   `yaml:"secret"`
	TTL        Duration `yaml:"ttl"`        // default 10m
	WireCompat bool     `yaml:"wireCompat"` // default true
}

// Load reads a YAML config file at path, applies defaults, and validates the result.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}
	cfg := defaultConfig()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("config: unmarshal %q: %w", path, err)
	}
	applyDefaults(cfg)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// defaultConfig returns a Config pre-filled with all default values.
func defaultConfig() *Config {
	return &Config{
		Proxy: ProxyConfig{
			Port:           8080,
			ResponseSize:   DataSize{Bytes: 1_048_576},
			RequestTimeout: Duration{D: 30 * time.Second},
		},
		Admin: AdminConfig{
			Port: 8090,
		},
		Monitor: MonitorConfig{
			Interval:     Duration{D: 30 * time.Second},
			CheckTimeout: Duration{D: 5 * time.Second},
		},
		Routing: RoutingConfig{
			Type: "EXTERNAL",
			External: ExternalConfig{
				Timeout: Duration{D: 1 * time.Second},
			},
		},
		Auth: AuthConfig{
			Type: "NOOP",
			OIDC: OIDCConfig{
				JWKSTTLSecs: 300,
			},
			LDAP: LDAPConfig{
				UserAttr: "uid",
			},
		},
		Cookie: CookieConfig{
			TTL:        Duration{D: 10 * time.Minute},
			WireCompat: true,
		},
		Metrics: MetricsConfig{
			Enabled: true,
			Path:    "/metrics",
		},
	}
}

// applyDefaults fills zero-value fields with their defaults after YAML unmarshaling.
// YAML unmarshaling merges into defaultConfig(), so zero values mean the user omitted the field.
func applyDefaults(cfg *Config) {
	if cfg.Proxy.Port == 0 {
		cfg.Proxy.Port = 8080
	}
	if cfg.Admin.Port == 0 {
		cfg.Admin.Port = 8090
	}
	if cfg.Monitor.Interval.D == 0 {
		cfg.Monitor.Interval.D = 30 * time.Second
	}
	if cfg.Monitor.CheckTimeout.D == 0 {
		cfg.Monitor.CheckTimeout.D = 5 * time.Second
	}
	if cfg.Proxy.RequestTimeout.D == 0 {
		cfg.Proxy.RequestTimeout.D = 30 * time.Second
	}
	if cfg.Proxy.ResponseSize.Bytes == 0 {
		cfg.Proxy.ResponseSize.Bytes = 1_048_576
	}
	if cfg.Routing.External.Timeout.D == 0 {
		cfg.Routing.External.Timeout.D = 1 * time.Second
	}
	if cfg.Cookie.TTL.D == 0 {
		cfg.Cookie.TTL.D = 10 * time.Minute
	}
	// WireCompat defaults to true via defaultConfig(); yaml.Unmarshal only overwrites it
	// when the key is explicitly present in the YAML file, so the default is preserved
	// when the user omits the field.
	if cfg.Auth.Type == "" {
		cfg.Auth.Type = "NOOP"
	}
	if cfg.Routing.Type == "" {
		cfg.Routing.Type = "EXTERNAL"
	}
	if cfg.Auth.LDAP.UserAttr == "" {
		cfg.Auth.LDAP.UserAttr = "uid"
	}
	if cfg.Auth.OIDC.JWKSTTLSecs == 0 {
		cfg.Auth.OIDC.JWKSTTLSecs = 300
	}
	// Metrics.Enabled defaults to true via defaultConfig(); yaml.Unmarshal only
	// overwrites it when the key is explicitly present, so the default is preserved
	// when the user omits the field.
	if cfg.Metrics.Path == "" {
		cfg.Metrics.Path = "/metrics"
	}
}

// Validate checks the configuration for logical errors.
func (c *Config) Validate() error {
	if c.Proxy.Port == c.Admin.Port {
		return fmt.Errorf("config: validate: proxy.port and admin.port must differ, both are %d", c.Proxy.Port)
	}
	if c.Proxy.ResponseSize.Bytes <= 0 {
		return fmt.Errorf("config: validate: proxy.responseSize must be > 0, got %d", c.Proxy.ResponseSize.Bytes)
	}
	if c.DB.Driver != "" {
		if c.DB.Driver != "postgres" && c.DB.Driver != "mysql" {
			return fmt.Errorf("config: validate: db.driver must be \"postgres\" or \"mysql\", got %q", c.DB.Driver)
		}
	}
	if c.Routing.Type != "EXTERNAL" {
		return fmt.Errorf("config: validate: routing.type must be \"EXTERNAL\", got %q", c.Routing.Type)
	}
	if c.Auth.Type == "OIDC" {
		if c.Auth.OIDC.JWKSURL == "" {
			return fmt.Errorf("config: validate: auth.oidc.jwksUrl must be non-empty when auth.type is OIDC")
		}
	}
	if c.Metrics.Enabled {
		if !strings.HasPrefix(c.Metrics.Path, "/") {
			return fmt.Errorf("config: validate: metrics.path must start with \"/\", got %q", c.Metrics.Path)
		}
	}
	if c.Auth.Type == "LDAP" {
		if c.Auth.LDAP.URL == "" {
			return fmt.Errorf("config: validate: auth.ldap.url must be non-empty when auth.type is LDAP")
		}
		if c.Auth.LDAP.UserBase == "" {
			return fmt.Errorf("config: validate: auth.ldap.userBase must be non-empty when auth.type is LDAP")
		}
	}
	return nil
}
