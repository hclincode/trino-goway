//go:build e2e

package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"text/template"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/testutil"
)

// Harness owns the lifecycle of a trino-goway subprocess and its dependencies.
// Construct via New; tests interact with it through ProxyClient / AdminClient /
// AddBackend. All resources are released via t.Cleanup registered in New.
type Harness struct {
	// ProxyURL is the base URL of the proxy listener (e.g. http://127.0.0.1:NNNN).
	ProxyURL string
	// AdminURL is the base URL of the admin listener.
	AdminURL string

	cfg     *harnessConfig
	cmd     *exec.Cmd
	binPath string
}

// Option configures Harness via the functional options pattern. Each option
// mutates the harnessConfig used to render the temp YAML config file.
type Option func(*harnessConfig)

// harnessConfig collects every field that can be tweaked by an Option.
// Zero values become the harness defaults applied in New.
type harnessConfig struct {
	proxyPort    int
	adminPort    int
	dbDSN        string
	responseSize int64

	monitorInterval time.Duration
	monitorTimeout  time.Duration
	refreshInterval time.Duration

	externalHTTPURL  string
	externalGRPCAddr string
	externalTimeout  time.Duration
	excludeHeaders   []string

	skipReadyzWait bool

	cookieSecret string

	authType    string // "NOOP" | "OIDC" | "LDAP"
	oidcJWKSURL string
	jwksTTLSecs int // 0 => omit, lets config default (300s) apply
	ldapURL     string
	ldapBindDN  string
	ldapBindPW  string
	ldapBase    string

	adminRoleRegex string
	userRoleRegex  string
	apiRoleRegex   string

	propagateErrors bool // sets proxy.propagateErrors

	disableMetrics bool // when true, renders metrics.enabled=false

	// Cluster-stats (UC-MON-02). clusterStatsType is the selected monitor type
	// ("" => omit the clusterStats block entirely, so the subprocess applies its
	// INFO_API default and every pre-Phase-12 e2e test is unchanged). The
	// backendState username/password feed the UI_API/METRICS collectors.
	clusterStatsType     string
	backendStateUsername string
	backendStatePassword string
}

// WithExternalHTTPRouter wires routing.external.url to the given HTTP endpoint.
func WithExternalHTTPRouter(url string) Option {
	return func(c *harnessConfig) { c.externalHTTPURL = url }
}

// WithExternalGRPCRouter wires routing.external.grpcAddr to the given address.
func WithExternalGRPCRouter(addr string) Option {
	return func(c *harnessConfig) { c.externalGRPCAddr = addr }
}

// WithExternalTimeout overrides routing.external.timeout (default 500ms).
func WithExternalTimeout(d time.Duration) Option {
	return func(c *harnessConfig) { c.externalTimeout = d }
}

// WithExcludeHeaders sets routing.external.excludeHeaders. Headers in this list
// are stripped from both outgoing requests to the routing service and incoming
// externalHeaders responses before they reach the upstream backend.
func WithExcludeHeaders(headers ...string) Option {
	return func(c *harnessConfig) { c.excludeHeaders = append([]string(nil), headers...) }
}

// WithCookieSecret sets cookie.secret. Must be set for cookie-protected flows.
func WithCookieSecret(secret string) Option {
	return func(c *harnessConfig) { c.cookieSecret = secret }
}

// WithMonitorInterval overrides monitor.interval (default 2s in the harness).
func WithMonitorInterval(d time.Duration) Option {
	return func(c *harnessConfig) { c.monitorInterval = d }
}

// WithBackendRefreshInterval overrides monitor.refreshInterval — how often the
// gateway reloads the DB backend set into the monitor's probe list (default 1s
// in the harness; the gateway default is 15s). Tests asserting the DB-refresh
// reconciliation cadence itself can raise this.
func WithBackendRefreshInterval(d time.Duration) Option {
	return func(c *harnessConfig) { c.refreshInterval = d }
}

// WithMetricsDisabled renders metrics.enabled=false so the /metrics route is not
// registered (the path returns 404).
func WithMetricsDisabled() Option {
	return func(c *harnessConfig) { c.disableMetrics = true }
}

// WithSkipReadyzWait makes New() skip polling /trino-gateway/readyz after
// launching the binary. Useful for tests that need to observe the 503 readyz
// state that exists before the monitor's first probe completes.
func WithSkipReadyzWait() Option {
	return func(c *harnessConfig) { c.skipReadyzWait = true }
}

