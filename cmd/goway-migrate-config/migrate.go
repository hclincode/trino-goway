package main

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/hclincode/trino-goway/internal/config"
)

// javaConfig is the top-level structure of a Java trino-gateway config.yml.
type javaConfig struct {
	RequestRouter      javaRequestRouter      `yaml:"requestRouter"`
	DataStore          javaDataStore          `yaml:"dataStore"`
	BackendState       javaBackendState       `yaml:"backendState"`
	Routing            javaRouting            `yaml:"routing"`
	ClusterStatsConfig javaClusterStatsConfig `yaml:"clusterStatsConfiguration"`
	Authentication     javaAuthentication     `yaml:"authentication"`
	Authorization      javaAuthorization      `yaml:"authorization"`
	Modules            []interface{}          `yaml:"modules"`
	ManagedApps        []interface{}          `yaml:"managedApps"`
}

type javaRequestRouter struct {
	Port              int    `yaml:"port"`
	Name              string `yaml:"name"`
	HistorySize       int    `yaml:"historySize"`
	RequestBufferSize int    `yaml:"requestBufferSize"`
}

type javaDataStore struct {
	LocalPort int    `yaml:"localPort"`
	JdbcURL   string `yaml:"jdbcUrl"`
	User      string `yaml:"user"`
	Password  string `yaml:"password"`
	Driver    string `yaml:"driver"`
}

type javaBackendState struct {
	HealthCheckPath     string `yaml:"healthCheckPath"`
	Concurrency         int    `yaml:"concurrency"`
	HealthCheckInterval int    `yaml:"healthCheckInterval"` // milliseconds
}

type javaRouting struct {
	DefaultRoutingGroup string `yaml:"defaultRoutingGroup"`
	RulesType           string `yaml:"rulesType"`
	ExternalURL         string `yaml:"externalUrl"`
}

type javaClusterStatsConfig struct {
	UseAPI bool `yaml:"useApi"`
}

type javaAuthentication struct {
	DefaultType string     `yaml:"defaultType"`
	OAuth2      javaOAuth2 `yaml:"oauth2"`
}

type javaOAuth2 struct {
	Issuer       string `yaml:"issuer"`
	ClientID     string `yaml:"clientId"`
	ClientSecret string `yaml:"clientSecret"`
	RedirectURL  string `yaml:"redirectUrl"`
	JwkURL       string `yaml:"jwkUrl"`
}

type javaAuthorization struct {
	Admin string `yaml:"admin"`
	User  string `yaml:"user"`
	API   string `yaml:"api"`
}

