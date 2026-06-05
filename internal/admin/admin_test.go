package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/admin"
	"github.com/hclincode/trino-goway/internal/auth"
	"github.com/hclincode/trino-goway/internal/config"
	"github.com/hclincode/trino-goway/internal/monitor"
	"github.com/hclincode/trino-goway/internal/persistence"
)

// ---- In-memory fakes ----

type fakeBackendStore struct {
	backends map[string]persistence.Backend
}

func newFakeBackendStore() *fakeBackendStore {
	return &fakeBackendStore{backends: make(map[string]persistence.Backend)}
}

func (f *fakeBackendStore) List(_ context.Context) ([]persistence.Backend, error) {
	result := make([]persistence.Backend, 0, len(f.backends))
	for _, b := range f.backends {
		result = append(result, b)
	}
	return result, nil
}

func (f *fakeBackendStore) ListActive(_ context.Context) ([]persistence.Backend, error) {
	var result []persistence.Backend
	for _, b := range f.backends {
		if b.Active {
			result = append(result, b)
		}
	}
	return result, nil
}

func (f *fakeBackendStore) Upsert(_ context.Context, b persistence.Backend) error {
	f.backends[b.Name] = b
	return nil
}

func (f *fakeBackendStore) Delete(_ context.Context, name string) error {
	delete(f.backends, name)
	return nil
}

func (f *fakeBackendStore) SetActive(_ context.Context, name string, active bool) error {
	b, ok := f.backends[name]
	if !ok {
		return fmt.Errorf("backend %q not found", name)
	}
	b.Active = active
	f.backends[name] = b
	return nil
}

type fakeHistoryStore struct {
	records []persistence.QueryRecord
}

func (f *fakeHistoryStore) Insert(_ context.Context, r persistence.QueryRecord) error {
	f.records = append(f.records, r)
	return nil
}

func (f *fakeHistoryStore) ListRecent(_ context.Context, limit int) ([]persistence.QueryRecord, error) {
	end := limit
	if end > len(f.records) {
		end = len(f.records)
	}
	return f.records[:end], nil
}

func (f *fakeHistoryStore) FindByFilter(_ context.Context, filter persistence.HistoryFilter) ([]persistence.QueryRecord, int64, error) {
	var result []persistence.QueryRecord
	for _, r := range f.records {
		if filter.UserName != "" && r.UserName != filter.UserName {
			continue
		}
		if filter.BackendURL != "" && r.BackendURL != filter.BackendURL {
			continue
		}
		if filter.QueryID != "" && r.QueryID != filter.QueryID {
			continue
		}
		result = append(result, r)
	}
	total := int64(len(result))
	page := filter.Page
	if page < 1 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = 10
	}
	start := (page - 1) * pageSize
	if start >= len(result) {
		return []persistence.QueryRecord{}, total, nil
	}
	end := start + pageSize
	if end > len(result) {
		end = len(result)
	}
	return result[start:end], total, nil
}

func (f *fakeHistoryStore) FindDistribution(_ context.Context, since time.Time) ([]persistence.DistributionBucket, error) {
	// Bucket records by minute + backend URL, mirroring the real DAO.
	type key struct {
		minute time.Time
		url    string
	}
	counts := make(map[key]int64)
	for _, r := range f.records {
		if r.CreatedAt.Before(since) {
			continue
		}
		k := key{minute: r.CreatedAt.UTC().Truncate(time.Minute), url: r.BackendURL}
		counts[k]++
	}
	buckets := make([]persistence.DistributionBucket, 0, len(counts))
	for k, c := range counts {
		buckets = append(buckets, persistence.DistributionBucket{
			MinuteStart: k.minute,
			BackendURL:  k.url,
			QueryCount:  c,
		})
	}
	return buckets, nil
}

type fakeStatusProvider struct {
	statuses map[string]monitor.TrinoStatus
}

func newFakeStatusProvider() *fakeStatusProvider {
	return &fakeStatusProvider{statuses: make(map[string]monitor.TrinoStatus)}
}

func (f *fakeStatusProvider) Status(url string) monitor.TrinoStatus {
	if s, ok := f.statuses[url]; ok {
		return s
	}
	return monitor.StatusUnknown
}

