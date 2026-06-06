//go:build diff

package main_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/admin"
	"github.com/hclincode/trino-goway/internal/auth"
	"github.com/hclincode/trino-goway/internal/config"
	"github.com/hclincode/trino-goway/internal/diffharness"
	"github.com/hclincode/trino-goway/internal/monitor"
	"github.com/hclincode/trino-goway/internal/persistence"
	"github.com/hclincode/trino-goway/internal/proxy"
	"github.com/hclincode/trino-goway/internal/routing"
)

// TestLive_SeamScenarios_DiffPasses is the Phase-2 integration smoke. It:
//  1. Boots the Phase-2 container fleet (Postgres → Java gateway → Trino) via
//     diffharness.BootstrapContainers.
//  2. Stands up the Go trino-goway in-process (same pattern as Task 27 G1)
//     pointed at the shared Trino backend.
//  3. Runs every committed scenario against both gateways and asserts PASS.
//
// Slow (~60–90s first boot). Gated by //go:build diff so per-PR CI does not
// pay the cost; the nightly job runs `go test -tags=diff ./...`.
func TestLive_SeamScenarios_DiffPasses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	containers := bootstrapOrSkip(ctx, t)

	gw := startGoGateway(t, containers.TrinoURL)
	defer gw.Close()

	scenariosDir, err := filepath.Abs("testdata/scenarios")
	require.NoError(t, err)

	scenarios := loadScenariosOrSkip(t, scenariosDir)

	r := diffharness.NewRunner(
		diffharness.Target{Name: "java", BaseURL: containers.JavaURL},
		diffharness.Target{Name: "go", BaseURL: gw.URL},
	)

	results := r.RunAll(ctx, scenarios)
	for _, res := range results {
		assert.Equalf(t, diffharness.VerdictPass, res.Verdict,
			"scenario %s: verdict=%s status(java=%d go=%d) body diff=%q header diffs=%v reason=%q",
			res.Scenario, res.Verdict, res.JavaStatus, res.GoStatus, res.BodyDiff, res.HeaderDiffs, res.Reason)
	}
}

// bootstrapOrSkip wraps BootstrapContainers in a t.Skip on Docker-unavailable
// hosts, mirroring the Task-27 G1 convention (never noisy on Docker-less
// laptops; CI nightly explicitly opts in to docker).
func bootstrapOrSkip(ctx context.Context, t *testing.T) diffharness.Containers {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Skipf("diff harness: docker unavailable: %v", r)
		}
	}()
	return diffharness.BootstrapContainers(ctx, t)
}

// loadScenariosOrSkip reads every YAML scenario from dir. If the directory is
// empty the test skips — scenarios are added incrementally in Phase 3 and the
// live test should not fail just because Phase-3 work is pending.
func loadScenariosOrSkip(t *testing.T, dir string) []*diffharness.Scenario {
	t.Helper()
	entries, err := readDirYAML(dir)
	require.NoError(t, err)
	if len(entries) == 0 {
		t.Skipf("no scenario YAML files in %s", dir)
	}
	out := make([]*diffharness.Scenario, 0, len(entries))
	for _, p := range entries {
		s, err := diffharness.LoadScenario(p)
		require.NoErrorf(t, err, "load scenario %s", p)
		if liveExcludedScenarios[s.Name] {
			t.Logf("diff: excluding %q from the live target (see scenario YAML prose: not a valid cross-impl diff)", s.Name)
			continue
		}
		out = append(out, s)
	}
	return out
}

