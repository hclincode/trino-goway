package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hclincode/trino-goway/internal/admin"
	"github.com/hclincode/trino-goway/internal/auth"
	"github.com/hclincode/trino-goway/internal/config"
	"github.com/hclincode/trino-goway/internal/monitor"
	"github.com/hclincode/trino-goway/internal/persistence"
	"github.com/hclincode/trino-goway/internal/testutil"
)

// TestMain installs the project-wide goroutine-leak verifier for the admin package.
func TestMain(m *testing.M) {
	testutil.VerifyTestMain(m)
}

// ---- Role-aware fakes for degradation paths ----

// errorBackendStore is a BackendStore that returns a configurable error from every call.
// Used to exercise 500-degradation paths.
type errorBackendStore struct {
	err error
}

func (e *errorBackendStore) List(context.Context) ([]persistence.Backend, error) {
	return nil, e.err
}
func (e *errorBackendStore) ListActive(context.Context) ([]persistence.Backend, error) {
	return nil, e.err
}
func (e *errorBackendStore) Upsert(context.Context, persistence.Backend) error { return e.err }
func (e *errorBackendStore) Delete(context.Context, string) error              { return e.err }
func (e *errorBackendStore) SetActive(context.Context, string, bool) error     { return e.err }

// errorHistoryStore returns a configurable error from FindByFilter / ListRecent.
type errorHistoryStore struct {
	err error
}

func (e *errorHistoryStore) Insert(context.Context, persistence.QueryRecord) error { return e.err }
func (e *errorHistoryStore) ListRecent(context.Context, int) ([]persistence.QueryRecord, error) {
	return nil, e.err
}
func (e *errorHistoryStore) FindByFilter(context.Context, persistence.HistoryFilter) ([]persistence.QueryRecord, int64, error) {
	return nil, 0, e.err
}
func (e *errorHistoryStore) FindDistribution(context.Context, time.Time) ([]persistence.DistributionBucket, error) {
	return nil, e.err
}

// ---- Helpers ----

// cfgWithPrincipal builds an admin.Config where role regexes are explicit and the auth
// middleware always attaches the given principal. The caller picks the regexes to control
// which roles `p` resolves to.
func cfgWithPrincipal(bs admin.BackendStore, hs admin.HistoryStore, sp *fakeStatusProvider,
	p *auth.Principal, adminRE, userRE, apiRE string) admin.Config {
	return admin.Config{
		Auth: config.AuthConfig{
			Type: "NOOP",
			Authorization: config.AuthorizationConfig{
				AdminRegex: adminRE,
				UserRegex:  userRE,
				APIRegex:   apiRE,
			},
		},
		Backends:  bs,
		History:   hs,
		Monitor:   sp,
		StatusMut: sp,
		AuthMW:    auth.NewTestMiddleware(p),
		StartTime: time.Now(),
	}
}

// principalAdmin matches AdminRegex "admins", UserRegex "admins|users", APIRegex "admins|api".
var principalAdmin = &auth.Principal{Name: "alice", MemberOf: "admins"}

// principalUser matches only UserRegex "admins|users".
var principalUser = &auth.Principal{Name: "bob", MemberOf: "users"}

// principalAPI matches only APIRegex "admins|api".
var principalAPI = &auth.Principal{Name: "svc", MemberOf: "api"}

// principalNone matches none of the role regexes.
var principalNone = &auth.Principal{Name: "nobody", MemberOf: "guests"}

const (
	reAdmin = "admins"
	reUser  = "admins|users"
	reAPI   = "admins|api"
)

// ---- 1. Per-endpoint role enforcement matrix ----

