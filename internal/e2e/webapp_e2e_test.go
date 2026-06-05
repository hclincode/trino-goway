//go:build e2e

package e2e_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/e2e/harness"
)

// TestE2E_Webapp_ResponseEnvelope asserts that webapp endpoints return the
// {code:200, msg:"Successful.", data:...} envelope on success.
func TestE2E_Webapp_ResponseEnvelope(t *testing.T) {
	h := harness.New(t)
	client := h.AdminClient("")

	resp, err := client.Post(h.AdminURL+"/webapp/getAllBackends", "application/json", strings.NewReader(""))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var env struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&env))

	assert.Equal(t, 200, env.Code)
	assert.Equal(t, "Successful.", env.Msg)
}

// TestE2E_Webapp_GetAllBackends asserts the response data array contains the
// added backend with a non-empty status field.
func TestE2E_Webapp_GetAllBackends(t *testing.T) {
	h := harness.New(t)
	client := h.AdminClient("")

	_ = h.AddBackend(t, "trino-1", "default")

	resp, err := client.Post(h.AdminURL+"/webapp/getAllBackends", "application/json", strings.NewReader(""))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var env struct {
		Code int                      `json:"code"`
		Msg  string                   `json:"msg"`
		Data []map[string]interface{} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&env))
	require.Equal(t, 200, env.Code)

	var found map[string]interface{}
	for _, b := range env.Data {
		if b["name"] == "trino-1" {
			found = b
			break
		}
	}
	require.NotNil(t, found, "backend trino-1 not present in /webapp/getAllBackends response")

	status, _ := found["status"].(string)
	assert.NotEmpty(t, status, "expected non-empty status field on backend")
}

// TestE2E_Webapp_GetDistribution asserts the DistributionResponse contains the
// fields required by USE_STORIES §4.3 (per internal/admin/webapp.go).
func TestE2E_Webapp_GetDistribution(t *testing.T) {
	h := harness.New(t)
	client := h.AdminClient("")

	resp, err := client.Post(h.AdminURL+"/webapp/getDistribution", "application/json", strings.NewReader(""))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var env struct {
		Code int                    `json:"code"`
		Msg  string                 `json:"msg"`
		Data map[string]interface{} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&env))
	require.Equal(t, 200, env.Code)

	// Fields per DistributionResponse in internal/admin/webapp.go.
	required := []string{
		"totalBackendCount",
		"onlineBackendCount",
		"offlineBackendCount",
		"healthyBackendCount",
		"unhealthyBackendCount",
		"totalQueryCount",
		"averageQueryCountMinute",
		"averageQueryCountSecond",
		"startTime",
		"distributionChart",
		"lineChart",
	}
	for _, k := range required {
		_, ok := env.Data[k]
		assert.Truef(t, ok, "missing field %q in DistributionResponse", k)
	}

	st, _ := env.Data["startTime"].(string)
	assert.NotEmpty(t, st, "startTime should be non-empty ISO-8601 string")
}

// TestE2E_Webapp_GetUIConfiguration asserts the response has the authType field.
func TestE2E_Webapp_GetUIConfiguration(t *testing.T) {
	h := harness.New(t)
	client := h.AdminClient("")

	resp, err := client.Post(h.AdminURL+"/webapp/getUIConfiguration", "application/json", strings.NewReader(""))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var env struct {
		Code int                    `json:"code"`
		Msg  string                 `json:"msg"`
		Data map[string]interface{} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&env))
	require.Equal(t, 200, env.Code)

	authType, _ := env.Data["authType"].(string)
	assert.NotEmpty(t, authType, "expected non-empty authType in UI configuration")
}

// TestE2E_Webapp_FindQueryHistory drives a query through the proxy and asserts
// the webapp returns a TableData{total, rows} envelope.
func TestE2E_Webapp_FindQueryHistory(t *testing.T) {
	h := harness.New(t)
	client := h.AdminClient("")

	fake := h.AddBackend(t, "trino-history", "default")
	require.NotNil(t, fake)

	// Drive a statement through the proxy so history is recorded.
	resp0, _ := postStatement(t, h, "SELECT 1", http.Header{"X-Trino-User": []string{"alice"}})
	_ = resp0.Body.Close()

	// Allow a brief moment for the history insert to land.
	time.Sleep(500 * time.Millisecond)

	reqBody := map[string]any{
		"userName":   "",
		"backendUrl": "",
		"queryId":    "",
		"source":     "",
		"page":       1,
		"pageSize":   10,
	}
	body, err := json.Marshal(reqBody)
	require.NoError(t, err)

	resp, err := client.Post(h.AdminURL+"/webapp/findQueryHistory", "application/json", strings.NewReader(string(body)))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var env struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&env))
	require.Equal(t, 200, env.Code)

	var td map[string]interface{}
	require.NoError(t, json.Unmarshal(env.Data, &td))

	_, hasTotal := td["total"]
	_, hasRows := td["rows"]
	assert.True(t, hasTotal, "expected TableData.total field")
	assert.True(t, hasRows, "expected TableData.rows field")
}

// TestE2E_Webapp_RoutingRules verifies external-routing semantics:
// getRoutingRules answers 204 (rules managed externally); updateRoutingRules
// accepts {}.
func TestE2E_Webapp_RoutingRules(t *testing.T) {
	h := harness.New(t)
	client := h.AdminClient("")

	resp, err := client.Post(h.AdminURL+"/webapp/getRoutingRules", "application/json", strings.NewReader(""))
	require.NoError(t, err)
	defer resp.Body.Close()
	// The Go gateway is external-routing-only: 204 signals "external routing in
	// use", which the UI reads via postMaybeNoContent.
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	resp2, err := client.Post(h.AdminURL+"/webapp/updateRoutingRules", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp2.Body)
	require.NoError(t, resp2.Body.Close())
	assert.Equal(t, http.StatusOK, resp2.StatusCode)
}

// TestE2E_Webapp_RoleEnforcement asserts that a principal with no matching
// role regex receives 403 on USER- and ADMIN-protected webapp endpoints.
func TestE2E_Webapp_RoleEnforcement(t *testing.T) {
	h := harness.New(t,
		harness.WithAdminRoleRegex("NEVER_MATCH"),
		harness.WithUserRoleRegex("NEVER_MATCH"),
	)
	client := h.AdminClient("")

	// USER role required.
	resp, err := client.Post(h.AdminURL+"/webapp/getAllBackends", "application/json", strings.NewReader(""))
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "/webapp/getAllBackends should require USER role")

	// ADMIN role required.
	resp2, err := client.Post(h.AdminURL+"/webapp/saveBackend", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp2.Body)
	require.NoError(t, resp2.Body.Close())
	assert.Equal(t, http.StatusForbidden, resp2.StatusCode, "/webapp/saveBackend should require ADMIN role")
}
