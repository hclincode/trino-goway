package clusterstats

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hclincode/trino-goway/internal/clusterstatus"
	"github.com/hclincode/trino-goway/internal/config"
)

// metricsCollector collects counts + a health verdict from a backend's
// OpenMetrics endpoint. Mirrors Java's ClusterStatsMetricsMonitor: it requests
// each required metric, parses the OpenMetrics text body, and gates health on
// configured minimum/maximum thresholds. It sets no NumWorkerNodes and no
// UserQueuedCount.
type metricsCollector struct {
	client *http.Client
	log    *slog.Logger

	metricsEndpoint          string
	runningQueriesMetricName string
	queuedQueriesMetricName  string
	metricMinimumValues      map[string]float64
	metricMaximumValues      map[string]float64
	metricNames              []string

	authHeaderName  string
	authHeaderValue string
	xForwarded      bool
	timeout         time.Duration
	retries         int
}

func newMetricsCollector(mon config.MonitorConfig, bs config.BackendStateConfig, client *http.Client, log *slog.Logger) *metricsCollector {
	c := &metricsCollector{
		client:                   client,
		log:                      log,
		metricsEndpoint:          mon.MetricsEndpoint,
		runningQueriesMetricName: mon.RunningQueriesMetricName,
		queuedQueriesMetricName:  mon.QueuedQueriesMetricName,
		metricMinimumValues:      mon.MetricMinimumValues,
		metricMaximumValues:      mon.MetricMaximumValues,
		xForwarded:               bs.XForwardedProtoHeader,
		timeout:                  mon.StatsTimeout.D,
		retries:                  mon.Retries,
	}
	// Auth: Basic when a password is set, else X-Trino-User (matching Java).
	if bs.Password != "" {
		c.authHeaderName = "Authorization"
		c.authHeaderValue = "Basic " + base64.StdEncoding.EncodeToString([]byte(bs.Username+":"+bs.Password))
	} else {
		c.authHeaderName = "X-Trino-User"
		c.authHeaderValue = bs.Username
	}
	// Required metric set: running + queued + all threshold keys (dedup, matching
	// Java's metricNames builder).
	seen := make(map[string]struct{})
	add := func(name string) {
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		c.metricNames = append(c.metricNames, name)
	}
	add(c.runningQueriesMetricName)
	add(c.queuedQueriesMetricName)
	for k := range c.metricMinimumValues {
		add(k)
	}
	for k := range c.metricMaximumValues {
		add(k)
	}
	return c
}

// Collect fetches the required metrics, applies threshold gating, and returns the
// resulting ClusterStats. Any required metric absent, or any min/max gate
// failing, yields UNHEALTHY with zero counts.
func (c *metricsCollector) Collect(ctx context.Context, b Backend) ClusterStats {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	metrics := c.getMetrics(ctx, b.GetURL())
	if len(metrics) == 0 {
		c.log.Debug("clusterstats: metrics: no metrics available", "backend", b.GetName())
		return c.unhealthy(b)
	}

	for name, min := range c.metricMinimumValues {
		v, ok := metrics[name]
		if !ok || v < min {
			c.log.Debug("clusterstats: metrics: below minimum", "backend", b.GetName(), "metric", name)
			return c.unhealthy(b)
		}
	}
	for name, max := range c.metricMaximumValues {
		v, ok := metrics[name]
		if !ok || v > max {
			c.log.Debug("clusterstats: metrics: above maximum", "backend", b.GetName(), "metric", name)
			return c.unhealthy(b)
		}
	}

	cs := statsBuilder(b)
	cs.TrinoStatus = clusterstatus.Healthy
	cs.RunningQueryCount = int(metrics[c.runningQueriesMetricName])
	cs.QueuedQueryCount = int(metrics[c.queuedQueriesMetricName])
	return cs
}

func (c *metricsCollector) unhealthy(b Backend) ClusterStats {
	cs := statsBuilder(b)
	cs.TrinoStatus = clusterstatus.Unhealthy
	return cs
}

// getMetrics requests the required metrics and parses the OpenMetrics body. It
// returns an empty map on transport error, a non-200 status, or when any required
// metric is missing from the response (matching Java's MetricsResponseHandler,
// which rejects a response lacking required keys). Retries on transport error up
// to c.retries times.
func (c *metricsCollector) getMetrics(ctx context.Context, baseURL string) map[string]float64 {
	target := strings.TrimRight(baseURL, "/") + c.metricsEndpoint
	q := url.Values{}
	for _, name := range c.metricNames {
		q.Add("name[]", name)
	}
	if enc := q.Encode(); enc != "" {
		target += "?" + enc
	}

	attempts := c.retries + 1
	for i := 0; i < attempts; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil
		}
		req.Header.Set(c.authHeaderName, c.authHeaderValue)
		req.Header.Set("Content-Type", "application/openmetrics-text; version=1.0.0; charset=utf-8")
		if c.xForwarded {
			req.Header.Set("X-Forwarded-Proto", "https")
		}
		resp, err := c.client.Do(req)
		if err != nil {
			if !backoff(ctx, i, attempts) {
				return nil
			}
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil
		}
		return c.parseMetrics(string(body))
	}
	return nil
}

// parseMetrics parses an OpenMetrics text body into name → value. It mirrors
// Java's parse (split on "\n", drop comment lines starting with '#', take the
// first whitespace-separated token as the name and the next as the value). If any
// required metric is missing the parse is rejected (empty map returned).
func (c *metricsCollector) parseMetrics(body string) map[string]float64 {
	out := make(map[string]float64)
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		name := parts[0]
		v, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err != nil {
			continue
		}
		out[name] = v
	}
	for _, name := range c.metricNames {
		if _, ok := out[name]; !ok {
			c.log.Debug("clusterstats: metrics: required metric missing", "metric", name)
			return nil
		}
	}
	return out
}
