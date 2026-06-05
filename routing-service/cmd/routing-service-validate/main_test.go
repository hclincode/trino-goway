package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestValidateBinary_ExitCodes is an end-to-end smoke test: it builds the
// routing-service-validate binary and exercises the three exit-code paths
// (valid → 0, invalid → 1, diff-changed → 2) the CI gate depends on.
func TestValidateBinary_ExitCodes(t *testing.T) {
	bin := buildBinary(t)

	validCfg := writeFile(t, "valid.yaml", configRouting("group-a"))
	changedCfg := writeFile(t, "changed.yaml", configRouting("group-b"))
	invalidCfg := writeFile(t, "invalid.yaml",
		"addr: \":9001\"\ndefaultRoutingGroup: default\nmethods:\n  - type: expr\n    program: '42'\n")
	samples := writeFile(t, "samples.yaml", "- id: hit-a\n  source: a\n  is_new: true\n")

	cases := []struct {
		name     string
		args     []string
		wantCode int
	}{
		{"valid", []string{"--config", validCfg}, 0},
		{"invalid", []string{"--config", invalidCfg}, 1},
		{"diff-none", []string{"--config", validCfg, "--samples", samples, "--diff", "--baseline", validCfg}, 0},
		{"diff-changed", []string{"--config", changedCfg, "--samples", samples, "--diff", "--baseline", validCfg}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bin, tc.args...)
			out, err := cmd.CombinedOutput()
			code := exitCode(err)
			if code != tc.wantCode {
				t.Fatalf("exit = %d, want %d\noutput:\n%s", code, tc.wantCode, out)
			}
		})
	}
}

// configRouting returns a config routing source=="a" to group via expr.
func configRouting(group string) string {
	return "" +
		"addr: \":9001\"\n" +
		"defaultRoutingGroup: default\n" +
		"methods:\n" +
		"  - type: expr\n" +
		"    program: 'request.source == \"a\" ? \"" + group + "\" : \"\"'\n"
}

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "routing-service-validate")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build binary: %v\n%s", err, out)
	}
	return bin
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}
