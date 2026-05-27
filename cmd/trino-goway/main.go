// Package main is the trino-goway gateway entry point.
// It loads configuration, wires the composition root, starts the proxy and admin
// HTTP servers, and orchestrates graceful shutdown on SIGTERM/SIGINT.
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/hclincode/trino-goway/internal/admin"
	"github.com/hclincode/trino-goway/internal/auth"
	"github.com/hclincode/trino-goway/internal/config"
	"github.com/hclincode/trino-goway/internal/lifecycle"
	"github.com/hclincode/trino-goway/internal/monitor"
	"github.com/hclincode/trino-goway/internal/persistence"
	"github.com/hclincode/trino-goway/internal/proxy"
	"github.com/hclincode/trino-goway/internal/routing"
)

// shutdownTimeout bounds graceful shutdown after SIGTERM/SIGINT.
const shutdownTimeout = 30 * time.Second

// backendRefreshInterval is how often main reloads the backend list from the DB
// into the monitor and router. Independent of monitor probe cadence.
const backendRefreshInterval = 15 * time.Second

//go:embed all:web/dist
var webDistFS embed.FS

func main() {
	configPath := flag.String("config", "config.yml", "path to gateway YAML config")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	if err := run(*configPath, log); err != nil {
		log.Error("trino-goway: fatal", "err", err)
		os.Exit(1)
	}
}

// run is the composition root. Returns the first fatal error or nil on clean exit.
func run(configPath string, log *slog.Logger) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("trino-goway: load config: %w", err)
	}

	log.Info("trino-goway: starting",
		"configPath", configPath,
		"proxyPort", cfg.Proxy.Port,
		"adminPort", cfg.Admin.Port,
		"wireCompat", cfg.Cookie.WireCompat,
		"authType", cfg.Auth.Type,
		"routingType", cfg.Routing.Type,
	)

	rootCtx, rootCancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer rootCancel()

	// --- HTTP clients (Hard Invariant: three distinct clients with distinct concerns). ---
	proxyClient := &http.Client{
		Timeout: cfg.Proxy.RequestTimeout.D,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	monitorClient := &http.Client{
		Timeout: cfg.Monitor.CheckTimeout.D,
	}
	routerClient := &http.Client{
		Timeout: cfg.Routing.External.Timeout.D,
	}

	// --- Database + persistence. ---
	var db *sqlx.DB
	var backendDAO *persistence.BackendDAO
	var historyDAO *persistence.HistoryDAO
	if cfg.DB.Driver != "" {
		db, err = persistence.Open(rootCtx, cfg.DB)
		if err != nil {
			return fmt.Errorf("trino-goway: open db: %w", err)
		}
		defer func() {
			if cerr := db.Close(); cerr != nil {
				log.Warn("trino-goway: close db", "err", cerr)
			}
		}()
		backendDAO = persistence.NewBackendDAO(db)
		historyDAO = persistence.NewHistoryDAO(db)
	} else {
		return fmt.Errorf("trino-goway: db.driver must be configured")
	}

	// --- Monitor. ---
	mon := monitor.New(cfg.Monitor, monitorClient, log.With("component", "monitor"))

	// --- Routing. ---
	router, err := routing.New(routing.Config{
		Routing:        cfg.Routing,
		ExternalClient: routerClient,
		ProbeClient:    monitorClient,
		History:        historyDAO,
		Backends:       &activeBackendAdapter{dao: backendDAO},
		Log:            log.With("component", "routing"),
	})
	if err != nil {
		return fmt.Errorf("trino-goway: build router: %w", err)
	}

	// --- Auth middleware. ---
	authMW, authStop, err := buildAuthMiddleware(rootCtx, cfg.Auth, log.With("component", "auth"))
	if err != nil {
		return fmt.Errorf("trino-goway: build auth: %w", err)
	}
	if authStop != nil {
		defer authStop()
	}

	// --- Proxy. ---
	proxyHandler := proxy.New(proxy.Config{
		Proxy:  cfg.Proxy,
		Cookie: cfg.Cookie,
		Auth:   cfg.Auth,
		Client: proxyClient,
		Router: router,
		AuthMW: authMW,
		Log:    log.With("component", "proxy"),
	})

	// --- Admin. ---
	adminUIFS, err := fs.Sub(webDistFS, "web/dist")
	if err != nil {
		return fmt.Errorf("trino-goway: web dist sub fs: %w", err)
	}
	_ = adminUIFS // reserved for future static handler wiring; admin currently serves a placeholder

	startTime := time.Now()
	adminHandler := admin.New(admin.Config{
		Auth:      cfg.Auth,
		Backends:  backendDAO,
		History:   historyDAO,
		Monitor:   mon,
		StatusMut: mon,
		AuthMW:    authMW,
		Log:       log.With("component", "admin"),
		StartTime: startTime,
	})

	// --- HTTP servers. ---
	proxySrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Proxy.Port),
		Handler:           proxyHandler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	adminSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Admin.Port),
		Handler:           adminHandler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// --- Lifecycle. ---
	lc := lifecycle.New(proxySrv, adminSrv, log.With("component", "lifecycle"))

	// Bind sockets first so the ports are known before any probes run.
	if err := lc.Listen(); err != nil {
		return fmt.Errorf("trino-goway: bind listeners: %w", err)
	}

	// --- Start monitor + backend refresh. ---
	if err := refreshBackends(rootCtx, backendDAO, mon); err != nil {
		log.Warn("trino-goway: initial backend refresh failed", "err", err)
	}
	// Flip readyz only after the first probe cycle so we don't advertise
	// healthy before any backend health is known.
	mon.SetOnFirstTick(adminHandler.SetReady)
	if err := mon.Start(rootCtx); err != nil {
		return fmt.Errorf("trino-goway: start monitor: %w", err)
	}
	refreshDone := make(chan struct{})
	// goroutine exits when rootCtx is cancelled (signal received).
	go func() {
		defer close(refreshDone)
		runBackendRefresh(rootCtx, backendDAO, mon, log)
	}()

	startErr := lc.Start(rootCtx)

	// Tear down monitor + refresh loop. Use a fresh deadline because rootCtx is already cancelled.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := mon.Stop(shutdownCtx); err != nil {
		log.Warn("trino-goway: stop monitor", "err", err)
	}
	<-refreshDone

	if startErr != nil && !errors.Is(startErr, context.Canceled) {
		return fmt.Errorf("trino-goway: lifecycle: %w", startErr)
	}
	log.Info("trino-goway: stopped")
	return nil
}