// MigrateConfig converts a Java trino-gateway config YAML document to a Go trino-goway Config.
// It returns the converted config, a list of human-readable warnings for unmapped or unsupported
// fields, and any parse error.
func MigrateConfig(javaYAML []byte) (*config.Config, []string, error) {
	var src javaConfig
	if err := yaml.Unmarshal(javaYAML, &src); err != nil {
		return nil, nil, fmt.Errorf("migrate: unmarshal java config: %w", err)
	}

	var warnings []string
	warn := func(msg string) { warnings = append(warnings, msg) }

	cfg := &config.Config{}

	// requestRouter
	if src.RequestRouter.Port != 0 {
		cfg.Proxy.Port = src.RequestRouter.Port
	}
	if src.RequestRouter.RequestBufferSize != 0 {
		cfg.Proxy.ResponseSize = config.DataSize{Bytes: int64(src.RequestRouter.RequestBufferSize)}
	}
	if src.RequestRouter.HistorySize != 0 {
		warn(fmt.Sprintf("requestRouter.historySize=%d has no Go equivalent, ignored", src.RequestRouter.HistorySize))
	}
	if src.RequestRouter.Name != "" {
		warn(fmt.Sprintf("requestRouter.name=%q has no Go equivalent, ignored", src.RequestRouter.Name))
	}

	// dataStore
	if src.DataStore.JdbcURL != "" {
		cfg.DB.DSN = stripJDBCPrefix(src.DataStore.JdbcURL)
	}
	if src.DataStore.Driver != "" {
		drv, ok := detectDriver(src.DataStore.Driver)
		if ok {
			cfg.DB.Driver = drv
		} else {
			warn(fmt.Sprintf("dataStore.driver=%q: unrecognised driver, db.driver not set", src.DataStore.Driver))
		}
	}
	if src.DataStore.LocalPort != 0 {
		cfg.Admin.Port = src.DataStore.LocalPort
	}
	if src.DataStore.User != "" {
		warn(fmt.Sprintf("dataStore.user=%q: embed credentials in db.dsn instead, ignored", src.DataStore.User))
	}
	if src.DataStore.Password != "" {
		warn("dataStore.password: embed credentials in db.dsn instead, ignored")
	}

	// backendState
	if src.BackendState.HealthCheckInterval != 0 {
		secs := src.BackendState.HealthCheckInterval / 1000
		if secs < 1 {
			secs = 1
		}
		cfg.Monitor.Interval = config.Duration{D: time.Duration(secs) * time.Second}
	}
	if src.BackendState.Concurrency != 0 {
		warn("backendState.concurrency is handled automatically, ignored")
	}
	if src.BackendState.HealthCheckPath != "" {
		warn(fmt.Sprintf("backendState.healthCheckPath=%q has no Go equivalent, ignored", src.BackendState.HealthCheckPath))
	}

	// routing
	if src.Routing.DefaultRoutingGroup != "" {
		cfg.Routing.DefaultGroup = src.Routing.DefaultRoutingGroup
	}
	if src.Routing.RulesType != "" {
		if src.Routing.RulesType == "EXTERNAL" {
			cfg.Routing.Type = "EXTERNAL"
		} else {
			warn(fmt.Sprintf("routing.rulesType=%q: only EXTERNAL routing supported, setting routing.type=EXTERNAL", src.Routing.RulesType))
			cfg.Routing.Type = "EXTERNAL"
		}
	}
	if src.Routing.ExternalURL != "" {
		cfg.Routing.External.URL = src.Routing.ExternalURL
	}

	// authentication
	if src.Authentication.OAuth2.JwkURL != "" {
		cfg.Auth.Type = "OIDC"
		cfg.Auth.OIDC.JWKSURL = src.Authentication.OAuth2.JwkURL
		cfg.Auth.OIDC.IssuerURL = src.Authentication.OAuth2.Issuer
		cfg.Auth.OIDC.ClientID = src.Authentication.OAuth2.ClientID
		cfg.Auth.OIDC.ClientSecret = src.Authentication.OAuth2.ClientSecret
		if src.Authentication.OAuth2.RedirectURL != "" {
			warn(fmt.Sprintf("authentication.oauth2.redirectUrl=%q has no Go equivalent, ignored", src.Authentication.OAuth2.RedirectURL))
		}
	} else if src.Authentication.DefaultType != "" {
		warn(fmt.Sprintf("authentication.defaultType=%q: no OIDC jwkUrl found, auth not configured", src.Authentication.DefaultType))
	}

	// authorization
	if src.Authorization.Admin != "" || src.Authorization.User != "" || src.Authorization.API != "" {
		warn("authorization block has no Go equivalent, ignored")
	}

	// modules / managedApps
	if len(src.Modules) > 0 {
		warn("modules not supported, ignored")
	}
	if len(src.ManagedApps) > 0 {
		warn("managedApps not supported, ignored")
	}

	return cfg, warnings, nil
}

// stripJDBCPrefix removes the leading "jdbc:" from a JDBC URL.
func stripJDBCPrefix(url string) string {
	if strings.HasPrefix(url, "jdbc:") {
		return url[len("jdbc:"):]
	}
	return url
}

// detectDriver maps a Java JDBC driver class name to a Go driver name.
// Returns ("postgres"|"mysql", true) on a match, ("", false) otherwise.
func detectDriver(driver string) (string, bool) {
	lower := strings.ToLower(driver)
	switch {
	case strings.Contains(lower, "postgresql") || strings.Contains(lower, "postgres"):
		return "postgres", true
	case strings.Contains(lower, "mysql"):
		return "mysql", true
	default:
		return "", false
	}
}
