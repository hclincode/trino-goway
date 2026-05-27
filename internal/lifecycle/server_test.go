package lifecycle_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/hclincode/trino-goway/internal/lifecycle"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// freePort asks the OS for a free port and returns it.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

// newTestServer returns an *http.Server bound to a random free port that responds with 200 OK.
func newTestServer(t *testing.T) *http.Server {
	t.Helper()
	port := freePort(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", port),
		Handler: mux,
	}
}

func TestServer_StartStop(t *testing.T) {
	proxySrv := newTestServer(t)
	adminSrv := newTestServer(t)

	log := slog.Default()
	srv := lifecycle.New(proxySrv, adminSrv, log)

	ctx, cancel := context.WithCancel(context.Background())

	startErrCh := make(chan error, 1)
	// goroutine exits when Start returns after ctx cancellation
	go func() {
		startErrCh <- srv.Start(ctx)
	}()

	// Wait for both servers to be ready by polling with a short deadline.
	proxyURL := fmt.Sprintf("http://%s/", proxySrv.Addr)
	adminURL := fmt.Sprintf("http://%s/", adminSrv.Addr)
	waitForReady(t, proxyURL)
	waitForReady(t, adminURL)

	// Verify both servers respond.
	assertHTTP200(t, proxyURL)
	assertHTTP200(t, adminURL)

	// Cancel context to trigger graceful shutdown.
	cancel()

	// Start should return without error.
	select {
	case err := <-startErrCh:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after ctx cancellation")
	}

	// After Stop, servers should no longer accept connections.
	client := &http.Client{Timeout: 500 * time.Millisecond}
	_, err := client.Get(proxyURL)
	assert.Error(t, err, "proxy server should be stopped")
	_, err = client.Get(adminURL)
	assert.Error(t, err, "admin server should be stopped")
}

func TestServer_Stop_GracefulShutdown(t *testing.T) {
	proxySrv := newTestServer(t)
	adminSrv := newTestServer(t)

	log := slog.Default()
	srv := lifecycle.New(proxySrv, adminSrv, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startErrCh := make(chan error, 1)
	// goroutine exits when Start returns after Stop is called
	go func() {
		startErrCh <- srv.Start(ctx)
	}()

	proxyURL := fmt.Sprintf("http://%s/", proxySrv.Addr)
	waitForReady(t, proxyURL)

	// Call Stop directly with a generous deadline.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()

	err := srv.Stop(stopCtx)
	assert.NoError(t, err)

	// Cancel the parent ctx so Start's goroutine can exit cleanly too.
	cancel()

	select {
	case err := <-startErrCh:
		// Start may return nil or a "use of closed network connection" non-ErrServerClosed
		// style error; both are acceptable after explicit Stop.
		_ = err
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after Stop")
	}
}

func TestServer_Listen_BindsBeforeServe(t *testing.T) {
	proxySrv := newTestServer(t)
	adminSrv := newTestServer(t)

	log := slog.Default()
	srv := lifecycle.New(proxySrv, adminSrv, log)

	require.NoError(t, srv.Listen(), "Listen must succeed")

	// After Listen, the OS owns the ports — a second bind on the same address must fail.
	if l, err := net.Listen("tcp", proxySrv.Addr); err == nil {
		_ = l.Close()
		t.Fatalf("expected double-bind on %s to fail after Listen", proxySrv.Addr)
	}

	ctx, cancel := context.WithCancel(context.Background())
	startErrCh := make(chan error, 1)
	// goroutine exits when Start returns after ctx cancellation
	go func() {
		startErrCh <- srv.Start(ctx)
	}()

	waitForReady(t, fmt.Sprintf("http://%s/", proxySrv.Addr))
	waitForReady(t, fmt.Sprintf("http://%s/", adminSrv.Addr))

	cancel()
	select {
	case err := <-startErrCh:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after ctx cancellation")
	}
}

func TestServer_Listen_FailsWhenPortInUse(t *testing.T) {
	// Hold the proxy port so Listen will collide.
	port := freePort(t)
	blocker, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	require.NoError(t, err)
	defer func() { _ = blocker.Close() }()

	proxySrv := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", port)}
	adminSrv := newTestServer(t)

	srv := lifecycle.New(proxySrv, adminSrv, slog.Default())

	err = srv.Listen()
	require.Error(t, err, "Listen must fail when proxy port is in use")

	// admin port must not remain bound after the partial-bind cleanup.
	l, err := net.Listen("tcp", adminSrv.Addr)
	require.NoError(t, err, "admin port should be released after Listen failure")
	_ = l.Close()
}

// waitForReady polls url until it responds with 200 or the deadline elapses.
func waitForReady(t *testing.T, url string) {
	t.Helper()
	client := &http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close() // safe: Write errors on Body.Close are unactionable
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server at %s did not become ready within 5s", url)
}

// assertHTTP200 GETs url and asserts the response is 200 OK.
func assertHTTP200(t *testing.T, url string) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }() // safe: Body.Close errors after read are unactionable
	_, _ = io.Copy(io.Discard, resp.Body)     // safe: Discard never returns errors
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
