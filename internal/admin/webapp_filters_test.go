package admin_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/admin"
	"github.com/hclincode/trino-goway/internal/persistence"
)

// TestAdmin_WebappFindQueryHistory_Filters covers audit T71: an ADMIN caller's
// userName / backendUrl / pageSize filters are honored server-side.
func TestAdmin_WebappFindQueryHistory_Filters(t *testing.T) {
	now := time.Now()
	hs := &fakeHistoryStore{
		records: []persistence.QueryRecord{
			{QueryID: "a1", UserName: "alice", BackendURL: "http://b1", CreatedAt: now},
			{QueryID: "a2", UserName: "alice", BackendURL: "http://b2", CreatedAt: now},
			{QueryID: "b1", UserName: "bob", BackendURL: "http://b1", CreatedAt: now},
			{QueryID: "b2", UserName: "bob", BackendURL: "http://b1", CreatedAt: now},
			{QueryID: "b3", UserName: "bob", BackendURL: "http://b1", CreatedAt: now},
		},
	}
	bs := newFakeBackendStore()
	sp := newFakeStatusProvider()
	// adminCfgNoAuth grants ADMIN, so the username is NOT force-overridden.
	a := admin.New(adminCfgNoAuth(bs, hs, sp))

	post := func(t *testing.T, req admin.FindQueryHistoryRequest) admin.TableData[admin.QueryDetail] {
		t.Helper()
		rec := do(a, http.MethodPost, "/webapp/findQueryHistory", mustJSON(t, req))
		require.Equal(t, http.StatusOK, rec.Code)
		var env admin.Result[admin.TableData[admin.QueryDetail]]
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
		return env.Data
	}

	t.Run("userName filter", func(t *testing.T) {
		got := post(t, admin.FindQueryHistoryRequest{UserName: "alice", Page: 1, PageSize: 50})
		assert.EqualValues(t, 2, got.Total)
		for _, row := range got.Rows {
			assert.Equal(t, "alice", row.User)
		}
	})

	t.Run("backendUrl filter", func(t *testing.T) {
		got := post(t, admin.FindQueryHistoryRequest{BackendURL: "http://b1", Page: 1, PageSize: 50})
		// a1, b1, b2, b3 are on http://b1.
		assert.EqualValues(t, 4, got.Total, "four rows on http://b1")
		for _, row := range got.Rows {
			assert.Equal(t, "http://b1", row.BackendURL)
		}
	})

	t.Run("userName + backendUrl combined", func(t *testing.T) {
		got := post(t, admin.FindQueryHistoryRequest{UserName: "bob", BackendURL: "http://b1", Page: 1, PageSize: 50})
		assert.EqualValues(t, 3, got.Total)
	})

	t.Run("pageSize limits rows", func(t *testing.T) {
		got := post(t, admin.FindQueryHistoryRequest{UserName: "bob", Page: 1, PageSize: 2})
		assert.EqualValues(t, 3, got.Total, "total reflects all matches")
		assert.Len(t, got.Rows, 2, "rows limited to pageSize")
	})
}

// TestAdmin_WebappGetRoutingRules_NoContent covers audit T71/§3.6: the Go gateway
// is external-routing-only, so getRoutingRules answers 204 (the UI reads this as
// "external routing in use"), on the POST verb the frontend uses (no 405).
func TestAdmin_WebappGetRoutingRules_NoContent(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()
	a := admin.New(adminCfgNoAuth(bs, hs, sp))

	rec := do(a, http.MethodPost, "/webapp/getRoutingRules", []byte(`{}`))
	assert.Equal(t, http.StatusNoContent, rec.Code, "external-routing gateway returns 204")
	assert.Empty(t, rec.Body.String(), "204 carries no body")

	// The route must accept POST (no 405) — the frontend posts to it.
	assert.NotEqual(t, http.StatusMethodNotAllowed, rec.Code)
}
