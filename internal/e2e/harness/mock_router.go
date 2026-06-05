//go:build e2e

package harness

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/hclincode/trino-goway/internal/testutil"
)

// MockGRPCRouter owns the lifecycle of a cmd/mock-external-router-grpc
// subprocess. The mock implements TrinoGatewayRouter and returns a fixed
// routing group for every request, exercising the same gRPC contract as the
// real routing-service. Used by the parity test to confirm the gateway treats
// any conformant TrinoGatewayRouter interchangeably.
type MockGRPCRouter struct {
	// GRPCAddr is the data-plane address (host:port) — pass to
	// WithExternalGRPCRouter.
	GRPCAddr string

	cmd *exec.Cmd
}

// StartMockGRPCRouter builds the cmd/mock-external-router-grpc binary, launches
// it on a freshly-allocated port configured to return the given fixed group,
// and waits until its gRPC listener accepts connections. A t.Cleanup SIGTERMs
// the subprocess (5s grace, then SIGKILL).
func StartMockGRPCRouter(t testing.TB, group string) *MockGRPCRouter {
	t.Helper()

	port := testutil.FreePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	bin := mockGRPCRouterBinaryPath(t)

	ctx, cancel := context.WithCancel(context.Background())

	cmd := exec.CommandContext(ctx, bin, "--addr", addr, "--group", group)
	cmd.Stdout = &logWriter{t: t, prefix: "mock-router[stdout]"}
	cmd.Stderr = &logWriter{t: t, prefix: "mock-router[stderr]"}
	cmd.SysProcAttr = procAttrNewPgrp()

	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("mock-router: start binary %s: %v", bin, err)
	}

	m := &MockGRPCRouter{GRPCAddr: addr, cmd: cmd}

	t.Cleanup(func() {
		shutdown(t, cmd)
		cancel()
	})

	waitTCPAccept(t, addr, 30*time.Second)
	return m
}

// mockGRPCRouterBinaryPath returns a path to the mock-external-router-grpc
// executable. If TRINO_GOWAY_MOCK_ROUTER_BIN is set it is used verbatim;
// otherwise the binary is built into t.TempDir() from the gateway module.
func mockGRPCRouterBinaryPath(t testing.TB) string {
	t.Helper()

	if env := os.Getenv("TRINO_GOWAY_MOCK_ROUTER_BIN"); env != "" {
		if _, err := os.Stat(env); err != nil {
			t.Fatalf("mock-router: TRINO_GOWAY_MOCK_ROUTER_BIN=%s: %v", env, err)
		}
		return env
	}

	out := filepath.Join(t.TempDir(), "mock-external-router-grpc")
	build := exec.Command("go", "build", "-o", out, "./cmd/mock-external-router-grpc")
	build.Dir = projectRoot(t)
	var stderr bytes.Buffer
	build.Stderr = &stderr
	if err := build.Run(); err != nil {
		t.Fatalf("mock-router: build: %v\n%s", err, stderr.String())
	}
	return out
}

// waitTCPAccept polls addr until a TCP connection is accepted (the gRPC
// listener is up) or the deadline elapses. The mock router has no gRPC health
// service, so a successful dial is the readiness signal.
func waitTCPAccept(t testing.TB, addr string, deadline time.Duration) {
	t.Helper()

	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		conn, err := net.DialTimeout("tcp", addr, 1*time.Second)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("mock-router: %s did not accept connections within %s", addr, deadline)
}