// TestAdmin_RoleMatrix_WriteEndpoints403 checks that every write/admin endpoint returns
// 403 to callers without the right role. Also covers cross-role denial: USER-only
// principals hitting ADMIN-only endpoints and API-only principals hitting USER-only.
func TestAdmin_RoleMatrix_WriteEndpoints403(t *testing.T) {
	// Each row carries the endpoint and which principals should be rejected (403).
	type row struct {
		name      string
		method    string
		path      string
		body      []byte
		denyAdmin bool // expects 403 when caller has ADMIN role only (no USER, no API)
		denyUser  bool // expects 403 when caller has USER role only
		denyAPI   bool // expects 403 when caller has API role only
		denyNone  bool // expects 403 when caller has no roles
	}

	// Each row asserts which roles are DENIED. Roles required by each route per router.go:
	//   API   role: /gateway/*, /gateway/backend/*
	//   ADMIN role: /entity, /webapp/{getRoutingRules, updateRoutingRules, saveBackend, updateBackend, deleteBackend}
	//   USER  role: /webapp/{getAllBackends, findQueryHistory, getDistribution, getUIConfiguration}
	//                /trino-gateway/api/{queryHistory, activeBackends, queryHistoryDistribution}
	rows := []row{
		// API-required (gateway/*): USER alone and ADMIN-alone are denied (cross-role denial).
		{"add backend (API only)", http.MethodPost, "/gateway/backend/modify/add",
			mustJSON(t, admin.ProxyBackend{Name: "x", ProxyTo: "http://x"}),
			true, true, false, true},
		{"update backend (API only)", http.MethodPost, "/gateway/backend/modify/update",
			mustJSON(t, admin.ProxyBackend{Name: "x", ProxyTo: "http://x"}),
			true, true, false, true},
		{"delete backend (API only)", http.MethodPost, "/gateway/backend/modify/delete",
			[]byte("x"),
			true, true, false, true},
		{"activate backend (API only)", http.MethodPost, "/gateway/backend/activate/x", nil,
			true, true, false, true},
		{"deactivate backend (API only)", http.MethodPost, "/gateway/backend/deactivate/x", nil,
			true, true, false, true},

		// ADMIN-required: USER and API alone are denied.
		{"upsert entity (ADMIN only)", http.MethodPost, "/entity?entityType=GATEWAY_BACKEND",
			mustJSON(t, admin.ProxyBackend{Name: "x", ProxyTo: "http://x"}),
			false, true, true, true},
		{"webapp saveBackend (ADMIN only)", http.MethodPost, "/webapp/saveBackend",
			mustJSON(t, admin.ProxyBackend{Name: "x", ProxyTo: "http://x"}),
			false, true, true, true},
		{"webapp updateBackend (ADMIN only)", http.MethodPost, "/webapp/updateBackend",
			mustJSON(t, admin.ProxyBackend{Name: "x", ProxyTo: "http://x"}),
			false, true, true, true},
		{"webapp deleteBackend (ADMIN only)", http.MethodPost, "/webapp/deleteBackend",
			mustJSON(t, admin.ProxyBackend{Name: "x"}),
			false, true, true, true},
		{"webapp updateRoutingRules (ADMIN only)", http.MethodPost, "/webapp/updateRoutingRules", nil,
			false, true, true, true},

		// USER-required: ADMIN-only (no USER regex) AND API-only are both denied (cross-role).
		// Role checks are independent regex matches — granting only ADMIN does not implicitly grant USER.
		{"webapp findQueryHistory (USER only)", http.MethodPost, "/webapp/findQueryHistory",
			mustJSON(t, admin.FindQueryHistoryRequest{}),
			true, false, true, true},
	}

	for _, tc := range rows {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Build four admin instances, each granting exactly one role to the caller.
			cases := []struct {
				label     string
				p         *auth.Principal
				adminRE   string
				userRE    string
				apiRE     string
				expect403 bool
			}{
				{"admin-only", principalAdmin, "admins", "", "", tc.denyAdmin},
				{"user-only", principalUser, "", "users", "", tc.denyUser},
				{"api-only", principalAPI, "", "", "api", tc.denyAPI},
				{"no-role", principalNone, "admins", "users", "api", tc.denyNone},
			}
			for _, c := range cases {
				c := c
				t.Run(c.label, func(t *testing.T) {
					bs := newFakeBackendStore()
					hs := &fakeHistoryStore{}
					sp := newFakeStatusProvider()
					a := admin.New(cfgWithPrincipal(bs, hs, sp, c.p, c.adminRE, c.userRE, c.apiRE))
					rec := do(a, tc.method, tc.path, tc.body)
					if c.expect403 && rec.Code != http.StatusForbidden {
						t.Errorf("%s %s: want 403, got %d; body=%s",
							tc.method, tc.path, rec.Code, rec.Body.String())
					}
					if !c.expect403 && rec.Code == http.StatusForbidden {
						t.Errorf("%s %s: want allowed, got 403; body=%s",
							tc.method, tc.path, rec.Body.String())
					}
				})
			}
		})
	}
}

