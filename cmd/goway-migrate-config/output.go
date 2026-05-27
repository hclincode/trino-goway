package main

import (
	"fmt"

	"github.com/hclincode/trino-goway/internal/config"
)

// outputConfig is a YAML-serialisable mirror of config.Config that uses plain strings
// for Duration and DataSize fields (matching the format config.Load expects on read-back).
type outputConfig struct {
	Proxy   outputProxy   `yaml:"proxy,omitempty"`
	Admin   outputAdmin   `yaml:"admin,omitempty"`
	Monitor outputMonitor `yaml:"monitor,omitempty"`
	DB      outputDB      `yaml:"db,omitempty"`
	Routing outputRouting `yaml:"routing,omitempty"`
	Auth    outputAuth    `yaml:"auth,omitempty"`
	Cookie  outputCookie  `yaml:"cookie,omitempty"`
}

type outputProxy struct {
	Port           int    `yaml:"port,omitempty"`
	ResponseSize   string `yaml:"responseSize,omitempty"`
	RequestTimeout string `yaml:"requestTimeout,omitempty"`
}

type outputAdmin struct {
	Port int `yaml:"port,omitempty"`
}

type outputMonitor struct {
	Interval     string `yaml:"interval,omitempty"`
	CheckTimeout string `yaml:"checkTimeout,omitempty"`
}

type outputDB struct {
	Driver string `yaml:"driver,omitempty"`
	DSN    string `yaml:"dsn,omitempty"`
}

type outputRouting struct {
	DefaultGroup string         `yaml:"defaultGroup,omitempty"`
	Type         string         `yaml:"type,omitempty"`
	External     outputExternal `yaml:"external,omitempty"`
}

type outputExternal struct {
	URL      string `yaml:"url,omitempty"`
	GRPCAddr string `yaml:"grpcAddr,omitempty"`
	Timeout  string `yaml:"timeout,omitempty"`
}

type outputAuth struct {
	Type string      `yaml:"type,omitempty"`
	OIDC outputOIDC  `yaml:"oidc,omitempty"`
	LDAP outputLDAP  `yaml:"ldap,omitempty"`
}

type outputOIDC struct {
	IssuerURL    string `yaml:"issuerUrl,omitempty"`
	ClientID     string `yaml:"clientId,omitempty"`
	ClientSecret string `yaml:"clientSecret,omitempty"`
	JWKSURL      string `yaml:"jwksUrl,omitempty"`
}

type outputLDAP struct {
	URL      string `yaml:"url,omitempty"`
	BindDN   string `yaml:"bindDn,omitempty"`
	BindPass string `yaml:"bindPassword,omitempty"`
	UserBase string `yaml:"userBase,omitempty"`
	UserAttr string `yaml:"userAttr,omitempty"`
}

type outputCookie struct {
	Secret     string `yaml:"secret,omitempty"`
	TTL        string `yaml:"ttl,omitempty"`
	WireCompat *bool  `yaml:"wireCompat,omitempty"`
}

// toOutput converts a config.Config to the serialisable outputConfig representation.
func toOutput(cfg *config.Config) outputConfig {
	out := outputConfig{}

	out.Proxy.Port = cfg.Proxy.Port
	if cfg.Proxy.ResponseSize.Bytes > 0 {
		out.Proxy.ResponseSize = formatBytes(cfg.Proxy.ResponseSize)
	}
	if cfg.Proxy.RequestTimeout.D > 0 {
		out.Proxy.RequestTimeout = cfg.Proxy.RequestTimeout.D.String()
	}

	out.Admin.Port = cfg.Admin.Port

	if cfg.Monitor.Interval.D > 0 {
		out.Monitor.Interval = cfg.Monitor.Interval.D.String()
	}
	if cfg.Monitor.CheckTimeout.D > 0 {
		out.Monitor.CheckTimeout = cfg.Monitor.CheckTimeout.D.String()
	}

	out.DB.Driver = cfg.DB.Driver
	out.DB.DSN = cfg.DB.DSN

	out.Routing.DefaultGroup = cfg.Routing.DefaultGroup
	out.Routing.Type = cfg.Routing.Type
	out.Routing.External.URL = cfg.Routing.External.URL
	out.Routing.External.GRPCAddr = cfg.Routing.External.GRPCAddr
	if cfg.Routing.External.Timeout.D > 0 {
		out.Routing.External.Timeout = cfg.Routing.External.Timeout.D.String()
	}

	out.Auth.Type = cfg.Auth.Type
	out.Auth.OIDC.IssuerURL = cfg.Auth.OIDC.IssuerURL
	out.Auth.OIDC.ClientID = cfg.Auth.OIDC.ClientID
	out.Auth.OIDC.ClientSecret = cfg.Auth.OIDC.ClientSecret
	out.Auth.OIDC.JWKSURL = cfg.Auth.OIDC.JWKSURL
	out.Auth.LDAP.URL = cfg.Auth.LDAP.URL
	out.Auth.LDAP.BindDN = cfg.Auth.LDAP.BindDN
	out.Auth.LDAP.BindPass = cfg.Auth.LDAP.BindPass
	out.Auth.LDAP.UserBase = cfg.Auth.LDAP.UserBase
	out.Auth.LDAP.UserAttr = cfg.Auth.LDAP.UserAttr

	out.Cookie.Secret = cfg.Cookie.Secret
	if cfg.Cookie.TTL.D > 0 {
		out.Cookie.TTL = cfg.Cookie.TTL.D.String()
	}

	return out
}

// formatBytes converts a DataSize to a human-readable string that config.DataSize.UnmarshalYAML
// can parse back: it prefers MiB for round mebibyte values, KiB for kibibytes, otherwise bytes.
func formatBytes(ds config.DataSize) string {
	b := ds.Bytes
	switch {
	case b >= 1_073_741_824 && b%1_073_741_824 == 0:
		return fmt.Sprintf("%dGiB", b/1_073_741_824)
	case b >= 1_048_576 && b%1_048_576 == 0:
		return fmt.Sprintf("%dMiB", b/1_048_576)
	case b >= 1_024 && b%1_024 == 0:
		return fmt.Sprintf("%dKiB", b/1_024)
	default:
		return fmt.Sprintf("%dB", b)
	}
}
