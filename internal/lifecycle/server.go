package lifecycle

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"golang.org/x/sync/errgroup"
)

// Server manages coordinated startup and graceful shutdown of the proxy and admin HTTP servers.
type Server struct {
	proxy *http.Server
	admin *http.Server
	log   *slog.Logger
}

// New creates a Server that manages proxySrv and adminSrv.
func New(proxySrv, adminSrv *http.Server, log *slog.Logger) *Server {
	return &Server{
		proxy: proxySrv,
		admin: adminSrv,
		log:   log,
	}
}

// Start starts both servers concurrently. It blocks until ctx is cancelled, then calls Stop.
// Returns the first non-ErrServerClosed startup error, or the error from Stop.
func (s *Server) Start(ctx context.Context) error {
	// errCh receives the first fatal server error from either ListenAndServe goroutine.
	errCh := make(chan error, 2)

	// goroutine exits when ListenAndServe returns (either on Shutdown or fatal error)
	go func() {
		s.log.Info("proxy server starting", "addr", s.proxy.Addr)
		if err := s.proxy.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		} else {
			errCh <- nil
		}
	}()

	// goroutine exits when ListenAndServe returns (either on Shutdown or fatal error)
	go func() {
		s.log.Info("admin server starting", "addr", s.admin.Addr)
		if err := s.admin.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