// liveExcludedScenarios are committed scenarios that are NOT run against the live
// Java↔Go fleet because they cannot be a faithful cross-implementation diff. They
// remain committed (and validation-clean) for documentation and unit coverage;
// each carries a prose note in its YAML explaining the exclusion (qa-tech-lead's
// F3-class "documented unit-only exclusion").
//
//   - query-history-scoping: FLEET-HISTORY ISOLATION (verified live, not assumed).
//     Java serves GET /trino-gateway/api/queryHistory → 200 with the FULL unscoped
//     history (admin-sees-all), which includes the bootstrap readiness-gate SELECT 1
//     (Java-only, see waitGatewayRoutable) + every prior scenario's Java traffic. The
//     Go in-memory history store only sees diff-runner traffic replayed to both sides,
//     so the record count/set structurally diverges and the list-count+shape assertion
//     can't match. User-scoping is asserted unit-only in internal/admin/admin_test.go.
//     See the full prose note in the scenario YAML header.
//
//   - public-backend-state-clusterstats: CHOICE-B FIELD-VALUE DIVERGENCE (M7).
//     Both fleets run clusterStatsConfiguration.monitorType INFO_API (Java) / the
//     INFO_API default (Go), so the ClusterStats field NAMES match (the M7 contract)
//     and counts are 0 on both. But two object fields diverge by VALUE in ways that
//     are intentional, documented choices, not regressions: externalUrl (Go fills it
//     from persistence with a proxyTo fallback per choice b; Java leaves the raw null
//     when the backend has no externalUrl) and userQueuedCount (Go omits it via
//     ,omitempty; Java emits explicit null). The 9-field shape + tag exactness is
//     asserted positively unit-side (internal/admin/backend_test.go golden) and end
//     to end (internal/e2e/cluster_stats_e2e_test.go). See the scenario YAML header.
//
// NOTE: external-routing-headers is deliberately NOT excluded — it passes live as a
// plain proxy diff (both gateways route to the default group and return the same
// upstream Trino response). Its externalHeaders-injection path is the part the live
// fleet can't exercise (F3), and that is covered by internal/proxy/proxy_test.go.
var liveExcludedScenarios = map[string]bool{
	"query-history-scoping":             true,
	"public-backend-state-clusterstats": true,
}

// startGoGateway composes the FULL Go gateway in-process pointed at the supplied
// Trino URL: the proxy (statement forwarding + recovery chain) AND the admin
// surface (gateway backend CRUD, webapp query-history), behind a single
// httptest.Server. This is required for diff parity with the Java gateway, which
// serves both the proxy and management APIs on one port — a proxy-only Go target
// 404s on /gateway/* and /trino-gateway/api/* (the admin scenarios).
//
// Persistence is in-memory (no second Postgres): a backendStore seeded with the
// same trino-shared backend the Java side gets, shared with the router as the
// BackendLister; and a historyStore shared between the proxy's HistoryRecorder
// (writes) and the admin's HistoryStore (reads). Auth is noop with wide-open
// authorization so the admin role checks pass, matching the anonymous Java fleet.
//
// Top-level dispatch mirrors the Java gateway: management paths (/gateway/*,
// /trino-gateway/*, /entity, /api/*, and the auth endpoints) go to the admin
// handler; everything else (the Trino protocol: /v1/*, …) goes to the proxy.
func startGoGateway(t *testing.T, trinoURL string) *httptest.Server {
	t.Helper()

	discard := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Seed the DISPLAY URL the Java side stores ("http://trino:8080", the
	// in-network DNS name). This makes /gateway/backend/all byte-identical to
	// Java for the trino-shared entry, so the admin-backend-crud diff does not
	// need to ignore proxyTo/externalUrl (which would also weaken the value-check
	// on the scenario's own added backend, and which the normalizer can't strip
	// from a JSON array anyway). The router can't reach that in-network name from
	// the host, so routerBackendAdapter rewrites this one backend's URL to the
	// host-mapped trinoURL for actual routing.
	const trinoDisplayURL = "http://trino:8080"
	store := newBackendStore(persistence.Backend{
		Name:         "trino-shared",
		URL:          trinoDisplayURL,
		ExternalURL:  trinoDisplayURL,
		RoutingGroup: "adhoc",
		Active:       true,
	})
	history := newHistoryStore()

	router, err := routing.New(routing.Config{
		Routing: config.RoutingConfig{
			DefaultGroup: "adhoc",
			Type:         "EXTERNAL",
			External:     config.ExternalConfig{Timeout: config.Duration{D: 500 * time.Millisecond}},
		},
		ExternalClient: &http.Client{Timeout: 500 * time.Millisecond},
		ProbeClient:    &http.Client{Timeout: 1 * time.Second},
		History:        noHistoryLookup{},
		Backends:       routerBackendAdapter{store: store, urlRewrite: map[string]string{trinoDisplayURL: trinoURL}},
		Log:            discard,
	})
	require.NoError(t, err)

	proxyClient := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	proxyHandler := proxy.New(proxy.Config{
		Proxy:   config.ProxyConfig{ResponseSize: config.DataSize{Bytes: 1_048_576}},
		Cookie:  config.CookieConfig{WireCompat: true},
		Client:  proxyClient,
		Router:  router,
		History: history,
		Log:     discard,
	})

	// Wide-open authorization so the noop anonymous principal satisfies the
	// admin/api role checks on /gateway/* (Java fleet is anonymous too).
	authz := config.AuthorizationConfig{AdminRegex: ".*", UserRegex: ".*", APIRegex: ".*"}
	adminHandler := admin.New(admin.Config{
		Auth:      config.AuthConfig{Type: "NOOP", Authorization: authz},
		Backends:  store,
		History:   historyAdminAdapter{history},
		Monitor:   alwaysHealthy{},
		AuthMW:    auth.Noop(),
		Log:       discard,
		StartTime: time.Now(),
	})

	return httptest.NewServer(gatewayMux(proxyHandler, adminHandler))
}

// gatewayMux dispatches management paths to the admin handler and everything
// else to the proxy, mirroring how the single-port Java gateway routes Trino
// protocol traffic vs. its management/UI surface.
func gatewayMux(proxyHandler, adminHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAdminPath(r.URL.Path) {
			adminHandler.ServeHTTP(w, r)
			return
		}
		proxyHandler.ServeHTTP(w, r)
	})
}

