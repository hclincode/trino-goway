//go:build e2e

package harness

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/hclincode/trino-goway/internal/e2e/harness/routingadmin"
	"github.com/hclincode/trino-goway/internal/testutil"
)

// RoutingService owns the lifecycle of a real routing-service subprocess built
// from the sibling Go module (github.com/hclincode/trino-goway-routing-service).
// It exposes the data-plane gRPC address (point the gateway at it via
// WithExternalGRPCRouter) and the admin gRPC address (the RoutingServiceAdmin
// kill-switch). All resources are released via t.Cleanup registered in
// StartRoutingService.
type RoutingService struct {
	// GRPCAddr is the data-plane address (host:port) implementing
	// TrinoGatewayRouter — pass to WithExternalGRPCRouter.
	GRPCAddr string
	// AdminAddr is the RoutingServiceAdmin kill-switch address (host:port).
	AdminAddr string
	// MetricsAddr is the Prometheus /metrics HTTP address (host:port).
	MetricsAddr string

	cmd     *exec.Cmd
	stopped bool
}

// StartRoutingService builds the routing-service binary, writes a config with
// the given inline routing program(s), launches the binary as a subprocess on
// freshly allocated data-plane / admin / metrics ports, and waits until the
// data-plane gRPC health service reports SERVING.
//
// methods is rendered verbatim into the config's `methods:` list, so callers
// pass fully-formed MethodConfig values (e.g. an inline expr program). A
// t.Cleanup is registered that SIGTERMs the subprocess (5s grace, then
// SIGKILL).
//
// Any failure short-circuits via t.Fatal so callers need not error-check.
func StartRoutingService(t testing.TB, defaultGroup string, methods ...MethodConfig) *RoutingService {
	t.Helper()

	dataPort := testutil.FreePort(t)
	adminPort := testutil.FreePort(t)
	for adminPort == dataPort {
		adminPort = testutil.FreePort(t)
	}
	metricsPort := testutil.FreePort(t)
	for metricsPort == dataPort || metricsPort == adminPort {
		metricsPort = testutil.FreePort(t)
	}

	cfgYAML := renderRoutingServiceConfig(routingServiceConfig{
		Addr:         fmt.Sprintf("127.0.0.1:%d", dataPort),
		AdminAddr:    fmt.Sprintf("127.0.0.1:%d", adminPort),
		MetricsAddr:  fmt.Sprintf("127.0.0.1:%d", metricsPort),
		DefaultGroup: defaultGroup,
		Methods:      methods,
	})

	cfgPath := filepath.Join(t.TempDir(), "routing-service.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfgYAML), 0o600),
		"routing-service: write config")

	bin := RoutingServiceBinaryPath(t)

	ctx, cancel := context.WithCancel(context.Background())

	cmd := exec.CommandContext(ctx, bin, "--config", cfgPath)
	cmd.Stdout = &logWriter{t: t, prefix: "routing-service[stdout]"}
	cmd.Stderr = &logWriter{t: t, prefix: "routing-service[stderr]"}
	cmd.SysProcAttr = procAttrNewPgrp()

	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("routing-service: start binary %s: %v", bin, err)
	}

	rs := &RoutingService{
		GRPCAddr:    fmt.Sprintf("127.0.0.1:%d", dataPort),
		AdminAddr:   fmt.Sprintf("127.0.0.1:%d", adminPort),
		MetricsAddr: fmt.Sprintf("127.0.0.1:%d", metricsPort),
		cmd:         cmd,
	}

	t.Cleanup(func() {
		if !rs.stopped {
			shutdown(t, cmd)
		}
		cancel()
	})

	rs.waitServing(t, 30*time.Second)
	return rs
}

// RoutingServiceBinaryPath returns a path to a routing-service executable. If
// TRINO_GOWAY_ROUTING_SERVICE_BIN is set, that path is used verbatim. Otherwise
// the binary is built into t.TempDir() via `go build ./cmd/routing-service`
// run inside the routing-service module directory.
func RoutingServiceBinaryPath(t testing.TB) string {
	t.Helper()

	if env := os.Getenv("TRINO_GOWAY_ROUTING_SERVICE_BIN"); env != "" {
		if _, err := os.Stat(env); err != nil {
			t.Fatalf("routing-service: TRINO_GOWAY_ROUTING_SERVICE_BIN=%s: %v", env, err)
		}
		return env
	}

	moduleDir := filepath.Join(projectRoot(t), "routing-service")
	if _, err := os.Stat(filepath.Join(moduleDir, "go.mod")); err != nil {
		t.Fatalf("routing-service: module not found at %s: %v", moduleDir, err)
	}

	out := filepath.Join(t.TempDir(), "routing-service")
	build := exec.Command("go", "build", "-o", out, "./cmd/routing-service")
	build.Dir = moduleDir
	var stderr bytes.Buffer
	build.Stderr = &stderr
	if err := build.Run(); err != nil {
		t.Fatalf("routing-service: build: %v\n%s", err, stderr.String())
	}
	return out
}

