package admin_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/admin"
	"github.com/hclincode/trino-goway/internal/auth"
)

// TestAdmin_GetUIConfiguration_DisablePages covers audit §3.12/T70: the
// configured disablePages list is surfaced (and is always present as an array).
func TestAdmin_GetUIConfiguration_DisablePages(t *testing.T) {
	t.Run("returns configured disablePages", func(t *testing.T) {
		bs := newFakeBackendStore()
		hs := &fakeHistoryStore{}
		sp := newFakeStatusProvider()
		cfg := adminCfgNoAuth(bs, hs, sp)
		cfg.DisablePages = []string{"routingRules", "cluster"}
		a := admin.New(cfg)

		rec := do(a, http.MethodPost, "/webapp/getUIConfiguration", nil)
		require.Equal(t, http.StatusOK, rec.Code)

		var env admin.Result[admin.UIConfiguration]
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
		assert.Equal(t, []string{"routingRules", "cluster"}, env.Data.DisablePages)
	})

	t.Run("disablePages is an empty array when unconfigured, never null", func(t *testing.T) {
		bs := newFakeBackendStore()
		hs := &fakeHistoryStore{}
		sp := newFakeStatusProvider()
		a := admin.New(adminCfgNoAuth(bs, hs, sp))

		rec := do(a, http.MethodPost, "/webapp/getUIConfiguration", nil)
		require.Equal(t, http.StatusOK, rec.Code)

		// Assert the wire JSON has [] not null.
		var raw struct {
			Data struct {
				DisablePages json.RawMessage `json:"disablePages"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
		assert.Equal(t, "[]", string(raw.Data.DisablePages))
	})
}

// TestAdmin_Userinfo_PagePermissions covers audit §3.12/T70: per-role page
// permissions are resolved and returned in /userinfo.
func TestAdmin_Userinfo_PagePermissions(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()

	// principalUser resolves to USER role (MemberOf "users" matches reUser).
	cfg := cfgWithPrincipal(bs, hs, sp, principalUser, reAdmin, reUser, reAPI)
	cfg.Auth.Authorization.PagePermissions = map[string]string{
		auth.RoleUser: "dashboard_history",
	}
	a := admin.New(cfg)

	rec := do(a, http.MethodPost, "/userinfo", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var env struct {
		Data struct {
			Roles       []string `json:"roles"`
			Permissions []string `json:"permissions"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	assert.Contains(t, env.Data.Roles, auth.RoleUser)
	assert.ElementsMatch(t, []string{"dashboard", "history"}, env.Data.Permissions)
}
