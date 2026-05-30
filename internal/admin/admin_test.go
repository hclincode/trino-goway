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

	t.Run("getRoutingRules returns empty list", func(t *testing.T) {
		rec := do(a, http.MethodPost, "/webapp/getRoutingRules", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d", rec.Code)
		}
		var env admin.Result[[]admin.RoutingRule]
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if env.Code != 200 {
			t.Errorf("result code: want 200, got %d", env.Code)
		}
		if env.Data == nil || len(env.Data) != 0 {
			t.Errorf("want empty slice, got %v", env.Data)
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