// isAdminPath reports whether p is owned by the admin/management surface rather
// than the Trino proxy. The proxy owns /v1/* and the Trino protocol catch-all.
func isAdminPath(p string) bool {
	adminPrefixes := []string{
		"/gateway", "/trino-gateway", "/entity", "/webapp", "/api/",
		"/sso", "/login", "/logout", "/loginType", "/oidc/", "/userinfo",
	}
	for _, pre := range adminPrefixes {
		if p == pre || strings.HasPrefix(p, pre+"/") || (strings.HasSuffix(pre, "/") && strings.HasPrefix(p, pre)) {
			return true
		}
	}
	return false
}

type noHistoryLookup struct{}

func (noHistoryLookup) LookupByQueryID(_ context.Context, _ string) (string, error) {
	return "", nil
}

// routerBackendAdapter adapts *backendStore to routing.BackendLister, whose
// ListActive returns []routing.ActiveBackend (vs admin.BackendStore's
// []persistence.Backend). Mirrors cmd/trino-goway/main.go::activeBackendAdapter.
//
// urlRewrite maps a backend's stored DISPLAY URL (what the admin API surfaces,
// matched to the Java side) to the host-reachable URL the router must actually
// dial. Entries not in the map pass through unchanged.
type routerBackendAdapter struct {
	store      *backendStore
	urlRewrite map[string]string
}

func (a routerBackendAdapter) ListActive(ctx context.Context) ([]routing.ActiveBackend, error) {
	backends, err := a.store.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]routing.ActiveBackend, 0, len(backends))
	for _, b := range backends {
		url := b.URL
		if rewritten, ok := a.urlRewrite[url]; ok {
			url = rewritten
		}
		out = append(out, routing.ActiveBackend{
			Name:         b.Name,
			URL:          url,
			RoutingGroup: b.RoutingGroup,
		})
	}
	return out, nil
}

// alwaysHealthy is a StatusProvider that reports every backend healthy. The live
// Go target has no monitor loop; admin endpoints that surface health read a
// stable HEALTHY so the wire shape is deterministic.
type alwaysHealthy struct{}

func (alwaysHealthy) Status(string) monitor.TrinoStatus { return monitor.StatusHealthy }

// backendStore is an in-memory persistence.BackendDAO stand-in satisfying both
// admin.BackendStore (CRUD) and routing.BackendLister (ListActive for the
// router). Safe for concurrent use.
type backendStore struct {
	mu       sync.Mutex
	byName   map[string]persistence.Backend
	ordered  []string // insertion order for stable list output
}

func newBackendStore(seed ...persistence.Backend) *backendStore {
	s := &backendStore{byName: map[string]persistence.Backend{}}
	for _, b := range seed {
		s.byName[b.Name] = b
		s.ordered = append(s.ordered, b.Name)
	}
	return s
}

func (s *backendStore) list() []persistence.Backend {
	out := make([]persistence.Backend, 0, len(s.ordered))
	for _, name := range s.ordered {
		out = append(out, s.byName[name])
	}
	return out
}

// List satisfies admin.BackendStore.
func (s *backendStore) List(context.Context) ([]persistence.Backend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.list(), nil
}

