package admin

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/hclincode/trino-goway/internal/auth"
	"github.com/hclincode/trino-goway/internal/clusterstats"
	"github.com/hclincode/trino-goway/internal/config"
	"github.com/hclincode/trino-goway/internal/monitor"
	"github.com/hclincode/trino-goway/internal/persistence"
)

// BackendStore is the admin package's interface for backend persistence.
type BackendStore interface {
	List(ctx context.Context) ([]persistence.Backend, error)
	ListActive(ctx context.Context) ([]persistence.Backend, error)
	Upsert(ctx context.Context, b persistence.Backend) error
	Delete(ctx context.Context, name string) error
	SetActive(ctx context.Context, name string, active bool) error
}

// HistoryStore is the admin package's interface for query history.
type HistoryStore interface {
	Insert(ctx context.Context, r persistence.QueryRecord) error
	ListRecent(ctx context.Context, limit int) ([]persistence.QueryRecord, error)
	FindByFilter(ctx context.Context, filter persistence.HistoryFilter) ([]persistence.QueryRecord, int64, error)
	FindDistribution(ctx context.Context, since time.Time) ([]persistence.DistributionBucket, error)
}

// StatusProvider is the admin package's interface for backend health status.
type StatusProvider interface {
	Status(url string) monitor.TrinoStatus
}

// StatsProvider is the admin package's interface for live per-backend cluster
// stats, looked up by backend NAME. Optional on admin.Config: when nil the
// admin layer serves zero counts (INFO_API parity). Satisfied by
// *clusterstats.StatsStore.
type StatsProvider interface {
	Stats(name string) clusterstats.ClusterStats
}

// StatusUpdater is the admin package's interface for setting backend health status directly.
type StatusUpdater interface {
	SetBackendStatus(url string, status monitor.TrinoStatus)
}

// OIDCWebLogin is the admin package's interface for the interactive Web-UI
// OAuth2 authorization-code flow. Satisfied by *auth.OIDCWebLogin.
type OIDCWebLogin interface {
	// AuthCodeURL returns the IdP authorization URL for the given state and nonce.
	AuthCodeURL(state, nonce string) string
	// Exchange swaps the authorization code for a validated id_token, checking
	// the nonce claim when nonce is non-empty.
	Exchange(ctx context.Context, code, nonce string) (string, error)
}

// Config holds constructor dependencies for Admin.
type Config struct {
	Auth      config.AuthConfig
	Backends  BackendStore
	History   HistoryStore
	Monitor   StatusProvider
	StatusMut StatusUpdater // optional; used by entity upsert
	Stats     StatsProvider // optional; nil ⇒ counts 0 (INFO_API parity)
	AuthMW    auth.Middleware
	Log       *slog.Logger
	StartTime time.Time // process start time (for DistributionResponse.startTime)

	// UIFS is the embedded web UI bundle (the contents of web/dist), served under
	// the /trino-gateway base path. When nil, the static handlers fall back to a
	// minimal placeholder so the gateway still boots without a UI build.
	UIFS fs.FS

	// WebLogin drives the interactive Web-UI OAuth2 login flow (/sso and
	// /oidc/callback). Non-nil only when Auth.Type is "OIDC" and the flow is
	// configured; when nil those handlers report that SSO is not configured.
	WebLogin OIDCWebLogin

	// DisablePages lists UI page keys globally hidden from the sidebar, surfaced
	// verbatim by getUIConfiguration (from config ui.disablePages).
	DisablePages []string

	// Metrics holds the Prometheus exposition config and handler. When
	// Metrics.Enabled is false, or Metrics.Handler is nil, no /metrics route is
	// registered (the path returns 404). The route is unauthenticated, matching
	// the health probes, since it is mounted on the admin listener only.
	Metrics MetricsConfig

	// MetricsMW is an optional HTTP server metrics middleware (from
	// metrics.HTTPMetrics.Middleware). When non-nil it wraps every admin route.
	MetricsMW func(http.Handler) http.Handler
}

// MetricsConfig carries the metrics exposition route for the admin server.
type MetricsConfig struct {
	Enabled bool
	Path    string
	Handler http.Handler
}

// Admin implements the gateway management HTTP API.
type Admin struct {
	cfg    Config
	router chi.Router
	ready  atomic.Bool
}

// New constructs a new Admin and registers all routes.
func New(cfg Config) *Admin {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.StartTime.IsZero() {
		cfg.StartTime = time.Now()
	}
	a := &Admin{cfg: cfg}
	a.router = a.buildRouter()
	return a
}

