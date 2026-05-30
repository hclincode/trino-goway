package proxy

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/hclincode/trino-goway/internal/auth"
	"github.com/hclincode/trino-goway/internal/config"
	"github.com/hclincode/trino-goway/internal/routing"
)

// Router is the consumer-package interface for routing decisions.
// Defined here (consumer owns the interface) per project conventions.
type Router interface {
	Route(ctx context.Context, req *routing.RouteInput) (*routing.RouteResult, error)
	WriteCache(queryID, backendURL string)
}

// HistoryRecorder records a routed query into persistent history.
// Defined here (consumer owns the interface) per project conventions.
type HistoryRecorder interface {
	Insert(ctx context.Context, queryID, backendURL, userName, source string) error
}

// Config holds all constructor parameters for Proxy.
type Config struct {
	Proxy   config.ProxyConfig
	Cookie  config.CookieConfig
	Auth    config.AuthConfig
	Client  *http.Client      // proxyClient — passed in from main; never created here
	Router  Router
	History HistoryRecorder   // nil-safe: skipped when not wired
	AuthMW  auth.Middleware   // from auth.New(...)
	Log     *slog.Logger
}

// Proxy implements http.Handler and routes inbound Trino traffic to selected backends.
type Proxy struct {
	cfg     Config
	router  Router
	history HistoryRecorder
	client  *http.Client
	log     *slog.Logger
	mux     http.Handler
}

// New constructs a Proxy and wires up chi routes.
func New(cfg Config) *Proxy {
	p := &Proxy{
		cfg:     cfg,
		router:  cfg.Router,
		history: cfg.History,
		client:  cfg.Client,
		log:     cfg.Log,
	}
	p.mux = p.setupRoutes()
	return p
}

// ServeHTTP implements http.Handler.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mux.ServeHTTP(w, r)
}

// setupRoutes builds the chi router.
// Auth enforcement on Trino traffic paths is intentionally absent — Trino handles its own auth.
func (p *Proxy) setupRoutes() http.Handler {
	r := chi.NewRouter()

	if p.cfg.AuthMW != nil {
		r.Use(p.cfg.AuthMW)
	}

	r.Post("/v1/statement", p.handleStatement)
	r.HandleFunc("/*", p.handleStream)

	return r
}