// WithResponseSize overrides proxy.responseSize in bytes.
func WithResponseSize(bytes int64) Option {
	return func(c *harnessConfig) { c.responseSize = bytes }
}

// WithAuthNOOP forces auth.type=NOOP (the harness default).
func WithAuthNOOP() Option {
	return func(c *harnessConfig) { c.authType = "NOOP" }
}

// WithAuthOIDC sets auth.type=OIDC and points auth.oidc.jwksUrl at the given URL.
func WithAuthOIDC(jwksURL string) Option {
	return func(c *harnessConfig) {
		c.authType = "OIDC"
		c.oidcJWKSURL = jwksURL
	}
}

// WithJWKSTTL overrides auth.oidc.jwksTtlSecs. Tests that exercise key rotation
// pass a small value (e.g. 1s) so the background refresher picks up the new key
// quickly enough for the test deadline.
func WithJWKSTTL(secs int) Option {
	return func(c *harnessConfig) { c.jwksTTLSecs = secs }
}

// WithAuthLDAP sets auth.type=LDAP and the bind / search parameters.
func WithAuthLDAP(addr, bindDN, bindPW, userBase string) Option {
	return func(c *harnessConfig) {
		c.authType = "LDAP"
		c.ldapURL = addr
		c.ldapBindDN = bindDN
		c.ldapBindPW = bindPW
		c.ldapBase = userBase
	}
}

// WithAdminRoleRegex sets auth.authorization.admin.
func WithAdminRoleRegex(regex string) Option {
	return func(c *harnessConfig) { c.adminRoleRegex = regex }
}

// WithUserRoleRegex sets auth.authorization.user.
func WithUserRoleRegex(regex string) Option {
	return func(c *harnessConfig) { c.userRoleRegex = regex }
}

// WithAPIRoleRegex sets auth.authorization.api.
func WithAPIRoleRegex(regex string) Option {
	return func(c *harnessConfig) { c.apiRoleRegex = regex }
}

// WithPropagateErrors toggles proxy.propagateErrors. When true, non-empty
// errors from the external routing service translate to HTTP 400 to the client.
func WithPropagateErrors(v bool) Option {
	return func(c *harnessConfig) { c.propagateErrors = v }
}

// WithClusterStats selects the cluster-stats monitor type (UC-MON-02) and the
// backendState credentials the UI_API/METRICS collectors use. monitorType is one
// of NOOP/INFO_API/UI_API/METRICS; passing "" or "INFO_API" leaves the default
// path (no stats HTTP, counts 0). For UI_API/METRICS, backendStateUsername must be
// non-empty (config.Validate requires it); backendStatePassword may be empty (the
// METRICS collector then authenticates with X-Trino-User instead of Basic).
//
// The rendered block also carries the Java-default monitor stats knobs (metric
// names + the ActiveNodeCount>=1 minimum gate) so a METRICS run resolves its
// required metrics without per-test wiring.
func WithClusterStats(monitorType, backendStateUsername, backendStatePassword string) Option {
	return func(c *harnessConfig) {
		c.clusterStatsType = monitorType
		c.backendStateUsername = backendStateUsername
		c.backendStatePassword = backendStatePassword
	}
}