// buildAuthMiddleware returns the configured auth middleware plus an optional
// stop function for background workers (e.g. OIDC JWKS refresher).
func buildAuthMiddleware(ctx context.Context, cfg config.AuthConfig, log *slog.Logger) (auth.Middleware, func(), error) {
	switch cfg.Type {
	case "OIDC":
		mw, err := auth.NewOIDC(ctx, cfg.OIDC, log)
		if err != nil {
			return nil, nil, err
		}
		return mw.Handler(), mw.Stop, nil
	case "LDAP":
		mw := auth.NewLDAP(cfg.LDAP, log)
		return mw.Handler(), nil, nil
	case "NOOP", "":
		return auth.Noop(), nil, nil
	default:
		return nil, nil, fmt.Errorf("auth: unknown type %q", cfg.Type)
	}
}

// runBackendRefresh periodically reloads backends from the DB and pushes the
// active set into the monitor so probes follow live configuration.
func runBackendRefresh(ctx context.Context, dao *persistence.BackendDAO, mon *monitor.Monitor, log *slog.Logger) {
	ticker := time.NewTicker(backendRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := refreshBackends(ctx, dao, mon); err != nil {
				log.Warn("trino-goway: backend refresh", "err", err)
			}
		}
	}
}

// refreshBackends loads active backends from the DB and updates the monitor.
func refreshBackends(ctx context.Context, dao *persistence.BackendDAO, mon *monitor.Monitor) error {
	backends, err := dao.ListActive(ctx)
	if err != nil {
		return err
	}
	monBackends := make([]monitor.Backend, len(backends))
	for i, b := range backends {
		monBackends[i] = monitor.SimpleBackend{Name: b.Name, URL: b.URL}
	}
	mon.SetBackends(monBackends)
	return nil
}

// activeBackendAdapter adapts *persistence.BackendDAO to routing.BackendLister.
type activeBackendAdapter struct {
	dao *persistence.BackendDAO
}

func (a *activeBackendAdapter) ListActive(ctx context.Context) ([]routing.ActiveBackend, error) {
	backends, err := a.dao.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]routing.ActiveBackend, len(backends))
	for i, b := range backends {
		out[i] = routing.ActiveBackend{
			Name:         b.Name,
			URL:          b.URL,
			RoutingGroup: b.RoutingGroup,
		}
	}
	return out, nil
}