func (f *fakeStatusProvider) SetBackendStatus(url string, status monitor.TrinoStatus) {
	f.statuses[url] = status
}

// ---- Test helpers ----

// adminCfgNoAuth builds a Config where every caller has all roles (authorization regexes match everything).
func adminCfgNoAuth(bs admin.BackendStore, hs admin.HistoryStore, sp *fakeStatusProvider) admin.Config {
	authCfg := config.AuthConfig{
		Type: "NOOP",
		Authorization: config.AuthorizationConfig{
			AdminRegex: ".*",
			UserRegex:  ".*",
			APIRegex:   ".*",
		},
	}
	return admin.Config{
		Auth:      authCfg,
		Backends:  bs,
		History:   hs,
		Monitor:   sp,
		StatusMut: sp,
		AuthMW:    auth.Noop(),
		StartTime: time.Now(),
	}
}

// do performs a request against the admin handler and returns the recorder.
func do(handler http.Handler, method, path string, body []byte) *httptest.ResponseRecorder {
	var bodyReader *bytes.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func mustJSON(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("mustJSON: %v", err)
	}
	return b
}

// ---- Tests ----

func TestAdmin_HealthProbes(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()
	a := admin.New(adminCfgNoAuth(bs, hs, sp))

	t.Run("livez always 200", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/trino-gateway/livez", nil)
		if rec.Code != http.StatusOK {
			t.Errorf("livez: want 200, got %d", rec.Code)
		}
		if got := strings.TrimSpace(rec.Body.String()); got != "ok" {
			t.Errorf("livez body: want %q, got %q", "ok", got)
		}
	})

	t.Run("readyz 503 before SetReady", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/trino-gateway/readyz", nil)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("readyz (before ready): want 503, got %d", rec.Code)
		}
	})

	t.Run("readyz 200 after SetReady", func(t *testing.T) {
		a.SetReady()
		rec := do(a, http.MethodGet, "/trino-gateway/readyz", nil)
		if rec.Code != http.StatusOK {
			t.Errorf("readyz (after ready): want 200, got %d", rec.Code)
		}
	})
}

func TestAdmin_BackendCRUD(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()
	a := admin.New(adminCfgNoAuth(bs, hs, sp))

	pb := admin.ProxyBackend{
		Name:         "cluster-1",
		ProxyTo:      "http://trino1:8080",
		Active:       true,
		RoutingGroup: "default",
	}

	t.Run("add backend", func(t *testing.T) {
		rec := do(a, http.MethodPost, "/gateway/backend/modify/add", mustJSON(t, pb))
		if rec.Code != http.StatusOK {
			t.Fatalf("add: want 200, got %d; body=%s", rec.Code, rec.Body.String())
		}
		var got admin.ProxyBackend
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("add: unmarshal: %v", err)
		}
		if got.Name != pb.Name {
			t.Errorf("add: name: want %q, got %q", pb.Name, got.Name)
		}
	})

	t.Run("list all backends", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/gateway/backend/all", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("list all: want 200, got %d", rec.Code)
		}
		var backends []admin.ProxyBackend
		if err := json.Unmarshal(rec.Body.Bytes(), &backends); err != nil {
			t.Fatalf("list all: unmarshal: %v", err)
		}
		if len(backends) != 1 {
			t.Fatalf("list all: want 1 backend, got %d", len(backends))
		}
		if backends[0].Name != "cluster-1" {
			t.Errorf("list all: want cluster-1, got %q", backends[0].Name)
		}
	})

	t.Run("list active backends", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/gateway/backend/active", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("list active: want 200, got %d", rec.Code)
		}
		var backends []admin.ProxyBackend
		if err := json.Unmarshal(rec.Body.Bytes(), &backends); err != nil {
			t.Fatalf("list active: unmarshal: %v", err)
		}
		if len(backends) != 1 {
			t.Fatalf("list active: want 1 backend, got %d", len(backends))
		}
	})

	t.Run("deactivate backend", func(t *testing.T) {
		rec := do(a, http.MethodPost, "/gateway/backend/deactivate/cluster-1", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("deactivate: want 200, got %d", rec.Code)
		}
		// Verify active list is now empty.
		rec2 := do(a, http.MethodGet, "/gateway/backend/active", nil)
		var backends []admin.ProxyBackend
		_ = json.Unmarshal(rec2.Body.Bytes(), &backends)
		if len(backends) != 0 {
			t.Errorf("after deactivate: want 0 active, got %d", len(backends))
		}
	})

	t.Run("activate backend", func(t *testing.T) {
		rec := do(a, http.MethodPost, "/gateway/backend/activate/cluster-1", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("activate: want 200, got %d", rec.Code)
		}
	})

	t.Run("delete backend", func(t *testing.T) {
		rec := do(a, http.MethodPost, "/gateway/backend/modify/delete", []byte("cluster-1"))
		if rec.Code != http.StatusOK {
			t.Fatalf("delete: want 200, got %d; body=%s", rec.Code, rec.Body.String())
		}
		// Verify list is empty.
		rec2 := do(a, http.MethodGet, "/gateway/backend/all", nil)
		var backends []admin.ProxyBackend
		_ = json.Unmarshal(rec2.Body.Bytes(), &backends)
		if len(backends) != 0 {
			t.Errorf("after delete: want 0 backends, got %d", len(backends))
		}
	})
}

