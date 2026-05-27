package admin

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/hclincode/trino-goway/internal/auth"
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
}

// StatusProvider is the admin package's interface for backend health status.
type StatusProvider interface {
	Status(url string) monitor.TrinoStatus
}

// StatusUpdater is the admin package's interface for setting backend health status directly.
type StatusUpdater interface {
	SetBackendStatus(url string, status monitor.TrinoStatus)
}

// Config holds constructor dependencies for Admin.
type Config struct {
	Auth      config.AuthConfig
	Backends  BackendStore
	History   HistoryStore
	Monitor   StatusProvider
	StatusMut StatusUpdater // optional; used by entity upsert
	AuthMW    auth.Middleware
	Log       *slog.Logger
	StartTime time.Time // process start time (for DistributionResponse.startTime)
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
	r.Use(middleware.Recoverer)

	// Redirect / to /trino-gateway
	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/trino-gateway", http.StatusSeeOther)
	})

	// Health probes (no auth)
	r.Get("/trino-gateway/livez", a.handleLivez)
	r.Get("/trino-gateway/readyz", a.handleReadyz)

	// Static web UI (no auth)
	r.Get("/trino-gateway", a.serveIndex)
	r.Get("/trino-gateway/logo.svg", a.serveLogoSVG)
	r.Get("/trino-gateway/assets/{*}", a.serveAssets)

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

	return r
}

// serveIndex serves the admin web UI index page.
// In production, this is replaced by the embedded static bundle.
func (a *Admin) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>Trino Gateway</body></html>`))
}

// serveLogoSVG serves a minimal SVG placeholder for the logo.
func (a *Admin) serveLogoSVG(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`))
}

// serveAssets serves static assets from the embedded bundle.
// In production, this is replaced by the embedded static bundle.
func (a *Admin) serveAssets(w http.ResponseWriter, r *http.Request) {
	http.NotFound(w, r)
}
