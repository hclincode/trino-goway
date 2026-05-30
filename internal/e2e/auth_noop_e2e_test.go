//go:build e2e

package e2e_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/e2e/harness"
)

// TestE2E_NOOP_ProxyPortNoAuth verifies the proxy port never requires gateway
// auth: a POST /v1/statement without Authorization succeeds (Trino handles its
// own auth per backend; the gateway never gates the proxy path).
func TestE2E_NOOP_ProxyPortNoAuth(t *testing.T) {
	h := harness.New(t)
	h.AddBackend(t, "trino-1", "default")

	req, err := http.NewRequest(http.MethodPost, h.ProxyURL+"/v1/statement", strings.NewReader("SELECT 1"))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("X-Trino-User", "anon")
	// Deliberately no Authorization header.

	resp, err := h.ProxyClient().Do(req)
	require.NoError(t, err, "POST /v1/statement")
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.Equalf(t, http.StatusOK, resp.StatusCode,
		"proxy port must not gate unauthenticated requests; body=%s", string(body))
}

// TestE2E_NOOP_AdminGrantedByRegex confirms a ".*" admin regex grants ADMIN to
// the NOOP anonymous principal (memberOf=="" matches ".*"), so the
// queryHistory endpoint returns 200.
func TestE2E_NOOP_AdminGrantedByRegex(t *testing.T) {
	h := harness.New(t,
		harness.WithAdminRoleRegex(".*"),
		harness.WithUserRoleRegex(".*"),
	)

	resp, err := h.AdminClient("").Get(h.AdminURL + "/trino-gateway/api/queryHistory")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"admin endpoint must return 200 when admin regex matches anonymous principal")
}

// TestE2E_NOOP_AdminDeniedWithoutRegex confirms that with regexes that do NOT
// match anonymous (memberOf==""), the queryHistory endpoint returns 403.
// The endpoint requires USER role (not ADMIN), so the USER regex is what
// determines access for /queryHistory.
func TestE2E_NOOP_AdminDeniedWithoutRegex(t *testing.T) {
	h := harness.New(t,
		harness.WithAdminRoleRegex("NOMATCH"),
		harness.WithUserRoleRegex("NOMATCH"),
	)

	resp, err := h.AdminClient("").Get(h.AdminURL + "/trino-gateway/api/queryHistory")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.Equalf(t, http.StatusForbidden, resp.StatusCode,
		"queryHistory must return 403 when neither regex matches; body=%s", string(body))
}

// TestE2E_Role_403OnInsufficientRole verifies that an endpoint requiring ADMIN
// returns 403 with the expected JSON body when the principal lacks ADMIN, but
// an endpoint requiring USER still returns 200 for the same principal.
func TestE2E_Role_403OnInsufficientRole(t *testing.T) {
	h := harness.New(t,
		harness.WithUserRoleRegex(".*"),
		harness.WithAdminRoleRegex("NOMATCH"),
	)

	// /webapp/saveBackend requires ADMIN → expect 403.
	saveBody := `{"name":"x","proxyTo":"http://x:1","active":true,"routingGroup":"default"}`
	resp, err := h.AdminClient("").Post(
		h.AdminURL+"/webapp/saveBackend",
		"application/json",
		strings.NewReader(saveBody),
	)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "saveBackend without ADMIN must 403")
	assert.JSONEq(t, `{"error":"forbidden"}`, string(body),
		"403 body must be the shared forbidden JSON")

	// /webapp/getAllBackends requires USER → expect 200.
	resp2, err := h.AdminClient("").Post(
		h.AdminURL+"/webapp/getAllBackends",
		"application/json",
		strings.NewReader("{}"),
	)
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusOK, resp2.StatusCode, "getAllBackends with USER must 200")
}

// TestE2E_Userinfo_ReturnsRoles verifies POST /userinfo returns the {code,msg,data}
// envelope with userId/userName/roles fields populated for the NOOP principal.
func TestE2E_Userinfo_ReturnsRoles(t *testing.T) {
	h := harness.New(t,
		harness.WithAdminRoleRegex(".*"),
		harness.WithUserRoleRegex(".*"),
	)

	resp, err := h.AdminClient("").Post(
		h.AdminURL+"/userinfo",
		"application/json",
		strings.NewReader("{}"),
	)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			UserID      string   `json:"userId"`
			UserName    string   `json:"userName"`
			Roles       []string `json:"roles"`
			Permissions []string `json:"permissions"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&env))

	assert.Equal(t, 200, env.Code, "userinfo code field")
	assert.NotEmpty(t, env.Data.UserName, "userinfo data.userName must be set")
	assert.Equal(t, env.Data.UserName, env.Data.UserID, "userId mirrors userName for NOOP")
	assert.Contains(t, env.Data.Roles, "ADMIN", "anonymous granted ADMIN by .* regex")
	assert.Contains(t, env.Data.Roles, "USER", "anonymous granted USER by .* regex")
}

// TestE2E_LoginType_ReportsNOOP verifies POST /loginType returns the wire
// envelope identifying NOOP authentication. admin/authhandlers.go maps:
//   NOOP/"" → "none"
//   OIDC    → "oauth"
//   LDAP    → "form"
func TestE2E_LoginType_ReportsNOOP(t *testing.T) {
	h := harness.New(t)

	resp, err := h.AdminClient("").Post(
		h.AdminURL+"/loginType",
		"application/json",
		strings.NewReader("{}"),
	)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data string `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&env))

	assert.Equal(t, 200, env.Code)
	assert.Equalf(t, "none", env.Data,
		"NOOP auth should report loginType=\"none\", got %q", env.Data)
}