// TestAdmin_BackendExternalURL covers audit M6: externalUrl is always present on
// the wire (no omitempty) and falls back to proxyTo when unset, matching Java's
// ProxyBackendConfiguration.getExternalUrl().
func TestAdmin_BackendExternalURL(t *testing.T) {
	cases := []struct {
		name        string
		externalURL string
		proxyTo     string
		wantWire    string
	}{
		{
			name:        "explicit external URL is preserved",
			externalURL: "https://trino.example.com:443",
			proxyTo:     "http://trino1:8080",
			wantWire:    "https://trino.example.com:443",
		},
		{
			name:        "empty external URL falls back to proxyTo",
			externalURL: "",
			proxyTo:     "http://trino1:8080",
			wantWire:    "http://trino1:8080",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bs := newFakeBackendStore()
			hs := &fakeHistoryStore{}
			sp := newFakeStatusProvider()
			a := admin.New(adminCfgNoAuth(bs, hs, sp))

			pb := admin.ProxyBackend{
				Name:         "cluster-1",
				ProxyTo:      tc.proxyTo,
				ExternalURL:  tc.externalURL,
				Active:       true,
				RoutingGroup: "default",
			}
			rec := do(a, http.MethodPost, "/gateway/backend/modify/add", mustJSON(t, pb))
			if rec.Code != http.StatusOK {
				t.Fatalf("add: want 200, got %d; body=%s", rec.Code, rec.Body.String())
			}

			// The stored backend must carry the raw externalUrl the client sent.
			stored := bs.backends["cluster-1"]
			if stored.ExternalURL != tc.externalURL {
				t.Errorf("stored externalUrl: want %q, got %q", tc.externalURL, stored.ExternalURL)
			}

			rec = do(a, http.MethodGet, "/gateway/backend/all", nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("list all: want 200, got %d", rec.Code)
			}

			// The field must always be present on the wire (no omitempty).
			var raw []map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
				t.Fatalf("list all: unmarshal raw: %v", err)
			}
			if len(raw) != 1 {
				t.Fatalf("list all: want 1 backend, got %d", len(raw))
			}
			if _, ok := raw[0]["externalUrl"]; !ok {
				t.Errorf("list all: externalUrl field absent from wire JSON: %s", rec.Body.String())
			}

			var backends []admin.ProxyBackend
			if err := json.Unmarshal(rec.Body.Bytes(), &backends); err != nil {
				t.Fatalf("list all: unmarshal: %v", err)
			}
			if backends[0].ExternalURL != tc.wantWire {
				t.Errorf("wire externalUrl: want %q, got %q", tc.wantWire, backends[0].ExternalURL)
			}
		})
	}
}

