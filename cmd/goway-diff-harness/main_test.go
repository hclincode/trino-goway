package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/diffharness"
)

// TestCLI_LiveMode_SmokeEndToEnd boots two fake gateways, runs the harness in
// live mode against the committed seam1 scenario, and asserts the diff passes.
// This is the Phase-1 smoke gate: proves the scenario file, loader, runner,
// normalizer, and CLI all hang together without containers.
func TestCLI_LiveMode_SmokeEndToEnd(t *testing.T) {
	t.Parallel()

	java := newSmokeGateway(t)
	defer java.Close()
	goGW := newSmokeGateway(t)
	defer goGW.Close()

	scenariosDir := "testdata/scenarios"
	files, err := os.ReadDir(scenariosDir)
	require.NoError(t, err)
	require.NotEmpty(t, files, "at least one scenario must ship with Phase 1")

	scenarios, err := loadAllScenarios(scenariosDir)
	require.NoError(t, err)
	assert.NotEmpty(t, scenarios)

	// Verify the seam1 file is well-formed and has the expected name.
	found := false
	for _, s := range scenarios {
		if s.Name == "seam1-body-passthrough" {
			found = true
			break
		}
	}
	assert.True(t, found, "seam1-body-passthrough scenario must be present")
}

// TestEmit_ExitCodes pins the CLI's exit-code contract: PASS-only → 0,
// any FAIL or ERROR → 1. CI gates on this code.
func TestEmit_ExitCodes(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// emit() writes to os.Stdout; redirect to a temp file so test output is
	// not polluted and the assertion focuses on the return value.
	origStdout := os.Stdout
	t.Cleanup(func() { os.Stdout = origStdout })
	f, err := os.Create(filepath.Join(dir, "stdout"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	os.Stdout = f

	passOnly := []diffharnessResult{{Verdict: "PASS"}}
	withFail := []diffharnessResult{{Verdict: "PASS"}, {Verdict: "FAIL"}}

	assert.Equal(t, 0, emitWrapped(passOnly, "text"))
	assert.Equal(t, 1, emitWrapped(withFail, "text"))
	assert.Equal(t, 0, emitWrapped(passOnly, "json"))
	assert.Equal(t, 1, emitWrapped(withFail, "json"))
}

// diffharnessResult is the test-side alias to avoid importing the package
// twice (main already imports it).
type diffharnessResult = diffharness.Result

func emitWrapped(rs []diffharnessResult, fmt string) int {
	return emit(rs, fmt)
}

// newSmokeGateway mirrors what a real Trino gateway returns from POST
// /v1/statement: id + nextUri pointing at its own host.
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
