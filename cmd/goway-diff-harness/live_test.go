//go:build diff

package main_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/config"
	"github.com/hclincode/trino-goway/internal/diffharness"
	"github.com/hclincode/trino-goway/internal/proxy"
	"github.com/hclincode/trino-goway/internal/routing"
)

// TestLive_SeamScenarios_DiffPasses is the Phase-2 integration smoke. It:
//  1. Boots the Phase-2 container fleet (Postgres → Java gateway → Trino) via
//     diffharness.BootstrapContainers.
//  2. Stands up the Go trino-goway in-process (same pattern as Task 27 G1)
//     pointed at the shared Trino backend.
//  3. Runs every committed scenario against both gateways and asserts PASS.
//
// Slow (~60–90s first boot). Gated by //go:build diff so per-PR CI does not
// pay the cost; the nightly job runs `go test -tags=diff ./...`.
func TestLive_SeamScenarios_DiffPasses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	containers := bootstrapOrSkip(ctx, t)

	gw := startGoGateway(t, containers.TrinoURL)
	defer gw.Close()

	scenariosDir, err := filepath.Abs("testdata/scenarios")
	require.NoError(t, err)

	scenarios := loadScenariosOrSkip(t, scenariosDir)

	r := diffharness.NewRunner(
		diffharness.Target{Name: "java", BaseURL: containers.JavaURL},
		diffharness.Target{Name: "go", BaseURL: gw.URL},
	)

	results := r.RunAll(ctx, scenarios)
	for _, res := range results {
		assert.Equalf(t, diffharness.VerdictPass, res.Verdict,
			"scenario %s: verdict=%s body diff=%q header diffs=%v reason=%q",
			res.Scenario, res.Verdict, res.BodyDiff, res.HeaderDiffs, res.Reason)
	}
}

// bootstrapOrSkip wraps BootstrapContainers in a t.Skip on Docker-unavailable
// hosts, mirroring the Task-27 G1 convention (never noisy on Docker-less
// laptops; CI nightly explicitly opts in to docker).
func bootstrapOrSkip(ctx context.Context, t *testing.T) diffharness.Containers {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Skipf("diff harness: docker unavailable: %v", r)
		}
	}()
	return diffharness.BootstrapContainers(ctx, t)
}

// loadScenariosOrSkip reads every YAML scenario from dir. If the directory is
// empty the test skips — scenarios are added incrementally in Phase 3 and the
// live test should not fail just because Phase-3 work is pending.
func loadScenariosOrSkip(t *testing.T, dir string) []*diffharness.Scenario {
	t.Helper()
	entries, err := readDirYAML(dir)
	require.NoError(t, err)
	if len(entries) == 0 {
		t.Skipf("no scenario YAML files in %s", dir)
	}
	out := make([]*diffharness.Scenario, 0, len(entries))
	for _, p := range entries {
		s, err := diffharness.LoadScenario(p)
		require.NoErrorf(t, err, "load scenario %s", p)
		out = append(out, s)
	}
	return out
}

// startGoGateway composes the Go proxy in-process pointed at the supplied
// Trino URL (the host-mapped port from BootstrapContainers). No DB, no admin,
// no external router service — only the proxy + recovery chain.
//
// This mirrors internal/e2e/proxy_e2e_test.go::startGateway intentionally:
// the same wiring shape, so a diff-harness pass is equivalent to a G1 pass at
// the proxy layer.
func startGoGateway(t *testing.T, trinoURL string) *httptest.Server {
	t.Helper()

	backends := &fixedBackendLister{backends: []routing.ActiveBackend{
		{Name: "trino", URL: trinoURL, RoutingGroup: "default"},
	}}

	router, err := routing.New(routing.Config{
		Routing: config.RoutingConfig{
			DefaultGroup: "default",
			Type:         "EXTERNAL",
			External:     config.ExternalConfig{Timeout: config.Duration{D: 500 * time.Millisecond}},
		},
		ExternalClient: &http.Client{Timeout: 500 * time.Millisecond},
		ProbeClient:    &http.Client{Timeout: 1 * time.Second},
		History:        noHistoryLookup{},
		Backends:       backends,
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)

	proxyClient := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	handler := proxy.New(proxy.Config{
		Proxy:  config.ProxyConfig{ResponseSize: config.DataSize{Bytes: 1_048_576}},
		Cookie: config.CookieConfig{WireCompat: true},
		Client: proxyClient,
		Router: router,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return httptest.NewServer(handler)
}

type fixedBackendLister struct {
	backends []routing.ActiveBackend
}

func (f *fixedBackendLister) ListActive(_ context.Context) ([]routing.ActiveBackend, error) {
	return f.backends, nil
}

type noHistoryLookup struct{}

func (noHistoryLookup) LookupByQueryID(_ context.Context, _ string) (string, error) {
	return "", nil
}

// readDirYAML returns the absolute paths of every .yaml/.yml file directly
// under dir. Wraps os.ReadDir + filepath.Join so the live test does not need
// to duplicate the filtering logic from main.go.
func readDirYAML(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !hasYAMLSuffix(name) {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	return out, nil
}

func hasYAMLSuffix(s string) bool {
	const a, b = ".yaml", ".yml"
	return len(s) >= len(a) && s[len(s)-len(a):] == a ||
		len(s) >= len(b) && s[len(s)-len(b):] == b
}