// TestAdmin_GetDistribution_LineChart covers audit T69: the per-backend,
// per-minute query-count series is populated and keyed by backend name.
func TestAdmin_GetDistribution_LineChart(t *testing.T) {
	bs := newFakeBackendStore()
	require.NoError(t, bs.Upsert(context.Background(), persistence.Backend{
		Name: "cluster-a", URL: "http://a:8080", Active: true,
	}))
	require.NoError(t, bs.Upsert(context.Background(), persistence.Backend{
		Name: "cluster-b", URL: "http://b:8080", Active: true,
	}))

	now := time.Now().UTC()
	minute1 := now.Add(-2 * time.Minute)
	minute2 := now.Add(-1 * time.Minute)
	hs := &fakeHistoryStore{
		records: []persistence.QueryRecord{
			{QueryID: "a1", BackendURL: "http://a:8080", CreatedAt: minute1},
			{QueryID: "a2", BackendURL: "http://a:8080", CreatedAt: minute1.Add(5 * time.Second)},
			{QueryID: "a3", BackendURL: "http://a:8080", CreatedAt: minute2},
			{QueryID: "b1", BackendURL: "http://b:8080", CreatedAt: minute2},
			// Outside the 1h window — must be excluded.
			{QueryID: "old", BackendURL: "http://a:8080", CreatedAt: now.Add(-3 * time.Hour)},
		},
	}
	sp := newFakeStatusProvider()
	a := admin.New(adminCfgNoAuth(bs, hs, sp))

	rec := do(a, http.MethodPost, "/webapp/getDistribution", []byte(`{"latestHour":1}`))
	require.Equal(t, http.StatusOK, rec.Code)

	var env admin.Result[admin.DistributionResponse]
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))

	lc := env.Data.LineChart
	require.Contains(t, lc, "cluster-a", "line chart must be keyed by backend name")
	require.Contains(t, lc, "cluster-b")

	// cluster-a: 2 in minute1, 1 in minute2 → two buckets; the old row is excluded.
	var aTotal int64
	for _, p := range lc["cluster-a"] {
		aTotal += p.QueryCount
		assert.Equal(t, "cluster-a", p.Name)
		assert.Equal(t, "http://a:8080", p.BackendURL)
		assert.NotZero(t, p.EpochMillis, "each point carries a minute timestamp")
	}
	assert.EqualValues(t, 3, aTotal, "cluster-a in-window count")
	assert.Len(t, lc["cluster-a"], 2, "two distinct minute buckets for cluster-a")

	var bTotal int64
	for _, p := range lc["cluster-b"] {
		bTotal += p.QueryCount
	}
	assert.EqualValues(t, 1, bTotal, "cluster-b in-window count")
}

func TestAdmin_PublicBackends(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()
	a := admin.New(adminCfgNoAuth(bs, hs, sp))

	// Add a backend directly to the store.
	_ = bs.Upsert(context.Background(), persistence.Backend{
		Name:         "pub-backend",
		URL:          "http://trino-pub:8080",
		Active:       true,
		RoutingGroup: "default",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	})

	t.Run("list public backends", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/api/public/backends", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d", rec.Code)
		}
		var backends []admin.ProxyBackend
		if err := json.Unmarshal(rec.Body.Bytes(), &backends); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(backends) != 1 {
			t.Fatalf("want 1, got %d", len(backends))
		}
	})

	t.Run("get public backend by name", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/api/public/backends/pub-backend", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d; body=%s", rec.Code, rec.Body.String())
		}
		var pb admin.ProxyBackend
		if err := json.Unmarshal(rec.Body.Bytes(), &pb); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if pb.Name != "pub-backend" {
			t.Errorf("want pub-backend, got %q", pb.Name)
		}
	})

	t.Run("get public backend not found", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/api/public/backends/nonexistent", nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("want 404, got %d", rec.Code)
		}
	})
}