// New builds and starts a trino-goway subprocess against a fresh Postgres
// container and returns a Harness configured to drive it.
//
// Steps:
//  1. Start Postgres via testcontainers.
//  2. Allocate free proxy + admin ports.
//  3. Render a temp YAML config and write it under t.TempDir().
//  4. Resolve the trino-goway binary (BinaryPath).
//  5. exec.CommandContext the binary with --config; pipe stdout/stderr to t.Log.
//  6. Poll GET /trino-gateway/readyz on the admin port until 200 (30s deadline).
//  7. Register t.Cleanup that SIGTERMs the process, waits up to 5s, then SIGKILL.
//
// Any failure short-circuits via t.Fatal so callers don't need to error-check.
func New(t testing.TB, opts ...Option) *Harness {
	t.Helper()

	cfg := &harnessConfig{
		authType:        "NOOP",
		responseSize:    1024 * 1024,
		monitorInterval: 2 * time.Second,
		monitorTimeout:  1 * time.Second,
		// Reload the DB→monitor probe set every second so a backend added via the
		// admin API after startup is probed promptly (the gateway default is 15s,
		// which would push add→HEALTHY past the AddBackend deadline).
		refreshInterval: 1 * time.Second,
		// Wide-open authorization regexes so the NOOP principal (memberOf == "")
		// satisfies RequireRole on admin/user endpoints. Tests that exercise
		// authorization explicitly should override via With{Admin,User}RoleRegex.
		adminRoleRegex: ".*",
		userRoleRegex:  ".*",
		apiRoleRegex:   ".*",
	}
	for _, opt := range opts {
		opt(cfg)
	}

	cfg.dbDSN = testutil.PostgresContainerDSN(t)
	cfg.proxyPort = testutil.FreePort(t)
	cfg.adminPort = testutil.FreePort(t)
	for cfg.adminPort == cfg.proxyPort {
		cfg.adminPort = testutil.FreePort(t)
	}

	yaml, err := buildConfig(cfg)
	require.NoError(t, err, "harness: render config")

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0o600), "harness: write config")

	bin := BinaryPath(t)

	// Subprocess context is bound to t so a test timeout kills the child.
	ctx, cancel := context.WithCancel(context.Background())

	cmd := exec.CommandContext(ctx, bin, "--config", cfgPath)
	cmd.Stdout = &logWriter{t: t, prefix: "trino-goway[stdout]"}
	cmd.Stderr = &logWriter{t: t, prefix: "trino-goway[stderr]"}
	// New process group so SIGTERM can reach the whole subtree on Unix.
	cmd.SysProcAttr = procAttrNewPgrp()

	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("harness: start binary %s: %v", bin, err)
	}

	h := &Harness{
		ProxyURL: fmt.Sprintf("http://127.0.0.1:%d", cfg.proxyPort),
		AdminURL: fmt.Sprintf("http://127.0.0.1:%d", cfg.adminPort),
		cfg:      cfg,
		cmd:      cmd,
		binPath:  bin,
	}

	t.Cleanup(func() {
		shutdown(t, cmd)
		cancel()
	})

	if !cfg.skipReadyzWait {
		waitReady(t, h.AdminURL, 30*time.Second)
	} else {
		waitListening(t, h.AdminURL, 10*time.Second)
	}

	return h
}

// waitListening polls the admin URL until the TCP listener accepts a request
// (any HTTP response, including 503). Used when readyz polling is skipped so
// callers know the server is up before issuing requests.
func waitListening(t testing.TB, adminURL string, deadline time.Duration) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	client := &http.Client{Timeout: 2 * time.Second}
	url := adminURL + "/trino-gateway/livez"

	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("harness: admin listener did not accept connections within %s", deadline)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// BinaryPath returns the path to a trino-goway executable. If the environment
// variable TRINO_GOWAY_BIN is set, that path is used verbatim. Otherwise the
// binary is built into t.TempDir() via `go build ./cmd/trino-goway`.
func BinaryPath(t testing.TB) string {
	t.Helper()

	if env := os.Getenv("TRINO_GOWAY_BIN"); env != "" {
		if _, err := os.Stat(env); err != nil {
			t.Fatalf("harness: TRINO_GOWAY_BIN=%s: %v", env, err)
		}
		return env
	}

	dir := t.TempDir()
	out := filepath.Join(dir, "trino-goway")

	build := exec.Command("go", "build", "-o", out, "./cmd/trino-goway")
	build.Dir = projectRoot(t)
	var stderr bytes.Buffer
	build.Stderr = &stderr
	if err := build.Run(); err != nil {
		t.Fatalf("harness: build trino-goway: %v\n%s", err, stderr.String())
	}
	return out
}

// projectRoot returns the absolute path to the module root by walking upward
// from the current working directory until a go.mod is found.
func projectRoot(t testing.TB) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("harness: getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("harness: project root (go.mod) not found above %s", dir)
		}
		dir = parent
	}
}

// DBDSN returns the DSN of the Postgres container backing the subprocess.
// Tests use this to seed rows directly (e.g. query_history) because the
// proxy itself does not currently write history records.
func (h *Harness) DBDSN() string {
	return h.cfg.dbDSN
}