// ---- 2. Query history scoping — non-vacuous truth ----

// TestAdmin_QueryHistoryScoping_NonAdminSeesOwnRecord seeds a record for the caller's
// username and asserts it IS returned (fixes the vacuous-truth gap in the existing
// TestAdmin_QueryHistoryScoping which only asserted absence).
func TestAdmin_QueryHistoryScoping_NonAdminSeesOwnRecord(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{
		records: []persistence.QueryRecord{
			{QueryID: "q-bob", UserName: "bob", BackendURL: "http://b1", CreatedAt: time.Now()},
			{QueryID: "q-alice", UserName: "alice", BackendURL: "http://b2", CreatedAt: time.Now()},
			{QueryID: "q-carol", UserName: "carol", BackendURL: "http://b3", CreatedAt: time.Now()},
		},
	}
	sp := newFakeStatusProvider()

	// Caller is "bob" with USER role only — must see only his own record.
	a := admin.New(cfgWithPrincipal(bs, hs, sp, principalUser, reAdmin, reUser, reAPI))

	rec := do(a, http.MethodGet, "/trino-gateway/api/queryHistory", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%s", rec.Code, rec.Body.String())
	}
	var got []admin.QueryDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("non-admin must see exactly own record; got %d records: %+v", len(got), got)
	}
	if got[0].User != "bob" || got[0].QueryID != "q-bob" {
		t.Errorf("non-admin saw wrong record: %+v", got[0])
	}
}

// TestAdmin_QueryHistoryScoping_AdminSeesAll asserts that an ADMIN caller receives all
// records (multiple users) via ListRecent — closes the "admin path never exercised" gap.
func TestAdmin_QueryHistoryScoping_AdminSeesAll(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{
		records: []persistence.QueryRecord{
			{QueryID: "q-bob", UserName: "bob", BackendURL: "http://b1", CreatedAt: time.Now()},
			{QueryID: "q-alice", UserName: "alice", BackendURL: "http://b2", CreatedAt: time.Now()},
			{QueryID: "q-carol", UserName: "carol", BackendURL: "http://b3", CreatedAt: time.Now()},
		},
	}
	sp := newFakeStatusProvider()

	a := admin.New(cfgWithPrincipal(bs, hs, sp, principalAdmin, reAdmin, reUser, reAPI))

	rec := do(a, http.MethodGet, "/trino-gateway/api/queryHistory", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var got []admin.QueryDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("admin must see all records; got %d: %+v", len(got), got)
	}
	seen := map[string]bool{}
	for _, qd := range got {
		seen[qd.User] = true
	}
	for _, want := range []string{"alice", "bob", "carol"} {
		if !seen[want] {
			t.Errorf("admin missing record for user %q", want)
		}
	}
}

// ---- 3. Happy-path coverage for previously-uncovered endpoints ----