func TestAdmin_RoleEnforcement(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()

	// Build an admin with no auth roles granted (empty regexes → always deny).
	noRoleCfg := admin.Config{
		Auth: config.AuthConfig{
			Type: "NOOP",
			Authorization: config.AuthorizationConfig{
				AdminRegex: "", // no match → deny
				UserRegex:  "",
				APIRegex:   "",
			},
		},
		Backends:  bs,
		History:   hs,
		Monitor:   sp,
		StatusMut: sp,
		AuthMW:    auth.Noop(),
		StartTime: time.Now(),
	}
	a := admin.New(noRoleCfg)

	tests := []struct {
		name   string
		method string
		path   string
		body   []byte
	}{
		{"gateway ping requires API", http.MethodGet, "/gateway", nil},
		{"list all backends requires API", http.MethodGet, "/gateway/backend/all", nil},
		{"list active backends requires API", http.MethodGet, "/gateway/backend/active", nil},
		{"entity types requires ADMIN", http.MethodGet, "/entity", nil},
		{"webapp getAllBackends requires USER", http.MethodPost, "/webapp/getAllBackends", nil},
		{"webapp getRoutingRules requires ADMIN", http.MethodPost, "/webapp/getRoutingRules", nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(a, tc.method, tc.path, tc.body)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s: want 403, got %d; body=%s", tc.method, tc.path, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAdmin_NoAuthEndpoints(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()

	// No-role config — but public endpoints should still work.
	noRoleCfg := admin.Config{
		Auth: config.AuthConfig{
			Type: "NOOP",
			Authorization: config.AuthorizationConfig{
				AdminRegex: "",
				UserRegex:  "",
				APIRegex:   "",
			},
		},
		Backends:  bs,
		History:   hs,
		Monitor:   sp,
		StatusMut: sp,
		AuthMW:    auth.Noop(),
		StartTime: time.Now(),
	}
	a := admin.New(noRoleCfg)

	t.Run("livez is accessible without auth", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/trino-gateway/livez", nil)
		if rec.Code != http.StatusOK {
			t.Errorf("want 200, got %d", rec.Code)
		}
	})

	t.Run("public backends endpoint accessible without auth", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/api/public/backends", nil)
		if rec.Code != http.StatusOK {
			t.Errorf("want 200, got %d; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("root redirect", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/", nil)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("want 303, got %d", rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "/trino-gateway" {
			t.Errorf("redirect location: want /trino-gateway, got %q", loc)
		}
	})
}

func TestAdmin_QueryHistoryScoping(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{
		records: []persistence.QueryRecord{
			{QueryID: "q1", UserName: "alice", BackendURL: "http://b1", CreatedAt: time.Now()},
			{QueryID: "q2", UserName: "bob", BackendURL: "http://b2", CreatedAt: time.Now()},
		},
	}
	sp := newFakeStatusProvider()

	// Build admin where only USER role is granted (not ADMIN).
	// The noop middleware sets MemberOf:""; use a pattern that won't match empty string.
	userOnlyCfg := admin.Config{
		Auth: config.AuthConfig{
			Type: "NOOP",
			Authorization: config.AuthorizationConfig{
				AdminRegex: "^ADMIN$", // never matches "" (noop MemberOf)
				UserRegex:  ".*",      // matches everything (including "")
				APIRegex:   "^API$",
			},
		},
		Backends:  bs,
		History:   hs,
		Monitor:   sp,
		StatusMut: sp,
		AuthMW:    auth.Noop(),
		StartTime: time.Now(),
	}
	a := admin.New(userOnlyCfg)

	t.Run("non-admin queryHistory scoped to caller", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/trino-gateway/api/queryHistory", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d; body=%s", rec.Code, rec.Body.String())
		}
		var results []admin.QueryDetail
		if err := json.Unmarshal(rec.Body.Bytes(), &results); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		// The noop middleware sets principal.Name = "anonymous", which won't match alice or bob.
		// So we expect 0 results (scoped to "anonymous").
		for _, qd := range results {
			if qd.User != "anonymous" {
				t.Errorf("non-admin should only see own records, got user %q", qd.User)
			}
		}
	})
}

