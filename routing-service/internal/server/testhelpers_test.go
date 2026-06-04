package server_test

import (
	"context"
	"log/slog"
	"net"
	"testing"

	"github.com/hclincode/trino-goway-routing-service/internal/server"
)

// newTestLogger returns a slog.Logger that discards output in tests.
func newTestLogger(_ *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(nil_writer{}, &slog.HandlerOptions{Level: slog.LevelError}))
}

type nil_writer struct{}

func (nil_writer) Write(p []byte) (int, error) { return len(p), nil }

// startOnFreePort binds a TCP listener on :0, calls server.StartOnListener,
// and returns the bound address plus a stop function.
// The stop function calls server.Stop() and waits for Start to return.
func startOnFreePort(t *testing.T, srv *server.Server) (addr string, stop func()) {
	t.Helper()

	lis, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr = lis.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.StartOnListener(ctx, lis); err != nil {
			// Ignore error after cancellation.
			t.Logf("server.StartOnListener: %v", err)
		}
	}()

	stop = func() {
		cancel()
		<-done
	}
	t.Cleanup(stop)
	return addr, stop
}