// ProxyClient returns an HTTP client for the proxy port. CheckRedirect mirrors
// the production proxy (no following) so tests observe upstream 3xx verbatim.
func (h *Harness) ProxyClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// AdminClient returns an HTTP client for the admin port. When bearerToken is
// non-empty it is injected as Authorization: Bearer on every request.
func (h *Harness) AdminClient(bearerToken string) *http.Client {
	transport := http.DefaultTransport
	if bearerToken != "" {
		transport = &bearerTransport{token: bearerToken, base: http.DefaultTransport}
	}
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
	}
}

// AddBackend starts a TrinoFake, registers it on the admin port, and waits
// until /gateway/backend/all reports the new backend with a non-empty status.
func (h *Harness) AddBackend(t testing.TB, name, group string) *testutil.TrinoFake {
	t.Helper()

	fake := testutil.NewTrinoFake(t)

	body, err := json.Marshal(map[string]any{
		"name":         name,
		"proxyTo":      fake.URL,
		"active":       true,
		"routingGroup": group,
	})
	require.NoError(t, err, "harness: marshal backend body")

	resp, err := h.AdminClient("").Post(
		h.AdminURL+"/entity?entityType=GATEWAY_BACKEND",
		"application/json",
		bytes.NewReader(body),
	)
	require.NoError(t, err, "harness: POST /entity")
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "harness: POST /entity status")

	deadline := time.Now().Add(15 * time.Second)
	client := h.AdminClient("")
	for time.Now().Before(deadline) {
		if backendVisible(t, client, h.AdminURL, name) {
			return fake
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("harness: backend %q not visible via /gateway/backend/all within 15s", name)
	return nil
}

// backendVisible returns true when the named backend appears in
// /gateway/backend/all with any non-empty status.
func backendVisible(t testing.TB, c *http.Client, adminURL, name string) bool {
	t.Helper()

	resp, err := c.Get(adminURL + "/gateway/backend/all")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var backends []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&backends); err != nil {
		return false
	}
	for _, b := range backends {
		if b.Name == name {
			return true
		}
	}
	return false
}