// TestAdmin_UpdateBackend_HappyPath exercises POST /gateway/backend/modify/update.
func TestAdmin_UpdateBackend_HappyPath(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()
	a := admin.New(cfgWithPrincipal(bs, hs, sp, principalAPI, reAdmin, reUser, reAPI))

	// Pre-seed.
	_ = bs.Upsert(context.Background(), persistence.Backend{
		Name: "be-u", URL: "http://old:8080", Active: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})

	pb := admin.ProxyBackend{Name: "be-u", ProxyTo: "http://new:8080", Active: false, RoutingGroup: "adhoc"}
	rec := do(a, http.MethodPost, "/gateway/backend/modify/update", mustJSON(t, pb))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%s", rec.Code, rec.Body.String())
	}
	got := bs.backends["be-u"]
	if got.URL != "http://new:8080" || got.RoutingGroup != "adhoc" || got.Active {
		t.Errorf("update did not propagate: %+v", got)
	}
}

// TestAdmin_WebappUpdateBackend_HappyPath exercises POST /webapp/updateBackend.
func TestAdmin_WebappUpdateBackend_HappyPath(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()
	a := admin.New(cfgWithPrincipal(bs, hs, sp, principalAdmin, reAdmin, reUser, reAPI))

	_ = bs.Upsert(context.Background(), persistence.Backend{
		Name: "wa-u", URL: "http://wa-old:8080", Active: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})

	pb := admin.ProxyBackend{Name: "wa-u", ProxyTo: "http://wa-new:8080", Active: true}
	rec := do(a, http.MethodPost, "/webapp/updateBackend", mustJSON(t, pb))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%s", rec.Code, rec.Body.String())
	}
	var env admin.Result[admin.ProxyBackend]
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Code != 200 || env.Data.ProxyTo != "http://wa-new:8080" {
		t.Errorf("update result wrong: code=%d, data=%+v", env.Code, env.Data)
	}
	if bs.backends["wa-u"].URL != "http://wa-new:8080" {
		t.Errorf("backend not updated in store: %+v", bs.backends["wa-u"])
	}
}

// TestAdmin_WebappUpdateRoutingRules_HappyPath exercises the (v1 stub) endpoint.
func TestAdmin_WebappUpdateRoutingRules_HappyPath(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()
	a := admin.New(cfgWithPrincipal(bs, hs, sp, principalAdmin, reAdmin, reUser, reAPI))

	rec := do(a, http.MethodPost, "/webapp/updateRoutingRules", []byte("[]"))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var env admin.Result[[]admin.RoutingRule]
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Code != 200 {
		t.Errorf("want code 200, got %d", env.Code)
	}
}

// TestAdmin_WebappFindQueryHistory_HappyPath returns the caller's own records
// (non-admin → username is forced) and validates pagination envelope.
func TestAdmin_WebappFindQueryHistory_HappyPath(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{
		records: []persistence.QueryRecord{
			{QueryID: "q-bob-1", UserName: "bob", BackendURL: "http://b1", CreatedAt: time.Now()},
			{QueryID: "q-bob-2", UserName: "bob", BackendURL: "http://b1", CreatedAt: time.Now()},
			{QueryID: "q-alice-1", UserName: "alice", BackendURL: "http://b2", CreatedAt: time.Now()},
		},
	}
	sp := newFakeStatusProvider()

	// USER-only principal "bob".
	a := admin.New(cfgWithPrincipal(bs, hs, sp, principalUser, reAdmin, reUser, reAPI))

	// Even though the request asks for user "alice", the handler must force the username to "bob".
	body := mustJSON(t, admin.FindQueryHistoryRequest{UserName: "alice", PageSize: 50, Page: 1})
	rec := do(a, http.MethodPost, "/webapp/findQueryHistory", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%s", rec.Code, rec.Body.String())
	}
	var env admin.Result[admin.TableData[admin.QueryDetail]]
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Data.Total != 2 || len(env.Data.Rows) != 2 {
		t.Fatalf("want 2 records for bob, got total=%d rows=%d", env.Data.Total, len(env.Data.Rows))
	}
	for _, qd := range env.Data.Rows {
		if qd.User != "bob" {
			t.Errorf("found wrong user %q in non-admin result", qd.User)
		}
	}
}

