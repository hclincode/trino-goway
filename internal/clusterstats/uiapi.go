package clusterstats

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hclincode/trino-goway/internal/clusterstatus"
	"github.com/hclincode/trino-goway/internal/config"
)

// UI API paths, matching Java's HttpUtils constants.
const (
	uiLoginPath        = "/ui/login"
	uiAPIStatsPath     = "/ui/api/stats"
	uiAPIQueuedAPIPath = "/ui/api/query?state=QUEUED"
)

// uiAPICollector collects live cluster stats via Trino's Web-UI API. It logs in
// once (form POST to /ui/login), keeps the resulting session cookie in a
// per-collector jar, and reuses it across ticks — a documented optimization over
// Java's fresh-login-per-GET (ClusterStatsHttpMonitor logs in before every GET).
// On a 401 the session is dropped so the next tick re-logs in.
//
// Mirrors Java's ClusterStatsHttpMonitor: GET /ui/api/stats yields
// activeWorkers/queuedQueries/runningQueries (status = activeWorkers>0 ? HEALTHY
// : UNHEALTHY); GET /ui/api/query?state=QUEUED tallies sessionUser into
// UserQueuedCount. xForwardedProtoHeader adds X-Forwarded-Proto: https on the two
// GETs (not the login).
type uiAPICollector struct {
	client                *http.Client
	username              string
	password              string
	xForwardedProtoHeader bool
	timeout               time.Duration
	retries               int
	log                   *slog.Logger

	mu       sync.Mutex
	jar      http.CookieJar
	loggedIn bool
}

func newUIAPICollector(mon config.MonitorConfig, bs config.BackendStateConfig, client *http.Client, log *slog.Logger) *uiAPICollector {
	return &uiAPICollector{
		client:                client,
		username:              bs.Username,
		password:              bs.Password,
		xForwardedProtoHeader: bs.XForwardedProtoHeader,
		timeout:               mon.StatsTimeout.D,
		retries:               mon.Retries,
		log:                   log,
	}
}

// uiStatsResponse is the JSON shape of GET /ui/api/stats.
type uiStatsResponse struct {
	ActiveWorkers  int `json:"activeWorkers"`
	RunningQueries int `json:"runningQueries"`
	QueuedQueries  int `json:"queuedQueries"`
}

// uiQueryEntry is one element of GET /ui/api/query?state=QUEUED.
type uiQueryEntry struct {
	QueryID     string `json:"queryId"`
	SessionUser string `json:"sessionUser"`
	State       string `json:"state"`
}

// Collect logs in (once), fetches cluster + per-user stats, and returns a
// best-effort ClusterStats. Any error yields a partial result (no counts) and
// never panics.
func (c *uiAPICollector) Collect(ctx context.Context, b Backend) ClusterStats {
	cs := statsBuilder(b)

	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	if err := c.ensureLogin(ctx, b.GetURL()); err != nil {
		c.log.Warn("clusterstats: ui_api login failed", "backend", b.GetName(), "err", err)
		return cs
	}

	body, status := c.get(ctx, b.GetURL()+uiAPIStatsPath)
	if status == http.StatusUnauthorized {
		c.invalidateSession()
	}
	if len(body) == 0 {
		c.log.Debug("clusterstats: ui_api empty stats response", "backend", b.GetName(), "status", status)
		return cs
	}
	var stats uiStatsResponse
	if err := json.Unmarshal(body, &stats); err != nil {
		c.log.Warn("clusterstats: ui_api parse stats", "backend", b.GetName(), "err", err)
		return cs
	}
	cs.NumWorkerNodes = stats.ActiveWorkers
	cs.RunningQueryCount = stats.RunningQueries
	cs.QueuedQueryCount = stats.QueuedQueries
	if stats.ActiveWorkers > 0 {
		cs.TrinoStatus = clusterstatus.Healthy
	} else {
		cs.TrinoStatus = clusterstatus.Unhealthy
	}

	// Per-user queued breakdown.
	body, status = c.get(ctx, b.GetURL()+uiAPIQueuedAPIPath)
	if status == http.StatusUnauthorized {
		c.invalidateSession()
	}
	if len(body) == 0 {
		c.log.Debug("clusterstats: ui_api empty queued response", "backend", b.GetName(), "status", status)
		return cs
	}
	var queries []uiQueryEntry
	if err := json.Unmarshal(body, &queries); err != nil {
		c.log.Warn("clusterstats: ui_api parse queued", "backend", b.GetName(), "err", err)
		return cs
	}
	userQueued := make(map[string]int, len(queries))
	for _, q := range queries {
		userQueued[q.SessionUser]++
	}
	cs.UserQueuedCount = userQueued
	return cs
}

// ensureLogin POSTs the login form once (per collector lifetime) and stores the
// session cookie in the jar. Subsequent calls are no-ops while a session is held.
func (c *uiAPICollector) ensureLogin(ctx context.Context, proxyTo string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loggedIn && c.jar != nil {
		return nil
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return err
	}
	form := url.Values{}
	form.Set("username", c.username)
	form.Set("password", c.password)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, proxyTo+uiLoginPath, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// Use a per-request client view that carries this jar so the Set-Cookie from
	// login is captured without mutating the shared transport.
	loginClient := *c.client
	loginClient.Jar = jar
	resp, err := loginClient.Do(req)
	if err != nil {
		return err
	}
	// Drain and close so the connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	c.jar = jar
	c.loggedIn = true
	return nil
}

func (c *uiAPICollector) invalidateSession() {
	c.mu.Lock()
	c.loggedIn = false
	c.jar = nil
	c.mu.Unlock()
}

// get performs an authenticated GET using the session jar, retrying on transport
// error up to c.retries times. It returns the body bytes and the HTTP status
// (0 on transport error / exhausted retries).
func (c *uiAPICollector) get(ctx context.Context, target string) ([]byte, int) {
	c.mu.Lock()
	jar := c.jar
	c.mu.Unlock()

	attempts := c.retries + 1
	for i := 0; i < attempts; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, 0
		}
		if c.xForwardedProtoHeader {
			req.Header.Set("X-Forwarded-Proto", "https")
		}
		reqClient := *c.client
		reqClient.Jar = jar
		resp, err := reqClient.Do(req)
		if err != nil {
			if !backoff(ctx, i, attempts) {
				return nil, 0
			}
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return body, resp.StatusCode
	}
	return nil, 0
}