// waitReady polls GET <adminURL>/trino-gateway/readyz until it returns 200 OK
// or the deadline elapses. On timeout the test is failed via t.Fatal.
func waitReady(t testing.TB, adminURL string, deadline time.Duration) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	client := &http.Client{Timeout: 2 * time.Second}
	url := adminURL + "/trino-gateway/readyz"

	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := client.Do(req)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
			_ = body
		}
		select {
		case <-ctx.Done():
			t.Fatalf("harness: readyz did not become ready within %s", deadline)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// shutdown SIGTERMs the subprocess and waits up to 5s; falls back to SIGKILL.
func shutdown(t testing.TB, cmd *exec.Cmd) {
	t.Helper()

	if cmd.Process == nil {
		return
	}

	_ = cmd.Process.Signal(syscall.SIGTERM)

	done := make(chan error, 1)
	// goroutine exits when cmd.Wait returns (process exits or is killed).
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil && !isExpectedExit(err) {
			t.Logf("harness: subprocess exited with: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Logf("harness: subprocess did not exit within 5s of SIGTERM; SIGKILL sent")
	}
}

// isExpectedExit returns true when the wait error reflects a signalled exit
// initiated by shutdown (SIGTERM/SIGKILL).
func isExpectedExit(err error) bool {
	if err == nil {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "signal: terminated") || strings.Contains(msg, "signal: killed")
}

// bearerTransport injects an Authorization header on every outbound request.
type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (b *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(clone)
}

// logWriter pipes a subprocess stream to t.Log line by line so test failures
// surface gateway logs in the test output.
type logWriter struct {
	t      testing.TB
	prefix string
	mu     sync.Mutex
	buf    bytes.Buffer
}

func (l *logWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Write(p)
	for {
		line, err := l.buf.ReadString('\n')
		if err != nil {
			l.buf.WriteString(line)
			break
		}
		l.t.Logf("%s: %s", l.prefix, strings.TrimRight(line, "\n"))
	}
	return len(p), nil
}

// buildConfig renders the harnessConfig into the YAML accepted by config.Load.
func buildConfig(c *harnessConfig) (string, error) {
	tmpl, err := template.New("harness-config").Parse(configTemplate)
	if err != nil {
		return "", fmt.Errorf("harness: parse template: %w", err)
	}
	externalTimeout := c.externalTimeout
	if externalTimeout == 0 {
		externalTimeout = 500 * time.Millisecond
	}
	data := map[string]any{
		"ProxyPort":        c.proxyPort,
		"AdminPort":        c.adminPort,
		"DBDSN":            c.dbDSN,
		"ResponseSize":     c.responseSize,
		"MonitorInterval":  c.monitorInterval,
		"MonitorTimeout":   c.monitorTimeout,
		"RefreshInterval":  c.refreshInterval,
		"ExternalHTTPURL":  c.externalHTTPURL,
		"ExternalGRPCAddr": c.externalGRPCAddr,
		"ExternalTimeout":  externalTimeout,
		"ExcludeHeaders":   c.excludeHeaders,
		"PropagateErrors":  c.propagateErrors,
		"CookieSecret":     c.cookieSecret,
		"AuthType":         c.authType,
		"OIDCJWKSURL":      c.oidcJWKSURL,
		"JWKSTTLSecs":      c.jwksTTLSecs,
		"LDAPURL":          c.ldapURL,
		"LDAPBindDN":       c.ldapBindDN,
		"LDAPBindPW":       c.ldapBindPW,
		"LDAPBase":         c.ldapBase,
		"AdminRoleRegex":   c.adminRoleRegex,
		"UserRoleRegex":    c.userRoleRegex,
		"APIRoleRegex":     c.apiRoleRegex,
		"DisableMetrics":   c.disableMetrics,
		"ClusterStatsType": c.clusterStatsType,
		"BackendStateUser": c.backendStateUsername,
		"BackendStatePass": c.backendStatePassword,
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		return "", fmt.Errorf("harness: execute template: %w", err)
	}
	return out.String(), nil
}

const configTemplate = `proxy:
  port: {{.ProxyPort}}
  responseSize: {{.ResponseSize}}B
  requestTimeout: 30s
  propagateErrors: {{.PropagateErrors}}
admin:
  port: {{.AdminPort}}
monitor:
  interval: {{.MonitorInterval}}
  checkTimeout: {{.MonitorTimeout}}
  refreshInterval: {{.RefreshInterval}}
{{- if .ClusterStatsType}}
  statsTimeout: 10s
  retries: 0
  metricsEndpoint: /metrics
  runningQueriesMetricName: trino_execution_name_QueryManager_RunningQueries
  queuedQueriesMetricName: trino_execution_name_QueryManager_QueuedQueries
  metricMinimumValues:
    trino_metadata_name_DiscoveryNodeManager_ActiveNodeCount: 1
{{- end}}
db:
  driver: postgres
  dsn: "{{.DBDSN}}"
routing:
  defaultGroup: default
  type: EXTERNAL
  external:
{{- if .ExternalHTTPURL}}
    url: "{{.ExternalHTTPURL}}"
{{- end}}
{{- if .ExternalGRPCAddr}}
    grpcAddr: "{{.ExternalGRPCAddr}}"
{{- end}}
    timeout: {{.ExternalTimeout}}
{{- if .ExcludeHeaders}}
    excludeHeaders:
{{- range .ExcludeHeaders}}
      - "{{.}}"
{{- end}}
{{- end}}
auth:
  type: {{.AuthType}}
{{- if eq .AuthType "OIDC"}}
  oidc:
    jwksUrl: "{{.OIDCJWKSURL}}"
{{- if gt .JWKSTTLSecs 0}}
    jwksTtlSecs: {{.JWKSTTLSecs}}
{{- end}}
{{- end}}
{{- if eq .AuthType "LDAP"}}
  ldap:
    url: "{{.LDAPURL}}"
    bindDn: "{{.LDAPBindDN}}"
    bindPassword: "{{.LDAPBindPW}}"
    userBase: "{{.LDAPBase}}"
{{- end}}
  authorization:
{{- if .AdminRoleRegex}}
    admin: "{{.AdminRoleRegex}}"
{{- end}}
{{- if .UserRoleRegex}}
    user: "{{.UserRoleRegex}}"
{{- end}}
{{- if .APIRoleRegex}}
    api: "{{.APIRoleRegex}}"
{{- end}}
cookie:
{{- if .CookieSecret}}
  secret: "{{.CookieSecret}}"
{{- end}}
  wireCompat: true
{{- if .DisableMetrics}}
metrics:
  enabled: false
{{- end}}
{{- if .ClusterStatsType}}
clusterStats:
  monitorType: {{.ClusterStatsType}}
backendState:
  username: "{{.BackendStateUser}}"
  password: "{{.BackendStatePass}}"
{{- end}}
`