// TestAdmin_GetPublicBackendState_HappyPath exercises GET /api/public/backends/{name}/state.
func TestAdmin_GetPublicBackendState_HappyPath(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()

	_ = bs.Upsert(context.Background(), persistence.Backend{
		Name: "be-state", URL: "http://be-state:8080", Active: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	sp.statuses["http://be-state:8080"] = monitor.StatusHealthy

	a := admin.New(adminCfgNoAuth(bs, hs, sp))

	rec := do(a, http.MethodGet, "/api/public/backends/be-state/state", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	// M7: the public-state endpoint now returns the ClusterStatsResponse shape.
	var cs admin.ClusterStatsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &cs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cs.ClusterID != "be-state" {
		t.Errorf("clusterId: got %q", cs.ClusterID)
	}
	if cs.TrinoStatus != "HEALTHY" {
		t.Errorf("trinoStatus: want HEALTHY, got %q", cs.TrinoStatus)
	}
	if cs.ProxyTo != "http://be-state:8080" {
		t.Errorf("proxyTo: got %q", cs.ProxyTo)
	}
	// No StatsProvider configured ⇒ counts 0, userQueuedCount absent.
	if cs.QueuedQueryCount != 0 || cs.RunningQueryCount != 0 {
		t.Errorf("counts: want 0/0, got %d/%d", cs.QueuedQueryCount, cs.RunningQueryCount)
	}
	if cs.UserQueuedCount != nil {
		t.Errorf("userQueuedCount: want nil, got %v", cs.UserQueuedCount)
	}
}

// TestAdmin_GetPublicBackendState_NotFound returns 404 for a missing backend.
func TestAdmin_GetPublicBackendState_NotFound(t *testing.T) {
	a := admin.New(adminCfgNoAuth(newFakeBackendStore(), &fakeHistoryStore{}, newFakeStatusProvider()))
	rec := do(a, http.MethodGet, "/api/public/backends/missing/state", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", rec.Code)
	}
}

// TestAdmin_HandleGatewayPing_HappyPath exercises GET /gateway.
func TestAdmin_HandleGatewayPing_HappyPath(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()
	a := admin.New(cfgWithPrincipal(bs, hs, sp, principalAPI, reAdmin, reUser, reAPI))

	rec := do(a, http.MethodGet, "/gateway", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var got string
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got != "ok" {
		t.Errorf("want \"ok\", got %q", got)
	}
}

// TestAdmin_LegacyActiveBackends_HappyPath exercises GET /trino-gateway/api/activeBackends.
func TestAdmin_LegacyActiveBackends_HappyPath(t *testing.T) {
	bs := newFakeBackendStore()
	_ = bs.Upsert(context.Background(), persistence.Backend{
		Name: "be-active", URL: "http://be-active:8080", Active: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	_ = bs.Upsert(context.Background(), persistence.Backend{
		Name: "be-inactive", URL: "http://be-inactive:8080", Active: false,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()
	a := admin.New(cfgWithPrincipal(bs, hs, sp, principalUser, reAdmin, reUser, reAPI))

	rec := do(a, http.MethodGet, "/trino-gateway/api/activeBackends", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var got []admin.ProxyBackend
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 || got[0].Name != "be-active" {
		t.Errorf("legacyActiveBackends: want one active, got %+v", got)
	}
}

// TestAdmin_QueryHistoryDistribution_HappyPath exercises GET /trino-gateway/api/queryHistoryDistribution.
// USER caller is scoped to own queries, ADMIN sees all.
func TestAdmin_QueryHistoryDistribution_HappyPath(t *testing.T) {
	hs := &fakeHistoryStore{
		records: []persistence.QueryRecord{
			{QueryID: "q1", UserName: "bob", BackendURL: "http://b1", CreatedAt: time.Now()},
			{QueryID: "q2", UserName: "bob", BackendURL: "http://b1", CreatedAt: time.Now()},
			{QueryID: "q3", UserName: "bob", BackendURL: "http://b2", CreatedAt: time.Now()},
			{QueryID: "q4", UserName: "alice", BackendURL: "http://b2", CreatedAt: time.Now()},
		},
	}

	t.Run("non-admin scoped", func(t *testing.T) {
		bs := newFakeBackendStore()
		sp := newFakeStatusProvider()
		a := admin.New(cfgWithPrincipal(bs, hs, sp, principalUser, reAdmin, reUser, reAPI))
		rec := do(a, http.MethodGet, "/trino-gateway/api/queryHistoryDistribution", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d", rec.Code)
		}
		var dist map[string]int64
		if err := json.Unmarshal(rec.Body.Bytes(), &dist); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		// Bob has 2 on b1, 1 on b2; alice's record must be excluded.
		if dist["http://b1"] != 2 || dist["http://b2"] != 1 {
			t.Errorf("non-admin distribution wrong: %v", dist)
		}
	})

	t.Run("admin sees all", func(t *testing.T) {
		bs := newFakeBackendStore()
		sp := newFakeStatusProvider()
		a := admin.New(cfgWithPrincipal(bs, hs, sp, principalAdmin, reAdmin, reUser, reAPI))
		rec := do(a, http.MethodGet, "/trino-gateway/api/queryHistoryDistribution", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d", rec.Code)
		}
		var dist map[string]int64
		_ = json.Unmarshal(rec.Body.Bytes(), &dist)
		// All four records: b1=2 (bob), b2=2 (bob+alice).
		if dist["http://b1"] != 2 || dist["http://b2"] != 2 {
			t.Errorf("admin distribution wrong: %v", dist)
		}
	})
}

// ---- 4. Degradation paths ----

// TestAdmin_BackendStoreError_PublicEndpoints500 exercises BackendStore.List failure on
// the public endpoint chain (no auth required, so easiest to hit).
func TestAdmin_BackendStoreError_PublicEndpoints500(t *testing.T) {
	bs := &errorBackendStore{err: errors.New("db connection refused")}
	sp := newFakeStatusProvider()
	a := admin.New(admin.Config{
		Auth: config.AuthConfig{
			Type: "NOOP",
			Authorization: config.AuthorizationConfig{
				AdminRegex: ".*", UserRegex: ".*", APIRegex: ".*",
			},
		},
		Backends:  bs,
		History:   &fakeHistoryStore{},
		Monitor:   sp,
		StatusMut: sp,
		AuthMW:    auth.Noop(),
		StartTime: time.Now(),
	})

	for _, path := range []string{
		"/api/public/backends",
		"/api/public/backends/whatever",
		"/api/public/backends/whatever/state",
		"/gateway/backend/all",
		"/gateway/backend/active",
		"/trino-gateway/api/activeBackends",
		"/entity/GATEWAY_BACKEND",
	} {
		path := path
		t.Run(path, func(t *testing.T) {
			rec := do(a, http.MethodGet, path, nil)
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("%s: want 500, got %d; body=%s", path, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestAdmin_HistoryStoreError_QueryHistory500 — FindByFilter / ListRecent error → 500.
func TestAdmin_HistoryStoreError_QueryHistory500(t *testing.T) {
	cases := []struct {
		name string
		p    *auth.Principal // controls admin vs. non-admin path
	}{
		{"admin path (ListRecent)", principalAdmin},
		{"user path (FindByFilter)", principalUser},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			bs := newFakeBackendStore()
			hs := &errorHistoryStore{err: errors.New("db read error")}
			sp := newFakeStatusProvider()
			a := admin.New(cfgWithPrincipal(bs, hs, sp, c.p, reAdmin, reUser, reAPI))

			rec := do(a, http.MethodGet, "/trino-gateway/api/queryHistory", nil)
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("want 500, got %d; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestAdmin_QueryHistoryDistribution_HistoryError500 verifies degradation on the
// distribution endpoint when FindByFilter fails.
func TestAdmin_QueryHistoryDistribution_HistoryError500(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &errorHistoryStore{err: errors.New("disk full")}
	sp := newFakeStatusProvider()
	a := admin.New(cfgWithPrincipal(bs, hs, sp, principalUser, reAdmin, reUser, reAPI))

	rec := do(a, http.MethodGet, "/trino-gateway/api/queryHistoryDistribution", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", rec.Code)
	}
}

// TestAdmin_MalformedJSON_400 verifies that gateway-API write endpoints return 400
// for malformed JSON bodies. (Webapp endpoints intentionally return 200+envelope.)
func TestAdmin_MalformedJSON_400(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()
	// API role to clear the gateway routes; ADMIN to clear the /entity route.
	a := admin.New(cfgWithPrincipal(bs, hs, sp, principalAdmin, reAdmin, reUser, reAPI))

	cases := []struct {
		path string
	}{
		{"/gateway/backend/modify/add"},
		{"/gateway/backend/modify/update"},
		{"/entity?entityType=GATEWAY_BACKEND"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.path, func(t *testing.T) {
			rec := do(a, http.MethodPost, tc.path, []byte("{not json"))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s: want 400, got %d; body=%s", tc.path, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestAdmin_DeleteBackend_EmptyBody_400 verifies the special-case empty-name path.
func TestAdmin_DeleteBackend_EmptyBody_400(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()
	a := admin.New(cfgWithPrincipal(bs, hs, sp, principalAPI, reAdmin, reUser, reAPI))

	rec := do(a, http.MethodPost, "/gateway/backend/modify/delete", []byte("   "))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rec.Code)
	}
}

// TestAdmin_Activate_UnknownBackend_404 verifies the activate handler surfaces SetActive errors.
func TestAdmin_Activate_UnknownBackend_404(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()
	a := admin.New(cfgWithPrincipal(bs, hs, sp, principalAPI, reAdmin, reUser, reAPI))

	rec := do(a, http.MethodPost, "/gateway/backend/activate/does-not-exist", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d; body=%s", rec.Code, rec.Body.String())
	}
}

// TestAdmin_WebappWriteEndpoints_StoreError200WithErrorEnvelope verifies that
// /webapp/saveBackend, /webapp/updateBackend, and /webapp/deleteBackend translate
// underlying store failures into a 200 + resultErr envelope (Java parity: the
// webapp surface never returns 5xx; failures are signalled via Code/Msg in the
// JSON envelope so the React app can render a banner).
func TestAdmin_WebappWriteEndpoints_StoreError200WithErrorEnvelope(t *testing.T) {
	bs := &errorBackendStore{err: errors.New("db write failed")}
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()
	a := admin.New(cfgWithPrincipal(bs, hs, sp, principalAdmin, reAdmin, reUser, reAPI))

	pb := admin.ProxyBackend{Name: "webapp-fail", ProxyTo: "http://x:8080", Active: true}
	body := mustJSON(t, pb)

	cases := []struct {
		name    string
		path    string
		wantMsg string
	}{
		{"saveBackend", "/webapp/saveBackend", "failed to save backend"},
		{"updateBackend", "/webapp/updateBackend", "failed to update backend"},
		{"deleteBackend", "/webapp/deleteBackend", "failed to delete backend"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			rec := do(a, http.MethodPost, tc.path, body)
			if rec.Code != http.StatusOK {
				t.Fatalf("want HTTP 200, got %d; body=%s", rec.Code, rec.Body.String())
			}
			var env admin.Result[any]
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("unmarshal envelope: %v; body=%s", err, rec.Body.String())
			}
			if env.Code != 500 {
				t.Errorf("envelope Code: want 500, got %d", env.Code)
			}
			if env.Msg != tc.wantMsg {
				t.Errorf("envelope Msg: want %q, got %q", tc.wantMsg, env.Msg)
			}
		})
	}
}

// TestAdmin_WebappWriteEndpoints_MalformedJSON200WithBadRequestEnvelope verifies
// that the webapp write endpoints translate a JSON parse error into a 200 +
// `{Code:500, Msg:"bad request"}` envelope (same envelope contract as the
// store-error path; the React app distinguishes via Msg).
func TestAdmin_WebappWriteEndpoints_MalformedJSON200WithBadRequestEnvelope(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()
	a := admin.New(cfgWithPrincipal(bs, hs, sp, principalAdmin, reAdmin, reUser, reAPI))

	for _, path := range []string{
		"/webapp/saveBackend",
		"/webapp/updateBackend",
		"/webapp/deleteBackend",
	} {
		path := path
		t.Run(path, func(t *testing.T) {
			rec := do(a, http.MethodPost, path, []byte("{not json"))
			if rec.Code != http.StatusOK {
				t.Fatalf("want HTTP 200 (webapp envelope contract), got %d", rec.Code)
			}
			var env admin.Result[any]
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("unmarshal envelope: %v; body=%s", err, rec.Body.String())
			}
			if env.Code != 500 || env.Msg != "bad request" {
				t.Errorf("envelope: want {500, %q}, got {%d, %q}", "bad request", env.Code, env.Msg)
			}
		})
	}
}

// ---- 5. Concurrency + goleak ----

// concurrentSafeBackendStore wraps fakeBackendStore with a mutex so concurrent goroutines
// don't race on the underlying map. Used only by the concurrent-write test.
type concurrentSafeBackendStore struct {
	mu       sync.Mutex
	backends map[string]persistence.Backend
	upserts  atomic.Int64
}

func newConcurrentSafeStore() *concurrentSafeBackendStore {
	return &concurrentSafeBackendStore{backends: make(map[string]persistence.Backend)}
}

func (s *concurrentSafeBackendStore) List(context.Context) ([]persistence.Backend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]persistence.Backend, 0, len(s.backends))
	for _, b := range s.backends {
		out = append(out, b)
	}
	return out, nil
}
func (s *concurrentSafeBackendStore) ListActive(context.Context) ([]persistence.Backend, error) {
	return s.List(context.Background())
}
func (s *concurrentSafeBackendStore) Upsert(_ context.Context, b persistence.Backend) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backends[b.Name] = b
	s.upserts.Add(1)
	return nil
}
func (s *concurrentSafeBackendStore) Delete(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.backends, name)
	return nil
}
func (s *concurrentSafeBackendStore) SetActive(_ context.Context, name string, active bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.backends[name]
	if !ok {
		return errors.New("not found")
	}
	b.Active = active
	s.backends[name] = b
	return nil
}

// TestAdmin_ConcurrentAddBackend_Race fires N concurrent POST /gateway/backend/modify/add
// calls and verifies all succeed under -race with a goroutine-safe store. This is the
// race-detector smoke test for the admin handler chain.
func TestAdmin_ConcurrentAddBackend_Race(t *testing.T) {
	t.Parallel()

	bs := newConcurrentSafeStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()
	a := admin.New(cfgWithPrincipal(bs, hs, sp, principalAPI, reAdmin, reUser, reAPI))

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			pb := admin.ProxyBackend{
				Name:    "be-" + intToString(i),
				ProxyTo: "http://b:8080",
				Active:  true,
			}
			body := mustJSON(t, pb)
			req := httptest.NewRequest(http.MethodPost, "/gateway/backend/modify/add", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			a.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("concurrent add %d: want 200, got %d", i, rec.Code)
			}
		}()
	}
	wg.Wait()

	if got := bs.upserts.Load(); got != n {
		t.Errorf("want %d upserts, got %d", n, got)
	}
	if got := len(bs.backends); got != n {
		t.Errorf("want %d backends in store, got %d", n, got)
	}
}

// intToString is a small helper to avoid pulling in strconv just for this test.
func intToString(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
