package routing

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/hclincode/trino-goway/internal/config"
)

// defaultExternalTimeout is used when config.ExternalConfig.Timeout is zero.
// Declared as a typed constant so both HTTP and gRPC transports share it.
var _ = time.Duration(defaultExternalTimeout) // ensure the untyped const is valid

// killQueryRE matches KILL QUERY '<queryId>' statements.
// Hard Invariant #6: KILL QUERY regex routing on POST body.
var killQueryRE = regexp.MustCompile(`(?i)KILL\s+QUERY\s+'([0-9]+_[0-9]+_[0-9]+_\w+)'`)

// RouteInput holds all fields needed to select a backend for an inbound request.
// Constructed by the proxy layer before calling Router.Route.
type RouteInput struct {
	Method      string
	RequestURI  string
	QueryString string
	RemoteAddr  string
	RemoteHost  string
	RemoteUser  string
	Body        string              // buffered body for POST /v1/statement; empty otherwise
	Parameters  map[string][]string // URL + form params
	headers     http.Header         // inbound request headers (unexported; access via Header())
}

// Header returns the first value of the named header (case-insensitive).
func (r *RouteInput) Header(name string) string {
	if r.headers == nil {
		return ""
	}
	return r.headers.Get(name)
}

// Headers returns the full inbound header map (read-only).
// Used by the external routing transport to forward headers to the routing service.
func (r *RouteInput) Headers() http.Header {
	return r.headers
}

// NewRouteInput constructs a RouteInput from an *http.Request and an optional buffered body.
func NewRouteInput(req *http.Request, body string) *RouteInput {
	return &RouteInput{
		Method:      req.Method,
		RequestURI:  req.URL.RequestURI(),
		QueryString: req.URL.RawQuery,
		RemoteAddr:  req.RemoteAddr,
		RemoteHost:  req.Host,
		RemoteUser:  req.Header.Get("X-Trino-User"),
		Body:        body,
		Parameters:  req.Form,
		headers:     req.Header,
	}
}

// RouteResult is the outcome of a routing decision.
type RouteResult struct {
	BackendURL      string
	RoutingGroup    string
	ExternalHeaders map[string]string
	Errors          []string
	// Outcome classifies how the backend was selected, for metrics. One of
	// OutcomeOK, OutcomeFallback, or OutcomeKillQuery. The proxy records
	// OutcomeError itself when no backend could be selected.
	Outcome string
}

// Route outcome classifications, recorded on RouteResult.Outcome.
const (
	// OutcomeOK is a normal selection: cache hit or external router resolved a group.
	OutcomeOK = "ok"
	// OutcomeFallback is selection via the default group or the recovery chain.
	OutcomeFallback = "fallback"
	// OutcomeKillQuery is KILL QUERY routing to the query's owning backend.
	OutcomeKillQuery = "kill_query"
	// OutcomeError is recorded by the proxy when no backend could be selected.
	OutcomeError = "error"
)

// Router orchestrates external routing selector + 3-step recovery chain.
type Router struct {
	cfg          config.RoutingConfig
	log          *slog.Logger
	httpSelector *externalHTTPSelector
	grpcSelector *externalGRPCSelector
	cache        *queryCache
	recovery     *recoveryChain
	backends     BackendLister // for group → backend resolution
	metrics      RouterMetrics
}

// Config is the routing package's constructor config type.
type Config struct {
	Routing        config.RoutingConfig
	ExternalClient *http.Client // routerClient — never shared with proxy or monitor
	ProbeClient    *http.Client // monitorClient re-used for HEAD probes (ok: same timeout profile)
	History        HistoryLookup
	Backends       BackendLister
	Metrics        RouterMetrics // nil-safe: defaults to a no-op recorder
	Log            *slog.Logger
}

// New constructs a Router. Returns an error if gRPC dial fails.
func New(cfg Config) (*Router, error) {
	cache, err := newQueryCache(defaultCacheSize)
	if err != nil {
		return nil, err
	}

	grpcSel, err := newExternalGRPCSelector(cfg.Routing.External)
	if err != nil {
		return nil, err
	}

	m := cfg.Metrics
	if m == nil {
		m = noopMetrics{}
	}

	return &Router{
		cfg:          cfg.Routing,
		log:          cfg.Log,
		httpSelector: newExternalHTTPSelector(cfg.Routing.External, cfg.ExternalClient),
		grpcSelector: grpcSel,
		cache:        cache,
		recovery: &recoveryChain{
			history:     cfg.History,
			backends:    cfg.Backends,
			probeClient: cfg.ProbeClient,
			metrics:     m,
		},
		backends: cfg.Backends,
		metrics:  m,
	}, nil
}

