//go:build diff

package diffharness

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"text/template"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcnetwork "github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// javaGatewayImage is the published Java trino-gateway tag used by the
// differential harness. Pinned explicitly so CI cannot silently upgrade.
//
// nearest published semver to studies-pinned 19-21-g334ba12; no hashed-tag
// channel available
const javaGatewayImage = "trinodb/trino-gateway:19"

// trinoImage is the published Trino tag the java gateway and the Go gateway
// both route to. Same major as the gateway's compatibility matrix.
const trinoImage = "trinodb/trino:476"

// postgresImage matches the upstream docker-compose default. Postgres is only
// used by the java gateway for its query-history / backend tables.
const postgresImage = "postgres:17-alpine"

// gatewayDBName / gatewayDBUser / gatewayDBPass match the upstream
// docker-compose defaults so the embedded config template needs no escaping.
const (
	gatewayDBName = "trino_gateway_db"
	gatewayDBUser = "trino_gateway_db_admin"
	gatewayDBPass = "P0stG&es"
)

//go:embed testdata/java-gateway-config.yaml.tmpl
var javaGatewayConfigTmpl string

//go:embed testdata/trino-config.properties
var trinoConfigProperties []byte

// Containers is the handle returned by BootstrapContainers. The URLs are
// loopback-resolvable from the host (testcontainers maps the gateway port).
//
// JavaURL is the base URL of the Java trino-gateway (e.g. http://127.0.0.1:34567).
// TrinoURL is the base URL of the shared Trino backend (e.g. http://127.0.0.1:45678).
// Both gateways (Java and Go) route to the same TrinoURL so request shape, not
// backend behavior, is what differs across runs.
type Containers struct {
	JavaURL  string
	TrinoURL string
}

// BootstrapContainers boots the Phase 2 container fleet:
//
//	postgres → java-gateway (linked to postgres) → trino (linked to both)
//
// All containers share one user-defined network so the gateway can reach
// postgres at "postgres:5432" and trino at "trino:8080". Cleanup is registered
// via t.Cleanup; the test does not need to terminate containers manually.
//
// Slow: end-to-end first boot takes ~60–90s on a warm Docker host. Intended
// to run under //go:build diff in nightly CI, not per-PR.
func BootstrapContainers(ctx context.Context, t testing.TB) Containers {
	t.Helper()

	net, err := tcnetwork.New(ctx)
	if err != nil {
		t.Fatalf("diffharness: create network: %v", err)
	}
	t.Cleanup(func() {
		if err := net.Remove(context.Background()); err != nil {
			t.Logf("diffharness: remove network: %v", err)
		}
	})

	startContainer(ctx, t, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        postgresImage,
			ExposedPorts: []string{"5432/tcp"},
			Env: map[string]string{
				"POSTGRES_DB":       gatewayDBName,
				"POSTGRES_USER":     gatewayDBUser,
				"POSTGRES_PASSWORD": gatewayDBPass,
			},
			Networks:       []string{net.Name},
			NetworkAliases: map[string][]string{net.Name: {"postgres"}},
			WaitingFor: wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})

	trinoC := startContainer(ctx, t, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        trinoImage,
			ExposedPorts: []string{"8080/tcp"},
			Networks:     []string{net.Name},
			NetworkAliases: map[string][]string{net.Name: {"trino"}},
			// Replace the image's default config.properties with one that adds
			// http-server.process-forwarded=true. Both gateways inject
			// X-Forwarded-* headers, which Trino rejects with HTTP 406 unless
			// this flag is set (see testdata/trino-config.properties).
			Files: []testcontainers.ContainerFile{{
				Reader:            bytes.NewReader(trinoConfigProperties),
				ContainerFilePath: "/etc/trino/config.properties",
				FileMode:          0o644,
			}},
			WaitingFor: wait.ForHTTP("/v1/info").
				WithPort("8080/tcp").
				WithStartupTimeout(120 * time.Second),
		},
		Started: true,
	})

	configPath := writeJavaGatewayConfig(t)

	javaC := startContainer(ctx, t, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        javaGatewayImage,
			ExposedPorts: []string{"8080/tcp"},
			Networks:     []string{net.Name},
			NetworkAliases: map[string][]string{net.Name: {"gateway"}},
			Files: []testcontainers.ContainerFile{{
				HostFilePath:      configPath,
				ContainerFilePath: "/etc/trino-gateway/config.yaml",
				FileMode:          0o644,
			}},
			WaitingFor: wait.ForHTTP("/trino-gateway/livez").
				WithPort("8080/tcp").
				WithStartupTimeout(120 * time.Second),
		},
		Started: true,
	})

	javaURL := containerBaseURL(ctx, t, javaC, "8080")
	trinoURL := containerBaseURL(ctx, t, trinoC, "8080")

	registerBackend(ctx, t, javaURL, "trino-shared", "http://trino:8080")

	// Readiness gate: the Java gateway's INFO_API cluster monitor runs on an
	// interval and does not mark the freshly-seeded backend healthy synchronously
	// with registerBackend. Until it does, the gateway 500s (empty body) on every
	// /v1/* request because routing finds no healthy cluster. Scenarios that fire
	// before the first healthy tick therefore diff Java=500 against Go=200. Poll a
	// real proxied request until the gateway routes it successfully, so the live
	// fleet is actually serving before any scenario runs.
	waitGatewayRoutable(ctx, t, javaURL)

	return Containers{JavaURL: javaURL, TrinoURL: trinoURL}
}