// SetReady marks the admin as ready (used by the composition root after the first monitor cycle).
func (a *Admin) SetReady() {
	a.ready.Store(true)
}

// ServeHTTP implements http.Handler.
func (a *Admin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.router.ServeHTTP(w, r)
}

// buildRouter constructs the chi router with all route registrations.
func (a *Admin) buildRouter() chi.Router {
	r := chi.NewRouter()
	// Metrics middleware first so it observes the full request lifecycle (including
	// the recoverer) and reads the matched route pattern after handlers run.
	if a.cfg.MetricsMW != nil {
		r.Use(a.cfg.MetricsMW)
	}
	r.Use(middleware.Recoverer)

	// Redirect / to /trino-gateway
	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/trino-gateway", http.StatusSeeOther)
	})

	// Health probes (no auth)
	r.Get("/trino-gateway/livez", a.handleLivez)
	r.Get("/trino-gateway/readyz", a.handleReadyz)

	// Prometheus metrics (no auth, admin listener only). Registered only when
	// enabled; otherwise the path falls through to the chi 404 handler.
	if a.cfg.Metrics.Enabled && a.cfg.Metrics.Handler != nil {
		r.Method(http.MethodGet, a.cfg.Metrics.Path, a.cfg.Metrics.Handler)
	}

	// Static web UI (no auth)
	r.Get("/trino-gateway", a.serveIndex)
	r.Get("/trino-gateway/logo.svg", a.serveLogoSVG)
	r.Get("/trino-gateway/assets/*", a.serveAssets)

	// Public backend endpoints (no auth)
	r.Get("/api/public/backends", a.listPublicBackends)
	r.Get("/api/public/backends/{name}", a.getPublicBackend)
	r.Get("/api/public/backends/{name}/state", a.getPublicBackendState)

	// Auth endpoints (no role required, but auth MW applied)
	r.Group(func(r chi.Router) {
		if a.cfg.AuthMW != nil {
			r.Use(a.cfg.AuthMW)
		}
		r.Post("/sso", a.handleSSO)
		r.Get("/oidc/callback", a.handleOIDCCallback)
		r.Post("/login", a.handleLogin)
		r.Post("/logout", a.handleLogout)
		r.Post("/loginType", a.handleLoginType)
	})

	// Gateway endpoints (API role required)
	r.Group(func(r chi.Router) {
		if a.cfg.AuthMW != nil {
			r.Use(a.cfg.AuthMW)
		}
		r.Use(auth.RequireRole(auth.RoleAPI, a.cfg.Auth.Authorization))
		r.Get("/gateway", a.handleGatewayPing)
		r.Get("/gateway/backend/all", a.listAllBackends)
		r.Get("/gateway/backend/active", a.listActiveBackends)
		r.Post("/gateway/backend/activate/{name}", a.activateBackend)
		r.Post("/gateway/backend/deactivate/{name}", a.deactivateBackend)
		r.Post("/gateway/backend/modify/add", a.addBackend)
		r.Post("/gateway/backend/modify/update", a.updateBackend)
		r.Post("/gateway/backend/modify/delete", a.deleteBackend)
	})

	// Entity endpoints (ADMIN role required)
	r.Group(func(r chi.Router) {
		if a.cfg.AuthMW != nil {
			r.Use(a.cfg.AuthMW)
		}
		r.Use(auth.RequireRole(auth.RoleAdmin, a.cfg.Auth.Authorization))
		r.Get("/entity", a.listEntityTypes)
		r.Post("/entity", a.upsertEntity)
		r.Get("/entity/{entityType}", a.listEntities)
	})

	// User info (USER role required)
	r.Group(func(r chi.Router) {
		if a.cfg.AuthMW != nil {
			r.Use(a.cfg.AuthMW)
		}
		r.Use(auth.RequireRole(auth.RoleUser, a.cfg.Auth.Authorization))
		r.Post("/userinfo", a.handleUserinfo)
	})

	// Query history & active backends (USER role required)
	r.Group(func(r chi.Router) {
		if a.cfg.AuthMW != nil {
			r.Use(a.cfg.AuthMW)
		}
		r.Use(auth.RequireRole(auth.RoleUser, a.cfg.Auth.Authorization))
		r.Get("/trino-gateway/api/queryHistory", a.queryHistory)
		r.Get("/trino-gateway/api/activeBackends", a.legacyActiveBackends)
		r.Get("/trino-gateway/api/queryHistoryDistribution", a.queryHistoryDistribution)
	})

	// Webapp endpoints (USER role required, ADMIN for write operations)
	r.Group(func(r chi.Router) {
		if a.cfg.AuthMW != nil {
			r.Use(a.cfg.AuthMW)
		}
		r.Use(auth.RequireRole(auth.RoleUser, a.cfg.Auth.Authorization))
		r.Post("/webapp/getAllBackends", a.webappGetAllBackends)
		r.Post("/webapp/findQueryHistory", a.webappFindQueryHistory)
		r.Post("/webapp/getDistribution", a.webappGetDistribution)
		r.Post("/webapp/getUIConfiguration", a.webappGetUIConfig)
	})

	// Webapp ADMIN-only endpoints
	r.Group(func(r chi.Router) {
		if a.cfg.AuthMW != nil {
			r.Use(a.cfg.AuthMW)
		}
		r.Use(auth.RequireRole(auth.RoleAdmin, a.cfg.Auth.Authorization))
		r.Post("/webapp/getRoutingRules", a.webappGetRoutingRules)
		r.Post("/webapp/updateRoutingRules", a.webappUpdateRoutingRules)
		r.Post("/webapp/saveBackend", a.webappSaveBackend)
		r.Post("/webapp/updateBackend", a.webappUpdateBackend)
		r.Post("/webapp/deleteBackend", a.webappDeleteBackend)
	})

	// SPA deep-link fallback (no auth): any unmatched GET under the /trino-gateway
	// base path serves index.html so the browser router can resolve client-side
	// routes (e.g. /trino-gateway/dashboard). Registered last; chi resolves the
	// more specific API/asset/probe patterns above before this wildcard, so it
	// never shadows real routes.
	r.Get("/trino-gateway/*", a.serveIndex)

	return r
}