// Route selects a backend URL for the given request.
// Decision order:
//  1. KILL QUERY regex → route to history backend
//  2. Cache hit for queryId (extracted from X-Trino-Source or URL)
//  3. External routing service (HTTP or gRPC)
//  4. 3-step recovery chain (history → HEAD probe → first-active)
//  5. Default routing group fallback
func (r *Router) Route(ctx context.Context, req *RouteInput) (*RouteResult, error) {
	// Step 1: KILL QUERY routing.
	if queryID := extractKillQueryID(req.Body); queryID != "" {
		if url := r.recovery.recoverBackend(ctx, queryID); url != "" {
			r.log.Debug("routing: kill query → history backend", "queryId", queryID, "backend", url)
			r.metrics.KillQueryRoute()
			return &RouteResult{BackendURL: url, Outcome: OutcomeKillQuery}, nil
		}
	}

	// Step 2: Cache hit.
	if queryID := extractQueryID(req); queryID != "" {
		if url, ok := r.cache.get(queryID); ok {
			r.log.Debug("routing: cache hit", "queryId", queryID, "backend", url)
			r.metrics.CacheEvent(CacheEventHit)
			return &RouteResult{BackendURL: url, Outcome: OutcomeOK}, nil
		}
		r.metrics.CacheEvent(CacheEventMiss)
	}

	// Step 3: External routing service.
	group, extHeaders, errs, err := r.callExternal(ctx, req)
	if err != nil {
		r.log.Warn("routing: external selector failed, falling back", "err", err)
	}

	// Step 4: Resolve group → backend URL.
	outcome := OutcomeOK
	if group == "" {
		group = r.cfg.DefaultGroup
		outcome = OutcomeFallback
	}
	backendURL, err := r.resolveGroup(ctx, group)
	if err != nil || backendURL == "" {
		// Step 5: 3-step recovery chain.
		if queryID := extractQueryID(req); queryID != "" {
			if url := r.recovery.recoverBackend(ctx, queryID); url != "" {
				r.log.Debug("routing: recovery chain succeeded", "queryId", queryID, "backend", url)
				return &RouteResult{
					BackendURL:      url,
					ExternalHeaders: extHeaders,
					Errors:          errs,
					Outcome:         OutcomeFallback,
				}, nil
			}
		}
		// Final fallback: first active backend regardless of group.
		backendURL, _ = r.firstActiveBackend(ctx)
		outcome = OutcomeFallback
	}

	return &RouteResult{
		BackendURL:      backendURL,
		RoutingGroup:    group,
		ExternalHeaders: extHeaders,
		Errors:          errs,
		Outcome:         outcome,
	}, nil
}

// WriteCache stores queryID → backendURL synchronously.
// Called by the proxy after extracting queryId from the POST /v1/statement response.
// Hard Invariant #3: cache write before response flush.
func (r *Router) WriteCache(queryID, backendURL string) {
	r.cache.set(queryID, backendURL)
}

// callExternal tries gRPC first, then HTTP if gRPC is not configured or fails.
// ExcludeHeaders are filtered from the externalHeaders response before returning.
func (r *Router) callExternal(ctx context.Context, req *RouteInput) (string, map[string]string, []string, error) {
	if r.grpcSelector != nil {
		start := time.Now()
		group, headers, errs, err := r.grpcSelector.selectGroup(ctx, req)
		r.metrics.RouterCall(TransportGRPC, routerOutcome(err), time.Since(start).Seconds())
		if err == nil {
			return group, r.filterExcludedHeaders(headers), errs, nil
		}
		r.log.Warn("routing: grpc selector failed", "err", err)
	}
	start := time.Now()
	group, headers, errs, err := r.httpSelector.selectGroup(ctx, req)
	r.metrics.RouterCall(TransportHTTP, routerOutcome(err), time.Since(start).Seconds())
	return group, r.filterExcludedHeaders(headers), errs, err
}

// routerOutcome classifies an external routing call result into a metric label.
// A deadline-exceeded error is reported as "timeout"; any other error as "error".
func routerOutcome(err error) string {
	switch {
	case err == nil:
		return RouterOutcomeOK
	case errors.Is(err, context.DeadlineExceeded):
		return RouterOutcomeTimeout
	default:
		return RouterOutcomeError
	}
}

// filterExcludedHeaders removes any key in cfg.External.ExcludeHeaders from the
// externalHeaders map returned by the routing service.
func (r *Router) filterExcludedHeaders(headers map[string]string) map[string]string {
	if len(r.cfg.External.ExcludeHeaders) == 0 {
		return headers
	}
	excluded := buildExcludeSet(r.cfg.External.ExcludeHeaders)
	result := make(map[string]string, len(headers))
	for k, v := range headers {
		if !excluded[http.CanonicalHeaderKey(k)] {
			result[k] = v
		}
	}
	return result
}

// resolveGroup returns the URL of an active backend in the given routing group.
func (r *Router) resolveGroup(ctx context.Context, group string) (string, error) {
	backends, err := r.backends.ListActive(ctx)
	if err != nil {
		return "", err
	}
	for _, b := range backends {
		if b.RoutingGroup == group {
			return b.URL, nil
		}
	}
	return "", nil
}

// firstActiveBackend returns the URL of the first active backend, regardless of group.
func (r *Router) firstActiveBackend(ctx context.Context) (string, error) {
	backends, err := r.backends.ListActive(ctx)
	if err != nil || len(backends) == 0 {
		return "", err
	}
	return backends[0].URL, nil
}

// extractKillQueryID returns the queryID from a KILL QUERY statement, or "".
func extractKillQueryID(body string) string {
	m := killQueryRE.FindStringSubmatch(body)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// extractQueryID extracts a queryId from the request path (/v1/query/<id> or /v1/statement/<id>).
func extractQueryID(req *RouteInput) string {
	// e.g. /v1/query/20240101_000000_00001_xxxxx or /v1/statement/executing/...
	const queryPrefix = "/v1/query/"
	uri := req.RequestURI
	if len(uri) > len(queryPrefix) && uri[:len(queryPrefix)] == queryPrefix {
		rest := uri[len(queryPrefix):]
		// queryID ends before the next '/' or '?'
		for i, c := range rest {
			if c == '/' || c == '?' {
				return rest[:i]
			}
		}
		return rest
	}
	return ""
}
