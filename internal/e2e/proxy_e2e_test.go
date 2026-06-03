//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/hclincode/trino-goway/internal/config"
	"github.com/hclincode/trino-goway/internal/proxy"
	"github.com/hclincode/trino-goway/internal/routing"
)

const (
	// trinoImage pins the Trino server image. Kept in sync with the version
	// recorded in doc/studies/both/component-signoff-rubric.qa-tech-lead.md (trino: 481-150-...).
	trinoImage = "trinodb/trino:481"

	// trinoBootBudget bounds how long we wait for the coordinator to come up.
	// G1 is a "silent failure mode" gate per TODO Task 27: t.Skip if we exceed
	// this so the test stays safe on Docker-less workstations and CI lanes.
	trinoBootBudget = 90 * time.Second
)

// TestG1_NextURIHostDerivation is the first QA gate.
//
// Sequence:
//  1. Boot a real Trino coordinator container.
//  2. Boot the trino-goway proxy (composed directly — no DB, no admin, no
//     external router) pointing recovery at the Trino container.
//  3. POST `SELECT 1` to the proxy's /v1/statement.
//  4. Assert the returned `nextUri` host:port is the GATEWAY's, not the
//     coordinator's. If Trino is leaking its bound address into nextUri the
//     gateway's redirect-disable invariant cannot protect downstream clients.
//
// The proxy itself never rewrites response bodies (Hard Invariant #1), so this
// test verifies the end-to-end property: Trino's nextUri is host-header-derived
// AND the proxy injects X-Forwarded-Host correctly.
func TestG1_NextURIHostDerivation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), trinoBootBudget)
	defer cancel()

	trinoURL := startTrinoOrSkip(ctx, t)

	gateway := startGateway(t, trinoURL)
	defer gateway.Close()

	gatewayHost := mustHost(t, gateway.URL)

	// Drive a real Trino statement through the gateway.
	req, err := http.NewRequestWithContext(ctx,
		http.MethodPost, gateway.URL+"/v1/statement", strings.NewReader("SELECT 1"))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("X-Trino-User", "g1-e2e")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err, "POST /v1/statement to gateway")
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equalf(t, http.StatusOK, resp.StatusCode,
		"gateway POST /v1/statement: want 200, got %d; body=%s", resp.StatusCode, string(body))

	var stmt struct {
		ID      string `json:"id"`
		NextURI string `json:"nextUri"`
	}
	require.NoError(t, json.Unmarshal(body, &stmt), "parse Trino response: %s", string(body))
	require.NotEmpty(t, stmt.ID, "Trino response must include id")
	require.NotEmpty(t, stmt.NextURI, "Trino response must include nextUri")

	nextHost := mustHost(t, stmt.NextURI)
	trinoHost := mustHost(t, trinoURL)

	// The gate: nextUri must point at the gateway, not at the coordinator.
	// If this fails, downstream clients will bypass the gateway on every poll —
	// queryId routing collapses, recovery chain never sees the queries, and
	// every Hard Invariant on the proxy becomes moot for ongoing statements.
	assert.Equal(t, gatewayHost, nextHost,
		"nextUri host must equal gateway %q (got %q in nextUri=%q)",
		gatewayHost, nextHost, stmt.NextURI)
	assert.NotEqual(t, trinoHost, nextHost,
		"nextUri must NOT leak the Trino coordinator host %q (nextUri=%q)",
		trinoHost, stmt.NextURI)
}

// startTrinoOrSkip launches a Trino coordinator container. If Docker is
// unavailable or the container fails to come up within trinoBootBudget, the
// test is skipped silently — per TODO Task 27, e2e tests must never fail
// noisily on infrastructure that lacks Docker.
func startTrinoOrSkip(ctx context.Context, t *testing.T) string {
	t.Helper()

	req := testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        trinoImage,
			ExposedPorts: []string{"8080/tcp"},
			WaitingFor: wait.ForHTTP("/v1/info").
				WithPort("8080/tcp").
				WithStatusCodeMatcher(func(status int) bool { return status == http.StatusOK }).
				WithStartupTimeout(trinoBootBudget),
		},
		Started: true,
	}

	container, err := testcontainers.GenericContainer(ctx, req)
	if err != nil {
		t.Skipf("G1 e2e: skipping (Docker/Trino unavailable): %v", err)
	}
	t.Cleanup(func() {
		// Use a fresh context — ctx is bound to trinoBootBudget which may be expired.
		_ = container.Terminate(context.Background())
	})

	host, err := container.Host(ctx)
	if err != nil {
		t.Skipf("G1 e2e: skipping (container host unavailable): %v", err)
	}
	port, err := container.MappedPort(ctx, "8080")
	if err != nil {
		t.Skipf("G1 e2e: skipping (container port unavailable): %v", err)
	}

	return fmt.Sprintf("http://%s:%s", host, port.Port())
}

// startGateway composes the proxy directly (no DB, no admin, no external
// router) and exposes it via httptest.Server. The proxy's recovery chain will
// fall through to firstActiveBackend → the supplied Trino URL.
func startGateway(t *testing.T, trinoURL string) *httptest.Server {
	t.Helper()

	backends := &fixedBackendLister{
		backends: []routing.ActiveBackend{
			{Name: "trino-1", URL: trinoURL, RoutingGroup: "default"},
		},
	}

	router, err := routing.New(routing.Config{
		Routing: config.RoutingConfig{
			DefaultGroup: "default",
			Type:         "EXTERNAL",
			External:     config.ExternalConfig{Timeout: config.Duration{D: 500 * time.Millisecond}},
		},
		ExternalClient: &http.Client{Timeout: 500 * time.Millisecond},
		ProbeClient:    &http.Client{Timeout: 1 * time.Second},
		History:        noHistory{},
		Backends:       backends,
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)

	// proxyClient mirrors the production wiring: never follow redirects
	// (Hard Invariant #2) — the whole point of G1 is to confirm nextUri
	// doesn't push clients past the gateway.
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

// mustHost extracts host:port from a URL string; fails the test if malformed.
func mustHost(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoErrorf(t, err, "parse url %q", raw)
	require.NotEmptyf(t, u.Host, "url %q has empty Host", raw)
	return u.Host
}

// fixedBackendLister returns a hard-coded list of active backends.
type fixedBackendLister struct {
	backends []routing.ActiveBackend
}

func (f *fixedBackendLister) ListActive(_ context.Context) ([]routing.ActiveBackend, error) {
	return f.backends, nil
}

// noHistory returns "not found" for every query lookup, exercising the
// recovery chain's history-miss path without requiring a real DB.
type noHistory struct{}

func (noHistory) LookupByQueryID(_ context.Context, _ string) (string, error) {
	return "", nil
}
