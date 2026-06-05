// Package config loads and validates the routing-service configuration.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration with YAML unmarshaling support for Go duration strings
// such as "200ms", "1s", "30s".
type Duration struct {
	D time.Duration
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("config: duration: decode: %w", err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("config: duration: parse %q: %w", s, err)
	}
	d.D = parsed
	return nil
}

// MethodConfig holds the configuration for a single routing method provider.
// Exactly one of Program (inline source) or File (path to source file) must be set.
type MethodConfig struct {
	// Type identifies the provider: "expr" or "script".
	Type string `yaml:"type"`
	// Refresh is how often the file-based source is re-checked for changes.
	// Only relevant when File is set; ignored for Program.
	Refresh Duration `yaml:"refresh"`
	// Program is an inline source string (mutually exclusive with File).
	Program string `yaml:"program"`
	// File is a path to a source file (mutually exclusive with Program).
	File string `yaml:"file"`
}

// Config is the top-level routing-service configuration.
type Config struct {
	// Addr is the gRPC listen address, e.g. ":9001". Default: ":9001".
	Addr string `yaml:"addr"`
	// MetricsAddr is the HTTP address for the /metrics endpoint. Default: ":9091".
	MetricsAddr string `yaml:"metricsAddr"`
	// AdminAddr is the gRPC listen address for the RoutingServiceAdmin
	// kill-switch service. Default: ":9092". It is served on a SEPARATE listener
	// from Addr so it can be firewalled to platform operators only (no auth in
	// Phase 1).
	AdminAddr string `yaml:"adminAddr"`
	// DefaultRoutingGroup is returned when all methods defer (return empty/"").
	// Must be non-empty.
	DefaultRoutingGroup string `yaml:"defaultRoutingGroup"`
	// Methods is the ordered list of routing method providers.
	// The pipeline evaluates them in order; first definitive decision wins.
	Methods []MethodConfig `yaml:"methods"`
}

// Load reads a YAML config file at path, applies defaults, and validates.
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
		Addr:        ":9001",
		MetricsAddr: ":9091",
		AdminAddr:   ":9092",
	}
}

// applyDefaults fills zero-value fields after YAML unmarshaling.
// Note: addr and metricsAddr are NOT defaulted here — if the user omits them
// they arrive as "" (overwriting the pre-filled defaultConfig values because
// yaml.Unmarshal merges into the struct). The pre-filled values in defaultConfig
// are used only when the field is truly absent from the YAML. An explicit
// empty string is caught by Validate. metricsAddr is optional and defaults to
// ":9091" only when it was genuinely absent (zero-value after unmarshal over
// a non-empty default means the user provided "").
// Addr default is enforced by Validate rejecting "".
func applyDefaults(cfg *Config) {
	if cfg.MetricsAddr == "" {
		cfg.MetricsAddr = ":9091"
	}
	if cfg.AdminAddr == "" {
		cfg.AdminAddr = ":9092"
	}
}

// Validate checks the configuration for logical errors.
func (c *Config) Validate() error {
	if c.Addr == "" {
		return fmt.Errorf("config: validate: addr must be non-empty")
	}
	if c.DefaultRoutingGroup == "" {
		return fmt.Errorf("config: validate: defaultRoutingGroup must be non-empty")
	}
	for i, m := range c.Methods {
		if m.Program != "" && m.File != "" {
			return fmt.Errorf("config: validate: methods[%d]: only one of program or file may be set, not both", i)
		}
		if m.Program == "" && m.File == "" {
			return fmt.Errorf("config: validate: methods[%d]: exactly one of program or file must be set", i)
		}
	}
	return nil
}