// TestAdmin_QueryHistoryExternalURL covers audit §3.7/M5: QueryDetail.externalUrl
// carries the captured external URL and is always present, falling back to
// backendUrl for rows captured before external_url was recorded.
func TestAdmin_QueryHistoryExternalURL(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{
		records: []persistence.QueryRecord{
			{
				QueryID:     "q-explicit",
				UserName:    "alice",
				BackendURL:  "http://b1:8080",
				ExternalURL: "https://b1.example:443",
				CreatedAt:   time.Now(),
			},
			{
				QueryID:    "q-fallback",
				UserName:   "alice",
				BackendURL: "http://b2:8080",
				// ExternalURL empty → falls back to BackendURL on the wire.
				CreatedAt: time.Now(),
			},
		},
	}
	sp := newFakeStatusProvider()
	a := admin.New(adminCfgNoAuth(bs, hs, sp))

	rec := do(a, http.MethodGet, "/trino-gateway/api/queryHistory", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%s", rec.Code, rec.Body.String())
	}

	// externalUrl must always be present in the wire JSON.
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	for i, item := range raw {
		if _, ok := item["externalUrl"]; !ok {
			t.Errorf("record %d: externalUrl field absent from wire JSON: %s", i, rec.Body.String())
		}
	}

	var results []admin.QueryDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &results); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	byID := make(map[string]admin.QueryDetail, len(results))
	for _, qd := range results {
		byID[qd.QueryID] = qd
	}

	if got := byID["q-explicit"].ExternalURL; got != "https://b1.example:443" {
		t.Errorf("explicit externalUrl: want %q, got %q", "https://b1.example:443", got)
	}
	if got := byID["q-fallback"].ExternalURL; got != "http://b2:8080" {
		t.Errorf("fallback externalUrl: want backendUrl %q, got %q", "http://b2:8080", got)
	}
}

func TestAdmin_EntityEndpoints(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()
	a := admin.New(adminCfgNoAuth(bs, hs, sp))

	t.Run("list entity types", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/entity", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d", rec.Code)
		}
		var types []string
		if err := json.Unmarshal(rec.Body.Bytes(), &types); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(types) == 0 {
			t.Error("want at least one entity type")
		}
		if types[0] != "GATEWAY_BACKEND" {
			t.Errorf("want GATEWAY_BACKEND, got %q", types[0])
		}
	})

	t.Run("upsert entity sets monitor status pending when active", func(t *testing.T) {
		pb := admin.ProxyBackend{
			Name:    "entity-backend",
			ProxyTo: "http://entity-backend:8080",
			Active:  true,
		}
		rec := do(a, http.MethodPost, "/entity?entityType=GATEWAY_BACKEND", mustJSON(t, pb))
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d; body=%s", rec.Code, rec.Body.String())
		}
		// StatusMut should have been called with StatusPending.
		status := sp.statuses["http://entity-backend:8080"]
		if status != monitor.StatusPending {
			t.Errorf("want StatusPending, got %v", status)
		}
	})

	t.Run("upsert entity sets monitor status unhealthy when inactive", func(t *testing.T) {
		pb := admin.ProxyBackend{
			Name:    "entity-backend",
			ProxyTo: "http://entity-backend:8080",
			Active:  false,
		}
		rec := do(a, http.MethodPost, "/entity?entityType=GATEWAY_BACKEND", mustJSON(t, pb))
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d; body=%s", rec.Code, rec.Body.String())
		}
		status := sp.statuses["http://entity-backend:8080"]
		if status != monitor.StatusUnhealthy {
			t.Errorf("want StatusUnhealthy, got %v", status)
		}
	})

	t.Run("upsert entity unknown type returns error", func(t *testing.T) {
		pb := admin.ProxyBackend{Name: "x", ProxyTo: "http://x"}
		rec := do(a, http.MethodPost, "/entity?entityType=UNKNOWN", mustJSON(t, pb))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("want 500, got %d", rec.Code)
		}
	})

	t.Run("list entities by type", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/entity/GATEWAY_BACKEND", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d", rec.Code)
		}
		var backends []admin.ProxyBackend
		if err := json.Unmarshal(rec.Body.Bytes(), &backends); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(backends) == 0 {
			t.Error("want at least one backend after upsert")
		}
	})
}

// TestAdmin_EntityAPI_UnknownTypeReturnsEmptyArray verifies that GET /entity/{otherType}
// returns 200 with an empty JSON array, matching Java's "200 []" behaviour.
func TestAdmin_EntityAPI_UnknownTypeReturnsEmptyArray(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()
	a := admin.New(adminCfgNoAuth(bs, hs, sp))

	rec := do(a, http.MethodGet, "/entity/UNKNOWN_TYPE", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%s", rec.Code, rec.Body.String())
	}

	var items []interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("unmarshal: %v; raw=%s", err, rec.Body.String())
	}
	if len(items) != 0 {
		t.Errorf("want empty array, got %d items: %v", len(items), items)
	}
}

