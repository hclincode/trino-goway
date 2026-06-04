package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hclincode/trino-goway-routing-service/internal/config"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "config-*.yaml")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	_ = f.Close()
	return f.Name()
}

func TestLoad_Valid(t *testing.T) {
	path := writeTemp(t, `
addr: ":9001"
defaultRoutingGroup: "adhoc"
methods:
  - type: expr
    program: 'source == "airflow" ? "etl" : ""'
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	if cfg.Addr != ":9001" {
		t.Errorf("Addr = %q, want %q", cfg.Addr, ":9001")
	}
	if cfg.DefaultRoutingGroup != "adhoc" {
		t.Errorf("DefaultRoutingGroup = %q, want %q", cfg.DefaultRoutingGroup, "adhoc")
	}
	if len(cfg.Methods) != 1 {
		t.Fatalf("len(Methods) = %d, want 1", len(cfg.Methods))
	}
	if cfg.Methods[0].Type != "expr" {
		t.Errorf("Methods[0].Type = %q, want %q", cfg.Methods[0].Type, "expr")
	}
}

func TestLoad_Defaults(t *testing.T) {
	// addr and metricsAddr should default when omitted.
	path := writeTemp(t, `
defaultRoutingGroup: "default"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	if cfg.Addr != ":9001" {
		t.Errorf("Addr default = %q, want %q", cfg.Addr, ":9001")
	}
	if cfg.MetricsAddr != ":9091" {
		t.Errorf("MetricsAddr default = %q, want %q", cfg.MetricsAddr, ":9091")
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := config.Load(filepath.Join(t.TempDir(), "no-such-file.yaml"))
	if err == nil {
		t.Fatal("Load: expected error for missing file, got nil")
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	path := writeTemp(t, ":::bad yaml:::")
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load: expected error for invalid YAML, got nil")
	}
}

func TestValidate_Errors(t *testing.T) {
	cases := []struct {
		name   string
		yaml   string
		errSub string
	}{
		{
			name: "missing defaultRoutingGroup",
			yaml: `addr: ":9001"`,
			errSub: "defaultRoutingGroup",
		},
		{
			name: "empty addr",
			yaml: `
addr: ""
defaultRoutingGroup: "default"
`,
			errSub: "addr",
		},
		{
			name: "method with both program and file",
			yaml: `
defaultRoutingGroup: "default"
methods:
  - type: expr
    program: 'source == "x" ? "y" : ""'
    file: /some/path.expr
`,
			errSub: "not both",
		},
		{
			name: "method with neither program nor file",
			yaml: `
defaultRoutingGroup: "default"
methods:
  - type: expr
`,
			errSub: "exactly one",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTemp(t, tc.yaml)
			_, err := config.Load(path)
			if err == nil {
				t.Fatalf("Load: expected validation error containing %q, got nil", tc.errSub)
			}
			if tc.errSub != "" {
				if errStr := err.Error(); len(errStr) == 0 {
					t.Errorf("error message empty")
				}
			}
		})
	}
}

func TestValidate_UnknownMethodType_NoError(t *testing.T) {
	// Unknown type is not a config-parse error — the registry decides at build time.
	path := writeTemp(t, `
defaultRoutingGroup: "default"
methods:
  - type: unknown-future-type
    program: "some program"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error for unknown type: %v", err)
	}
	if cfg.Methods[0].Type != "unknown-future-type" {
		t.Errorf("Methods[0].Type = %q", cfg.Methods[0].Type)
	}
}

func TestDuration_Unmarshal(t *testing.T) {
	path := writeTemp(t, `
defaultRoutingGroup: "default"
methods:
  - type: expr
    program: '""'
    refresh: "30s"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	if cfg.Methods[0].Refresh.D.Seconds() != 30 {
		t.Errorf("Refresh = %v, want 30s", cfg.Methods[0].Refresh.D)
	}
}