// waitGatewayRoutable polls POST /v1/statement (SELECT 1) against the gateway
// until it returns a 2xx, proving the seeded backend has been marked healthy by
// the cluster monitor and the gateway can route. Fails the test if the gateway
// is still not routable within the deadline.
//
// SELECT 1 is a harmless probe: it mints a throwaway Trino query that completes
// immediately; it does not affect any scenario's own request sequence.
func waitGatewayRoutable(ctx context.Context, t testing.TB, gatewayURL string) {
	t.Helper()

	const (
		deadline = 90 * time.Second
		interval = 1 * time.Second
	)
	client := &http.Client{Timeout: 10 * time.Second}
	end := time.Now().Add(deadline)

	var lastStatus int
	var lastErr error
	for time.Now().Before(end) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			gatewayURL+"/v1/statement", bytes.NewReader([]byte("SELECT 1")))
		if err != nil {
			t.Fatalf("diffharness: build readiness request: %v", err)
		}
		req.Header.Set("Content-Type", "text/plain")
		req.Header.Set("X-Trino-User", "diff-harness-readiness")

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
		} else {
			lastStatus = resp.StatusCode
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("diffharness: gateway readiness cancelled: %v", ctx.Err())
		case <-time.After(interval):
		}
	}
	t.Fatalf("diffharness: gateway not routable within %s (last status=%d, last err=%v)",
		deadline, lastStatus, lastErr)
}

func startContainer(ctx context.Context, t testing.TB, req testcontainers.GenericContainerRequest) testcontainers.Container {
	t.Helper()
	c, err := testcontainers.GenericContainer(ctx, req)
	if err != nil {
		t.Fatalf("diffharness: start %s: %v", req.Image, err)
	}
	t.Cleanup(func() {
		if err := c.Terminate(context.Background()); err != nil {
			t.Logf("diffharness: terminate %s: %v", req.Image, err)
		}
	})
	return c
}

func containerBaseURL(ctx context.Context, t testing.TB, c testcontainers.Container, port string) string {
	t.Helper()
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("diffharness: container host: %v", err)
	}
	mapped, err := c.MappedPort(ctx, "8080")
	if err != nil {
		t.Fatalf("diffharness: container mapped port %s: %v", port, err)
	}
	return fmt.Sprintf("http://%s:%s", host, mapped.Port())
}

// writeJavaGatewayConfig renders the embedded config template into a temp
// file. The template hard-codes the in-network DNS names ("postgres", "trino")
// because the java gateway reaches them through the shared user-defined
// network, not through the host's loopback mapping.
func writeJavaGatewayConfig(t testing.TB) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	tmpl, err := template.New("config").Parse(javaGatewayConfigTmpl)
	if err != nil {
		t.Fatalf("diffharness: parse config template: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("diffharness: create config: %v", err)
	}
	defer func() { _ = f.Close() }()

	if err := tmpl.Execute(f, map[string]string{
		"DBName": gatewayDBName,
		"DBUser": gatewayDBUser,
		"DBPass": gatewayDBPass,
	}); err != nil {
		t.Fatalf("diffharness: render config: %v", err)
	}
	return path
}

// registerBackend POSTs to /entity?entityType=GATEWAY_BACKEND so the java
// gateway has a routable backend immediately on first boot. Without this the
// gateway would 404 on any /v1/* request because its backend table is empty.
//
// Uses HTTP basic auth disabled (the embedded config has authentication.type:
// "noop") so no credentials are needed.
func registerBackend(ctx context.Context, t testing.TB, gatewayURL, name, backendURL string) {
	t.Helper()

	payload := fmt.Sprintf(`{
  "name": %q,
  "proxyTo": %q,
  "active": true,
  "routingGroup": "adhoc",
  "externalUrl": %q
}`, name, backendURL, backendURL)

	url := gatewayURL + "/entity?entityType=GATEWAY_BACKEND"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte(payload)))
	if err != nil {
		t.Fatalf("diffharness: build register-backend request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("diffharness: register backend: %v", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("diffharness: register backend: status %d: %s", resp.StatusCode, body)
	}
}
