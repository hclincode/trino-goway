package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"

	"golang.org/x/sync/errgroup"
)

// Server manages coordinated startup and graceful shutdown of the proxy and admin HTTP servers.
type Server struct {
	proxy *http.Server
	admin *http.Server
	log   *slog.Logger

	proxyLn net.Listener
	adminLn net.Listener
}

// New creates a Server that manages proxySrv and adminSrv.
func New(proxySrv, adminSrv *http.Server, log *slog.Logger) *Server {
	return &Server{
		proxy: proxySrv,
		admin: adminSrv,
		log:   log,
	}
}

// Listen binds both servers' listening sockets without serving traffic yet.
// Returns an error if either bind fails (and closes any partially-acquired socket).
// Callers that need to know when the listeners are bound (e.g., to flip a readiness
// gate) should call Listen before Start. Start will lazily call Listen if it has
// not been called already.
func (s *Server) Listen() error {
	if s.proxyLn != nil && s.adminLn != nil {
		return nil
	}
	proxyLn, err := net.Listen("tcp", s.proxy.Addr)
	if err != nil {
		return fmt.Errorf("lifecycle: bind proxy %s: %w", s.proxy.Addr, err)
	}
	adminLn, err := net.Listen("tcp", s.admin.Addr)
	if err != nil {
		_ = proxyLn.Close()
		return fmt.Errorf("lifecycle: bind admin %s: %w", s.admin.Addr, err)
	}
	s.proxyLn = proxyLn
	s.adminLn = adminLn
	return nil
}

// Start starts both servers concurrently. It blocks until ctx is cancelled, then calls Stop.
// Returns the first non-ErrServerClosed startup error, or the error from Stop.
// If Listen has not been called, Start calls it first; bind failures are returned
// before any goroutines are launched.
func (s *Server) Start(ctx context.Context) error {
	if err := s.Listen(); err != nil {
		return err
	}

	// errCh receives the first fatal server error from either Serve goroutine.
	errCh := make(chan error, 2)

	// goroutine exits when Serve returns (either on Shutdown or fatal error)
	go func() {
		s.log.Info("proxy server starting", "addr", s.proxy.Addr)
		if err := s.proxy.Serve(s.proxyLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		} else {
			errCh <- nil
		}
	}()

	// goroutine exits when Serve returns (either on Shutdown or fatal error)
	go func() {
		s.log.Info("admin server starting", "addr", s.admin.Addr)
		if err := s.admin.Serve(s.adminLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		} else {
			errCh <- nil
		}
	}()

	select {
	case err := <-errCh:
		// One server failed to start; shut down the other and return the error.
		_ = s.Stop(ctx) // best-effort; original error is the meaningful one
		return err
	case <-ctx.Done():
		// Normal shutdown path: context was cancelled.
		return s.Stop(ctx)
	}
}

// Stop gracefully shuts down both servers using the provided context deadline.
func (s *Server) Stop(ctx context.Context) error {
	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		if err := s.proxy.Shutdown(gCtx); err != nil {
			return err
		}
		s.log.Info("proxy server stopped")
		return nil
	})

	g.Go(func() error {
		if err := s.admin.Shutdown(gCtx); err != nil {
			return err
		}
		s.log.Info("admin server stopped")
		return nil
	})

	return g.Wait()
}