// Stop terminates the routing-service subprocess early (SIGTERM, 5s grace, then
// SIGKILL) so a test can observe the gateway's behaviour when the routing
// service is unavailable. It is safe to call before the t.Cleanup shutdown,
// which becomes a no-op once the process has exited.
func (rs *RoutingService) Stop(t testing.TB) {
	t.Helper()
	if rs.stopped {
		return
	}
	rs.stopped = true
	shutdown(t, rs.cmd)
}

// AdminClient dials the RoutingServiceAdmin kill-switch and returns a client.
// The connection is closed via t.Cleanup.
func (rs *RoutingService) AdminClient(t testing.TB) routingadmin.RoutingServiceAdminClient {
	t.Helper()

	conn, err := grpc.NewClient(rs.AdminAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err, "routing-service: dial admin")
	t.Cleanup(func() { _ = conn.Close() })

	return routingadmin.NewRoutingServiceAdminClient(conn)
}

// DisableMethod disables the named routing method (e.g. "expr") via the
// kill-switch and fails the test if the call errors.
func (rs *RoutingService) DisableMethod(t testing.TB, methodType string) {
	t.Helper()
	client := rs.AdminClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := client.DisableMethod(ctx, &routingadmin.DisableMethodRequest{Type: methodType})
	require.NoErrorf(t, err, "routing-service: DisableMethod(%q)", methodType)
}

// waitServing polls the data-plane gRPC health service until it reports
// SERVING or the deadline elapses.
func (rs *RoutingService) waitServing(t testing.TB, deadline time.Duration) {
	t.Helper()

	conn, err := grpc.NewClient(rs.GRPCAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err, "routing-service: dial data-plane for health")
	defer func() { _ = conn.Close() }()

	health := healthpb.NewHealthClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	for {
		checkCtx, checkCancel := context.WithTimeout(ctx, 2*time.Second)
		resp, err := health.Check(checkCtx, &healthpb.HealthCheckRequest{})
		checkCancel()
		if err == nil && resp.GetStatus() == healthpb.HealthCheckResponse_SERVING {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("routing-service: data-plane did not report SERVING within %s (last err=%v)", deadline, err)
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// MethodConfig describes one routing method entry in the routing-service config.
// Exactly one of Program (inline source) or File (path) is set; the harness
// only uses inline programs.
type MethodConfig struct {
	// Type is the provider type: "expr" or "script".
	Type string
	// Program is the inline source rendered under `program: |`.
	Program string
}

// ExprMethod is a convenience constructor for an inline expr routing method.
func ExprMethod(program string) MethodConfig {
	return MethodConfig{Type: "expr", Program: program}
}

type routingServiceConfig struct {
	Addr         string
	AdminAddr    string
	MetricsAddr  string
	DefaultGroup string
	Methods      []MethodConfig
}

// renderRoutingServiceConfig builds the routing-service YAML. It is intentionally
// hand-rendered (not text/template) so the multi-line `program:` blocks indent
// correctly under the YAML block scalar.
func renderRoutingServiceConfig(c routingServiceConfig) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "addr: %q\n", c.Addr)
	fmt.Fprintf(&b, "adminAddr: %q\n", c.AdminAddr)
	fmt.Fprintf(&b, "metricsAddr: %q\n", c.MetricsAddr)
	fmt.Fprintf(&b, "tracingEndpoint: %q\n", "")
	fmt.Fprintf(&b, "defaultRoutingGroup: %q\n", c.DefaultGroup)
	b.WriteString("methods:\n")
	for _, m := range c.Methods {
		fmt.Fprintf(&b, "  - type: %s\n", m.Type)
		b.WriteString("    program: |\n")
		for _, line := range splitLines(m.Program) {
			fmt.Fprintf(&b, "      %s\n", line)
		}
	}
	return b.String()
}

// splitLines splits s on newlines, dropping a single trailing empty line so the
// block scalar does not carry a spurious blank line.
func splitLines(s string) []string {
	lines := bytes.Split([]byte(s), []byte("\n"))
	out := make([]string, 0, len(lines))
	for i, l := range lines {
		if i == len(lines)-1 && len(l) == 0 {
			break
		}
		out = append(out, string(l))
	}
	return out
}