// ListActive satisfies admin.BackendStore and routing.BackendLister. The two
// interfaces have different element types, so this returns the persistence.Backend
// slice (admin) and routerListActive adapts for routing.
func (s *backendStore) ListActive(context.Context) ([]persistence.Backend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]persistence.Backend, 0, len(s.ordered))
	for _, name := range s.ordered {
		if b := s.byName[name]; b.Active {
			out = append(out, b)
		}
	}
	return out, nil
}

func (s *backendStore) Upsert(_ context.Context, b persistence.Backend) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byName[b.Name]; !ok {
		s.ordered = append(s.ordered, b.Name)
	}
	s.byName[b.Name] = b
	return nil
}

func (s *backendStore) Delete(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byName, name)
	for i, n := range s.ordered {
		if n == name {
			s.ordered = append(s.ordered[:i], s.ordered[i+1:]...)
			break
		}
	}
	return nil
}

func (s *backendStore) SetActive(_ context.Context, name string, active bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.byName[name]; ok {
		b.Active = active
		s.byName[name] = b
	}
	return nil
}

// historyStore is an in-memory query-history stand-in. It satisfies both the
// proxy's HistoryRecorder (Insert with positional args) and admin.HistoryStore
// (Insert with a QueryRecord, plus the read methods). Safe for concurrent use.
type historyStore struct {
	mu      sync.Mutex
	records []persistence.QueryRecord
}

func newHistoryStore() *historyStore { return &historyStore{} }

// Insert satisfies proxy.HistoryRecorder.
func (h *historyStore) Insert(_ context.Context, queryID, backendURL, userName, source string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, persistence.QueryRecord{
		QueryID:    queryID,
		BackendURL: backendURL,
		UserName:   userName,
		Source:     source,
		CreatedAt:  time.Now().UTC(),
	})
	return nil
}

// InsertRecord satisfies admin.HistoryStore's Insert(ctx, QueryRecord). The two
// Insert signatures collide on name, so admin.HistoryStore is satisfied via the
// historyAdminAdapter below rather than by historyStore directly.
func (h *historyStore) insertRecord(r persistence.QueryRecord) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
}

func (h *historyStore) snapshot() []persistence.QueryRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]persistence.QueryRecord, len(h.records))
	copy(out, h.records)
	return out
}

// ListRecent satisfies admin.HistoryStore.
func (h *historyStore) ListRecent(_ context.Context, limit int) ([]persistence.QueryRecord, error) {
	recs := h.snapshot()
	if limit > 0 && len(recs) > limit {
		recs = recs[len(recs)-limit:]
	}
	return recs, nil
}

// FindByFilter satisfies admin.HistoryStore. Only the UserName scope is needed
// by the live scenarios; other filter fields are honored when set.
func (h *historyStore) FindByFilter(_ context.Context, f persistence.HistoryFilter) ([]persistence.QueryRecord, int64, error) {
	var out []persistence.QueryRecord
	for _, r := range h.snapshot() {
		if f.UserName != "" && r.UserName != f.UserName {
			continue
		}
		if f.BackendURL != "" && r.BackendURL != f.BackendURL {
			continue
		}
		if f.QueryID != "" && r.QueryID != f.QueryID {
			continue
		}
		if f.Source != "" && r.Source != f.Source {
			continue
		}
		out = append(out, r)
	}
	return out, int64(len(out)), nil
}

// FindDistribution satisfies admin.HistoryStore.
func (h *historyStore) FindDistribution(_ context.Context, _ time.Time) ([]persistence.DistributionBucket, error) {
	return nil, nil
}

// historyAdminAdapter wraps *historyStore to satisfy admin.HistoryStore, whose
// Insert(ctx, QueryRecord) signature collides with the proxy HistoryRecorder's
// positional Insert on the underlying store. The read methods delegate straight
// through so the admin reads the same records the proxy wrote.
type historyAdminAdapter struct{ *historyStore }

func (a historyAdminAdapter) Insert(_ context.Context, r persistence.QueryRecord) error {
	a.historyStore.insertRecord(r)
	return nil
}

// readDirYAML returns the absolute paths of every .yaml/.yml file directly
// under dir. Wraps os.ReadDir + filepath.Join so the live test does not need
// to duplicate the filtering logic from main.go.
func readDirYAML(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !hasYAMLSuffix(name) {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	return out, nil
}

func hasYAMLSuffix(s string) bool {
	const a, b = ".yaml", ".yml"
	return len(s) >= len(a) && s[len(s)-len(a):] == a ||
		len(s) >= len(b) && s[len(s)-len(b):] == b
}
