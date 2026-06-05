package metrics

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
)

// HTTPMetrics holds the HTTP server metrics shared across listeners. Construct
// one with NewHTTPMetrics (registering against the gateway registry) and obtain a
// per-listener middleware with Middleware.
type HTTPMetrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	inFlight *prometheus.GaugeVec
}

// NewHTTPMetrics registers the HTTP server metric families on the registry.
// It returns an error if any family is already registered.
func NewHTTPMetrics(reg prometheus.Registerer) (*HTTPMetrics, error) {
	m := &HTTPMetrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "http",
			Name:      "requests_total",
			Help:      "Total HTTP requests handled, by listener, method, route pattern, and response code.",
		}, []string{"listener", "method", "pattern", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace,
			Subsystem: "http",
			Name:      "request_duration_seconds",
			Help:      "HTTP request duration in seconds, by listener, method, and route pattern.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"listener", "method", "pattern"}),
		inFlight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: "http",
			Name:      "requests_in_flight",
			Help:      "In-flight HTTP requests, by listener.",
		}, []string{"listener"}),
	}
	for _, c := range []prometheus.Collector{m.requests, m.duration, m.inFlight} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("metrics: register http metrics: %w", err)
		}
	}
	return m, nil
}

// Middleware returns a net/http middleware that records request metrics tagged
// with the given listener label (e.g. "proxy" or "admin"). It uses the chi route
// pattern rather than the raw path to bound metric cardinality.
func (m *HTTPMetrics) Middleware(listener string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			m.inFlight.WithLabelValues(listener).Inc()
			defer m.inFlight.WithLabelValues(listener).Dec()

			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)

			// The route pattern is only populated after the router has matched, so
			// it must be read after next.ServeHTTP.
			pattern := routePattern(r)
			elapsed := time.Since(start).Seconds()

			m.duration.WithLabelValues(listener, r.Method, pattern).Observe(elapsed)
			m.requests.WithLabelValues(listener, r.Method, pattern, strconv.Itoa(sw.status)).Inc()
		})
	}
}

// routePattern returns the matched chi route pattern, or "unmatched" when no
// route matched (e.g. a 404). Using the pattern instead of r.URL.Path keeps the
// metric cardinality bounded to the number of registered routes.
func routePattern(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		if p := rctx.RoutePattern(); p != "" {
			return p
		}
	}
	return "unmatched"
}

// statusWriter captures the response status code written by downstream handlers.
type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.wroteHeader = true
	return w.ResponseWriter.Write(b)
}
