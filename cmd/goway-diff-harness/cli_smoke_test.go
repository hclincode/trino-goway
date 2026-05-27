package main_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCLI_LiveSmoke_ExitsZero builds the CLI, boots two fakes, runs
// `live` against them, and asserts exit 0 with a PASS in stdout.
// External entry point for the Phase-1 acceptance smoke.
func TestCLI_LiveSmoke_ExitsZero(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI smoke under -short")
	}

	javaGW := newSmokeGateway(t)
	defer javaGW.Close()
	goGW := newSmokeGateway(t)
	defer goGW.Close()

	bin := buildCLI(t)

	scenariosDir, err := filepath.Abs("testdata/scenarios")
	require.NoError(t, err)

	// The smoke fake only knows POST /v1/statement; point the smoke at seam1
	// alone. Phase-3 scenarios are validated against the real fleet by the
	// //go:build diff live_test.go, which is exactly what they're for.
	smokeScenarios := t.TempDir()
	require.NoError(t, copyFile(
		filepath.Join(scenariosDir, "seam1-body-passthrough.yaml"),
		filepath.Join(smokeScenarios, "seam1-body-passthrough.yaml"),
	))

	out, err := exec.Command(bin, "live",
		"--java-url", javaGW.URL,
		"--go-url", goGW.URL,
		"--scenarios", smokeScenarios,
		"--format", "text",
	).CombinedOutput()

	assert.NoError(t, err, "CLI exit non-zero — output:\n%s", string(out))
	assert.Contains(t, string(out), "result:   PASS",
		"seam1 scenario must pass against two identical fakes")
}

// copyFile is a small helper so the smoke can isolate seam1 from the full
// scenarios dir without depending on shell utilities.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

func buildCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "goway-diff-harness")
	// Build relative to this test's package (cmd/goway-diff-harness).
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Run(), "failed to build CLI binary")
	return bin
}

func newSmokeGateway(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/statement" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		resp := map[string]any{
			"id":      "qid-from-" + r.Host,
			"nextUri": fmt.Sprintf("http://%s/v1/statement/qid/1", r.Host),
			"stats":   map[string]any{"state": "QUEUED", "elapsedTimeMillis": 0},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// TestCLI_RecordThenReplay_ExitsZero exercises the record → replay loop end
// to end against two identical fakes. record writes a golden; replay diffs
// the Go side against it.
func TestCLI_RecordThenReplay_ExitsZero(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI smoke under -short")
	}

	javaGW := newSmokeGateway(t)
	defer javaGW.Close()
	goGW := newSmokeGateway(t)
	defer goGW.Close()

	bin := buildCLI(t)
	scenariosDir, err := filepath.Abs("testdata/scenarios")
	require.NoError(t, err)

	// Same reason as TestCLI_LiveSmoke_ExitsZero: the smoke fake only knows
	// POST /v1/statement, so isolate seam1 from the full set.
	smokeScenarios := t.TempDir()
	require.NoError(t, copyFile(
		filepath.Join(scenariosDir, "seam1-body-passthrough.yaml"),
		filepath.Join(smokeScenarios, "seam1-body-passthrough.yaml"),
	))
	goldensDir := t.TempDir()

	out, err := exec.Command(bin, "record",
		"--java-url", javaGW.URL,
		"--scenarios", smokeScenarios,
		"--goldens", goldensDir,
	).CombinedOutput()
	require.NoError(t, err, "record exit non-zero — output:\n%s", string(out))
	assert.Contains(t, string(out), "recorded seam1-body-passthrough")

	out, err = exec.Command(bin, "replay",
		"--go-url", goGW.URL,
		"--scenarios", smokeScenarios,
		"--goldens", goldensDir,
	).CombinedOutput()
	assert.NoError(t, err, "replay exit non-zero — output:\n%s", string(out))
	assert.Contains(t, string(out), "result:   PASS")
}

// TestCLI_Report_ReformatsJSON runs `report` against a synthetic results
// file and asserts it re-renders as text.
func TestCLI_Report_ReformatsJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI smoke under -short")
	}

	bin := buildCLI(t)
	tmp := t.TempDir()
	input := filepath.Join(tmp, "results.json")
	require.NoError(t, os.WriteFile(input,
		[]byte(`{"results":[{"scenario":"x","verdict":"PASS","javaStatus":200,"goStatus":200,"statusMatch":true}],"summary":{"pass":1}}`),
		0o644))

	out, err := exec.Command(bin, "report", "--input", input, "--format", "text").CombinedOutput()
	require.NoError(t, err, "report exit non-zero — output:\n%s", string(out))
	assert.Contains(t, string(out), "result:   PASS")
}

// TestCLI_MissingRequiredFlags_ExitsUsage verifies each subcommand surfaces a
// usage error (exit 2) when its required flags are missing.
func TestCLI_MissingRequiredFlags_ExitsUsage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI smoke under -short")
	}
	bin := buildCLI(t)

	for _, sub := range []string{"live", "record", "replay"} {
		out, err := exec.Command(bin, sub).CombinedOutput()
		require.Error(t, err, "expected non-zero exit for %s without flags — output:\n%s", sub, out)
		assert.Contains(t, string(out), "required")
	}
	out, err := exec.Command(bin, "report").CombinedOutput()
	require.Error(t, err, "report without --input should exit non-zero — output:\n%s", out)
	assert.Contains(t, string(out), "required")
}

// silence imports the test won't otherwise reference.
var _ = strings.HasPrefix
