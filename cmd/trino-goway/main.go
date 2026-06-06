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
	"github.com/hclincode/trino-goway/internal/clusterstats"
	"github.com/hclincode/trino-goway/internal/config"
	"github.com/hclincode/trino-goway/internal/lifecycle"
	"github.com/hclincode/trino-goway/internal/metrics"
	"github.com/hclincode/trino-goway/internal/monitor"
	"github.com/hclincode/trino-goway/internal/persistence"
	"github.com/hclincode/trino-goway/internal/proxy"
	"github.com/hclincode/trino-goway/internal/routing"
)

// shutdownTimeout bounds graceful shutdown after SIGTERM/SIGINT.
const shutdownTimeout = 30 * time.Second

// defaultBackendRefreshInterval is the fallback DB→monitor reload cadence used
// when monitor.refreshInterval is unset (config.applyDefaults normally fills it).
const defaultBackendRefreshInterval = 15 * time.Second

// Result labels for backend-refresh metrics.
const (
	metricsResultOK    = "ok"
	metricsResultError = "error"
)

//go:embed all:web/dist
var webDistFS embed.FS

// Compile-time assertion that the metrics implementations satisfy the consumer
// interfaces they are injected into.
var (
	_ proxy.MetricsRecorder  = (*metrics.ProxyMetrics)(nil)
	_ monitor.StatusObserver = (*metrics.BackendMetrics)(nil)
	_ routing.RouterMetrics  = (*metrics.RouterMetrics)(nil)
	_ auth.Metrics           = (*metrics.AuthMetrics)(nil)
	_ persistence.Metrics    = (*metrics.PersistenceMetrics)(nil)
	// The stats store is both the monitor's stats observer (write side) and the
	// admin's stats provider (read side).
	_ monitor.StatsObserver = (*clusterstats.StatsStore)(nil)
	_ admin.StatsProvider   = (*clusterstats.StatsStore)(nil)
)

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

	// --- HTTP clients (Hard Invariant #12: distinct clients with distinct concerns). ---
	// Four pools, never shared:
	//   1. proxyClient   — POST /v1/statement and statement-path forwarding.
	//   2. monitorClient — /v1/info health probes.
	//   3. routerClient  — external routing-service calls (follows redirects).
	//   4. statsClient   — cluster-stats collection (UI_API/METRICS only);
	//      constructed only when ClusterStats.MonitorType needs outbound HTTP, so
	//      the default INFO_API/NOOP path stays at three live pools. The UI_API
	//      cookie jar lives inside the collector, not on this shared transport (R5).
	// Pool isolation prevents backpressure on one path starving the others.
	proxyClient := &http.Client{
		Timeout: cfg.Proxy.RequestTimeout.D,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	monitorClient := &http.Client{
		Timeout: cfg.Monitor.CheckTimeout.D,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	routerClient := &http.Client{
		Timeout: cfg.Routing.External.Timeout.D,
	}
	// statsClient is built lazily — only the UI_API/METRICS collectors issue stats
	// HTTP; INFO_API/NOOP reuse the monitor verdict and need no client.
	var statsClient *http.Client
	if mt := cfg.ClusterStats.MonitorType; mt == "UI_API" || mt == "METRICS" {
		statsClient = &http.Client{
			Timeout: cfg.Monitor.StatsTimeout.D,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}

	// --- Metrics registry (explicit construction; no global registry). ---
	metricsReg := metrics.New()
	if err := metricsReg.RegisterRuntime(); err != nil {
		return fmt.Errorf("trino-goway: register runtime metrics: %w", err)
	}
	httpMetrics, err := metrics.NewHTTPMetrics(metricsReg.Registerer())
	if err != nil {
		return fmt.Errorf("trino-goway: register http metrics: %w", err)
	}
	proxyMetrics, err := metrics.NewProxyMetrics(metricsReg.Registerer())
	if err != nil {
		return fmt.Errorf("trino-goway: register proxy metrics: %w", err)
	}
	backendMetrics, err := metrics.NewBackendMetrics(metricsReg.Registerer())
	if err != nil {
		return fmt.Errorf("trino-goway: register backend metrics: %w", err)
	}
	routerMetrics, err := metrics.NewRouterMetrics(metricsReg.Registerer())
	if err != nil {
		return fmt.Errorf("trino-goway: register router metrics: %w", err)
	}
	authMetrics, err := metrics.NewAuthMetrics(metricsReg.Registerer())
	if err != nil {
		return fmt.Errorf("trino-goway: register auth metrics: %w", err)
	}
	persistenceMetrics, err := metrics.NewPersistenceMetrics(metricsReg.Registerer())
	if err != nil {
		return fmt.Errorf("trino-goway: register persistence metrics: %w", err)
	}

	// --- Database + persistence. ---
	var db *sqlx.DB
	var backendDAO *persistence.BackendDAO
	var historyDAO *persistence.HistoryDAO
	if cfg.DB.Driver != "" {
		db, err = persistence.Open(rootCtx, cfg.DB)
		if err != nil {
			persistenceMetrics.SetDBUp(false)
			return fmt.Errorf("trino-goway: open db: %w", err)
		}
		persistenceMetrics.SetDBUp(true)
		defer func() {
			if cerr := db.Close(); cerr != nil {
				log.Warn("trino-goway: close db", "err", cerr)
			}
		}()
		backendDAO = persistence.NewBackendDAO(db)
		historyDAO = persistence.NewHistoryDAO(db, persistenceMetrics)
	} else {
		return fmt.Errorf("trino-goway: db.driver must be configured")
	}

	// --- Monitor. ---
	mon := monitor.New(cfg.Monitor, monitorClient, log.With("component", "monitor"))
	mon.SetObserver(backendMetrics)

	// --- Cluster stats (UC-MON-02 / M7). ---
	// The collector rides the monitor tick and publishes a per-tick name-keyed
	// snapshot into statsStore, which the admin layer reads. INFO_API (default)
	// and NOOP reuse mon.Status (no extra HTTP); UI_API/METRICS use statsClient.
	statsStore := clusterstats.NewStatsStore()
	collector, err := clusterstats.NewCollector(
		cfg.ClusterStats,
		cfg.Monitor,
		cfg.BackendState,
		mon.Status,
		statsClient,
		log.With("component", "clusterstats"),
	)
	if err != nil {
		return fmt.Errorf("trino-goway: build cluster-stats collector: %w", err)
	}
	mon.SetClusterStatsCollector(collector)
	mon.SetStatsObserver(statsStore)
	log.Info("trino-goway: cluster stats configured", "clusterStatsMonitor", cfg.ClusterStats.MonitorType)

	// --- Routing. ---
	router, err := routing.New(routing.Config{
		Routing:        cfg.Routing,
		ExternalClient: routerClient,
		ProbeClient:    monitorClient,
		History:        historyDAO,
		Backends:       &activeBackendAdapter{dao: backendDAO},
		Metrics:        routerMetrics,
		Log:            log.With("component", "routing"),
	})
	if err != nil {
		return fmt.Errorf("trino-goway: build router: %w", err)
	}

	// --- Auth middleware. ---
	authMW, authStop, err := buildAuthMiddleware(rootCtx, cfg.Auth, authMetrics, log.With("component", "auth"))
	if err != nil {
		return fmt.Errorf("trino-goway: build auth: %w", err)
	}
	if authStop != nil {
		defer authStop()
	}

	// --- OIDC Web-UI login (authorization-code flow). ---
	// Constructed only when OIDC auth is configured with a redirect URL; the
	// gateway still boots (and the /sso handler reports "not configured") when it
	// is absent.
	var webLogin admin.OIDCWebLogin
	if cfg.Auth.Type == "OIDC" && cfg.Auth.OIDC.RedirectURL != "" {
		oidcClient := &http.Client{Timeout: 10 * time.Second}
		wl, werr := auth.NewOIDCWebLogin(rootCtx, cfg.Auth.OIDC, oidcClient)
		if werr != nil {
			return fmt.Errorf("trino-goway: build oidc web login: %w", werr)
		}
		webLogin = wl
	}

	// --- Proxy. ---
	var proxyHistory proxy.HistoryRecorder
	if historyDAO != nil {
		proxyHistory = &historyAdapter{dao: historyDAO, backends: backendDAO}
	}
	proxyHandler := proxy.New(proxy.Config{
		Proxy:           cfg.Proxy,
		Cookie:          cfg.Cookie,
		Auth:            cfg.Auth,
		Client:          proxyClient,
		Router:          router,
		History:         proxyHistory,
		AuthMW:          authMW,
		Metrics:         httpMetrics.Middleware("proxy"),
		MetricsRecorder: proxyMetrics,
		Log:             log.With("component", "proxy"),
	})

	// --- Admin. ---
	adminUIFS, err := fs.Sub(webDistFS, "web/dist")
	if err != nil {
		return fmt.Errorf("trino-goway: web dist sub fs: %w", err)
	}

	startTime := time.Now()
	adminHandler := admin.New(admin.Config{
		Auth:         cfg.Auth,
		Backends:     backendDAO,
		History:      historyDAO,
		Monitor:      mon,
		StatusMut:    mon,
		Stats:        statsStore,
		AuthMW:       authMW,
		UIFS:         adminUIFS,
		WebLogin:     webLogin,
		DisablePages: cfg.UI.DisablePages,
		Log:          log.With("component", "admin"),
		StartTime:    startTime,
		Metrics: admin.MetricsConfig{
			Enabled: cfg.Metrics.Enabled,
			Path:    cfg.Metrics.Path,
			Handler: metricsReg.Handler(),
		},
		MetricsMW: httpMetrics.Middleware("admin"),
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
	if err := refreshBackends(rootCtx, backendDAO, mon, persistenceMetrics); err != nil {
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
		runBackendRefresh(rootCtx, cfg.Monitor.RefreshInterval.D, backendDAO, mon, persistenceMetrics, log)
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
func buildAuthMiddleware(ctx context.Context, cfg config.AuthConfig, am auth.Metrics, log *slog.Logger) (auth.Middleware, func(), error) {
	switch cfg.Type {
	case "OIDC":
		mw, err := auth.NewOIDC(ctx, cfg.OIDC, log, am)
		if err != nil {
			return nil, nil, err
		}
		return mw.Handler(), mw.Stop, nil
	case "LDAP":
		mw := auth.NewLDAP(cfg.LDAP, log, am)
		return mw.Handler(), nil, nil
	case "NOOP", "":
		return auth.Noop(am), nil, nil
	default:
		return nil, nil, fmt.Errorf("auth: unknown type %q", cfg.Type)
	}
}

// runBackendRefresh periodically reloads backends from the DB and pushes the
// active set into the monitor so probes follow live configuration.
func runBackendRefresh(ctx context.Context, interval time.Duration, dao *persistence.BackendDAO, mon *monitor.Monitor, pm *metrics.PersistenceMetrics, log *slog.Logger) {
	if interval <= 0 {
		interval = defaultBackendRefreshInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := refreshBackends(ctx, dao, mon, pm); err != nil {
				log.Warn("trino-goway: backend refresh", "err", err)
			}
		}
	}
}

// refreshBackends loads active backends from the DB and updates the monitor.
// It records the refresh outcome and database reachability on pm.
func refreshBackends(ctx context.Context, dao *persistence.BackendDAO, mon *monitor.Monitor, pm *metrics.PersistenceMetrics) error {
	backends, err := dao.ListActive(ctx)
	if err != nil {
		pm.BackendRefresh(metricsResultError)
		pm.SetDBUp(false)
		return err
	}
	pm.BackendRefresh(metricsResultOK)
	pm.SetDBUp(true)
	monBackends := make([]monitor.Backend, len(backends))
	for i, b := range backends {
		// Populate ExternalURL/RoutingGroup so the backend satisfies
		// clusterstats.Backend with a fully-populated identity for collectors.
		monBackends[i] = monitor.SimpleBackend{
			Name:         b.Name,
			URL:          b.URL,
			ExternalURL:  b.ExternalURL,
			RoutingGroup: b.RoutingGroup,
		}
	}
	mon.SetBackends(monBackends)
	return nil
}

// activeBackendAdapter adapts *persistence.BackendDAO to routing.BackendLister.
type activeBackendAdapter struct {
	dao *persistence.BackendDAO
}

// historyAdapter adapts *persistence.HistoryDAO to proxy.HistoryRecorder.
// It stamps external_url at capture time by resolving the routed backend's
// external URL (falling back to the backend URL), matching Java's
// ProxyRequestHandler which sets queryDetail.externalUrl from the routing
// destination before submitting it.
type historyAdapter struct {
	dao      *persistence.HistoryDAO
	backends *persistence.BackendDAO
}

func (h *historyAdapter) Insert(ctx context.Context, queryID, backendURL, userName, source string) error {
	externalURL, err := h.backends.LookupExternalURL(ctx, backendURL)
	if err != nil {
		// A failed lookup must not drop the history record; fall back to the
		// backend URL, which is the same value Java emits when externalUrl is unset.
		externalURL = backendURL
	}
	return h.dao.Insert(ctx, persistence.QueryRecord{
		QueryID:     queryID,
		BackendURL:  backendURL,
		ExternalURL: externalURL,
		UserName:    userName,
		Source:      source,
	})
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