// placeholderIndex is served when no embedded UI bundle is present (UIFS is nil
// or has no index.html), so the gateway still boots without a UI build.
const placeholderIndex = `<!DOCTYPE html><html><body>Trino Gateway</body></html>`

// placeholderLogo is the SVG fallback when the bundle has no logo.svg.
const placeholderLogo = `<svg xmlns="http://www.w3.org/2000/svg"/>`

// serveIndex serves the web UI shell (index.html) from the embedded bundle.
// It also backs the SPA deep-link fallback, so it always returns 200 with the
// shell HTML; the browser router resolves the client-side route.
func (a *Admin) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The shell must never be cached: it references content-hashed assets that
	// change on every build.
	w.Header().Set("Cache-Control", "no-cache")

	data, err := a.readUIFile("index.html")
	if err != nil {
		_, _ = w.Write([]byte(placeholderIndex))
		return
	}
	_, _ = w.Write(data)
}

// serveLogoSVG serves logo.svg from the embedded bundle, falling back to a
// minimal placeholder when absent.
func (a *Admin) serveLogoSVG(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")

	data, err := a.readUIFile("logo.svg")
	if err != nil {
		_, _ = w.Write([]byte(placeholderLogo))
		return
	}
	_, _ = w.Write(data)
}

// serveAssets serves content-hashed static assets under /trino-gateway/assets/*
// from the embedded bundle. Missing assets return 404 (never the SPA shell, so
// a broken asset reference does not masquerade as HTML).
func (a *Admin) serveAssets(w http.ResponseWriter, r *http.Request) {
	if a.cfg.UIFS == nil {
		http.NotFound(w, r)
		return
	}

	rest := chi.URLParam(r, "*")
	// path.Clean collapses any ".." segments to a single rooted path, so the
	// join below cannot escape the assets/ subtree.
	name := path.Join("assets", path.Clean("/"+rest))
	name = strings.TrimPrefix(name, "/")

	f, err := a.cfg.UIFS.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}

	rs, ok := f.(io.ReadSeeker)
	if !ok {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Content-hashed asset names are immutable; cache aggressively.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, info.Name(), info.ModTime(), rs)
}

// readUIFile reads a file from the embedded UI bundle. Returns an error when the
// bundle is absent (UIFS nil) or the file does not exist, so callers can fall
// back to a placeholder.
func (a *Admin) readUIFile(name string) ([]byte, error) {
	if a.cfg.UIFS == nil {
		return nil, fs.ErrNotExist
	}
	data, err := fs.ReadFile(a.cfg.UIFS, name)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			a.cfg.Log.Warn("admin: read ui file", "name", name, "err", err)
		}
		return nil, err
	}
	return data, nil
}
