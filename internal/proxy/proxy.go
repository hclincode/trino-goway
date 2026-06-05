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

// MetricsRecorder records proxy forwarding metrics. Defined here (consumer owns
// the interface) per project conventions; nil-safe, like HistoryRecorder — when
// the Proxy's recorder is nil every call is a no-op.
type MetricsRecorder interface {
	// RequestHandled records a completed proxy request with its routing outcome.
	// outcome is one of routing.Outcome* ("ok"|"fallback"|"error"|"kill_query").
	RequestHandled(backend, routingGroup, outcome string)
	// UpstreamDuration records the time spent on the upstream backend round-trip.
	UpstreamDuration(backend string, seconds float64)
	// OversizedResponse records a /v1/statement response that exceeded the buffer limit.
	OversizedResponse()
	// StatementCacheWrite records a sticky-routing cache write (Hard Invariant #3).
	StatementCacheWrite()
}

// Config holds all constructor parameters for Proxy.
type Config struct {
	Proxy   config.ProxyConfig
	Cookie  config.CookieConfig
	Auth    config.AuthConfig
	Client  *http.Client // proxyClient — passed in from main; never created here
	Router  Router
	History HistoryRecorder // nil-safe: skipped when not wired
	AuthMW  auth.Middleware // from auth.New(...)
	// Metrics is an optional HTTP metrics middleware (from metrics.HTTPMetrics.Middleware).
	// When non-nil it is installed first so it wraps every proxy route; nil skips it.
	Metrics func(http.Handler) http.Handler
	// MetricsRecorder records proxy forwarding metrics; nil-safe (nil = no-op).
	MetricsRecorder MetricsRecorder
	Log             *slog.Logger
}

// Proxy implements http.Handler and routes inbound Trino traffic to selected backends.
type Proxy struct {
	cfg     Config
	router  Router
	history HistoryRecorder
	metrics MetricsRecorder
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
		metrics: cfg.MetricsRecorder,
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

// The recordX helpers wrap the optional MetricsRecorder so call sites stay free
// of nil checks; each is a no-op when no recorder was wired.

func (p *Proxy) recordRequest(backend, routingGroup, outcome string) {
	if p.metrics != nil {
		p.metrics.RequestHandled(backend, routingGroup, outcome)
	}
}

func (p *Proxy) recordUpstreamDuration(backend string, seconds float64) {
	if p.metrics != nil {
		p.metrics.UpstreamDuration(backend, seconds)
	}
}

func (p *Proxy) recordOversized() {
	if p.metrics != nil {
		p.metrics.OversizedResponse()
	}
}

func (p *Proxy) recordCacheWrite() {
	if p.metrics != nil {
		p.metrics.StatementCacheWrite()
	}
}

// setupRoutes builds the chi router.
// Auth enforcement on Trino traffic paths is intentionally absent — Trino handles its own auth.
func (p *Proxy) setupRoutes() http.Handler {
	r := chi.NewRouter()

	// Metrics middleware first so it observes the full request lifecycle and reads
	// the matched route pattern after downstream handlers run.
	if p.cfg.Metrics != nil {
		r.Use(p.cfg.Metrics)
	}

	if p.cfg.AuthMW != nil {
		r.Use(p.cfg.AuthMW)
	}

	r.Post("/v1/statement", p.handleStatement)
	r.HandleFunc("/*", p.handleStream)

	return r
}