func TestAdmin_WebappEndpoints(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()
	a := admin.New(adminCfgNoAuth(bs, hs, sp))

	// Seed some backends.
	_ = bs.Upsert(context.Background(), persistence.Backend{
		Name: "wa-backend", URL: "http://wa:8080", Active: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	sp.statuses["http://wa:8080"] = monitor.StatusHealthy

	t.Run("getAllBackends", func(t *testing.T) {
		rec := do(a, http.MethodPost, "/webapp/getAllBackends", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d; body=%s", rec.Code, rec.Body.String())
		}
		var env admin.Result[[]admin.BackendResponse]
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if env.Code != 200 {
			t.Errorf("result code: want 200, got %d", env.Code)
		}
		if len(env.Data) != 1 {
			t.Errorf("want 1 backend, got %d", len(env.Data))
		}
		if env.Data[0].Status != "HEALTHY" {
			t.Errorf("want HEALTHY, got %q", env.Data[0].Status)
		}
	})

	t.Run("getDistribution", func(t *testing.T) {
		rec := do(a, http.MethodPost, "/webapp/getDistribution", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d; body=%s", rec.Code, rec.Body.String())
		}
		var env admin.Result[admin.DistributionResponse]
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if env.Data.TotalBackendCount != 1 {
			t.Errorf("want 1 backend, got %d", env.Data.TotalBackendCount)
		}
		if env.Data.HealthyBackendCount != 1 {
			t.Errorf("want 1 healthy, got %d", env.Data.HealthyBackendCount)
		}
	})

	t.Run("getUIConfiguration", func(t *testing.T) {
		rec := do(a, http.MethodPost, "/webapp/getUIConfiguration", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d", rec.Code)
		}
		var env admin.Result[admin.UIConfiguration]
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if env.Data.AuthType != "NOOP" {
			t.Errorf("want NOOP, got %q", env.Data.AuthType)
		}
	})

	t.Run("getRoutingRules returns 204 (external routing)", func(t *testing.T) {
		// The Go gateway is external-routing-only, so rules are managed by the
		// external service; the handler signals this with 204 No Content (Java
		// parity), which the UI reads as "external routing in use".
		rec := do(a, http.MethodPost, "/webapp/getRoutingRules", nil)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("want 204, got %d", rec.Code)
		}
		if rec.Body.Len() != 0 {
			t.Errorf("204 must have no body, got %q", rec.Body.String())
		}
	})

	t.Run("saveBackend then deleteBackend", func(t *testing.T) {
		pb := admin.ProxyBackend{Name: "new-be", ProxyTo: "http://new:8080", Active: true}
		rec := do(a, http.MethodPost, "/webapp/saveBackend", mustJSON(t, pb))
		if rec.Code != http.StatusOK {
			t.Fatalf("save: want 200, got %d; body=%s", rec.Code, rec.Body.String())
		}

		rec2 := do(a, http.MethodPost, "/webapp/deleteBackend", mustJSON(t, pb))
		if rec2.Code != http.StatusOK {
			t.Fatalf("delete: want 200, got %d; body=%s", rec2.Code, rec2.Body.String())
		}
		var env admin.Result[bool]
		if err := json.Unmarshal(rec2.Body.Bytes(), &env); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !env.Data {
			t.Error("want true for successful delete")
		}
	})
}

func TestAdmin_LoginEndpoints(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()
	a := admin.New(adminCfgNoAuth(bs, hs, sp))

	t.Run("loginType returns none for NOOP", func(t *testing.T) {
		rec := do(a, http.MethodPost, "/loginType", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d", rec.Code)
		}
		var env admin.Result[string]
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if env.Data != "none" {
			t.Errorf("want none, got %q", env.Data)
		}
	})

	t.Run("login with NOOP returns username as token", func(t *testing.T) {
		body := mustJSON(t, map[string]string{"username": "testuser", "password": "pass"})
		rec := do(a, http.MethodPost, "/login", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d", rec.Code)
		}
		var env admin.Result[map[string]string]
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if env.Data["token"] != "testuser" {
			t.Errorf("want testuser, got %q", env.Data["token"])
		}
	})

	t.Run("logout returns 200", func(t *testing.T) {
		rec := do(a, http.MethodPost, "/logout", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d", rec.Code)
		}
	})
}
