package main

import (
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "update golden files in testdata/")

func TestMigrateConfig_GoldenFile(t *testing.T) {
	javaYAML, err := os.ReadFile("testdata/java_config.yml")
	require.NoError(t, err, "read java_config.yml")

	cfg, warnings, err := MigrateConfig(javaYAML)
	require.NoError(t, err, "MigrateConfig")
	require.NotNil(t, cfg)

	got, err := marshalWithWarnings(toOutput(cfg), warnings)
	require.NoError(t, err, "marshalWithWarnings")

	goldenPath := "testdata/expected_go_config.yml"
	if *update {
		err = os.WriteFile(goldenPath, got, 0o644)
		require.NoError(t, err, "update golden file")
		t.Logf("updated %s", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	require.NoError(t, err, "read expected_go_config.yml")

	assert.Equal(t, string(want), string(got), "output matches golden file")
}

func TestMigrateConfig_PortMapping(t *testing.T) {
	input := []byte(`
requestRouter:
  port: 9090
dataStore:
  localPort: 9091
`)
	cfg, _, err := MigrateConfig(input)
	require.NoError(t, err)
	assert.Equal(t, 9090, cfg.Proxy.Port)
	assert.Equal(t, 9091, cfg.Admin.Port)
}

func TestMigrateConfig_DBDriverDetection(t *testing.T) {
	tests := []struct {
		name           string
		driver         string
		expectedDriver string
		wantWarning    bool
	}{
		{
			name:           "postgresql full class",
			driver:         "org.postgresql.Driver",
			expectedDriver: "postgres",
		},
		{
			name:           "postgres short",
			driver:         "com.postgres.Driver",
			expectedDriver: "postgres",
		},
		{
			name:           "mysql driver",
			driver:         "com.mysql.jdbc.Driver",
			expectedDriver: "mysql",
		},
		{
			name:        "unknown driver warns",
			driver:      "com.oracle.jdbc.Driver",
			wantWarning: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := []byte("dataStore:\n  driver: " + tc.driver + "\n")
			cfg, warnings, err := MigrateConfig(input)
			require.NoError(t, err)
			assert.Equal(t, tc.expectedDriver, cfg.DB.Driver)
			if tc.wantWarning {
				require.NotEmpty(t, warnings, "expected a warning for unknown driver")
				assert.Contains(t, warnings[0], "unrecognised driver")
			} else {
				for _, w := range warnings {
					assert.NotContains(t, w, "unrecognised driver")
				}
			}
		})
	}
}

func TestMigrateConfig_UnsupportedRoutingType_Warning(t *testing.T) {
	input := []byte(`
routing:
  rulesType: FILE
  defaultRoutingGroup: default
`)
	cfg, warnings, err := MigrateConfig(input)
	require.NoError(t, err)

	// Should still set type to EXTERNAL (only supported type).
	assert.Equal(t, "EXTERNAL", cfg.Routing.Type)

	// Should have a warning about unsupported routing type.
	require.NotEmpty(t, warnings, "expected a warning")
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "only EXTERNAL routing supported") {
			found = true
			break
		}
	}
	assert.True(t, found, "expected warning about unsupported routing type, got: %v", warnings)
}
