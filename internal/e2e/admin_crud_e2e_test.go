//go:build e2e

package e2e_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/e2e/harness"
)

// TestE2E_Admin_BackendListEmpty asserts that a fresh gateway with no
// backends returns an empty list (or null) from /gateway/backend/all.
func TestE2E_Admin_BackendListEmpty(t *testing.T) {
	h := harness.New(t)
	client := h.AdminClient("")

	resp, err := client.Get(h.AdminURL + "/gateway/backend/all")
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	trimmed := strings.TrimSpace(string(body))
	// Accept either "[]" or "null" — both signal "no backends".
	assert.Truef(t, trimmed == "[]" || trimmed == "null",
		"expected empty array or null, got %q", trimmed)
}

// TestE2E_Admin_BackendCRUDLifecycle drives a backend through add → list →
// activate → deactivate → delete via the /gateway/* endpoints and verifies
// the visible state at each step.
func TestE2E_Admin_BackendCRUDLifecycle(t *testing.T) {
	h := harness.New(t)
	client := h.AdminClient("")

	const name = "test-be"

	addBody := map[string]any{
		"name":         name,
		"proxyTo":      "http://fake:9999",
		"active":       true,
		"routingGroup": "default",
	}
	postJSON(t, client, h.AdminURL+"/gateway/backend/modify/add", addBody, http.StatusOK)

	assert.True(t, backendInList(t, client, h.AdminURL+"/gateway/backend/all", name),
		"backend %q expected in /gateway/backend/all after add", name)

	postRaw(t, client, h.AdminURL+"/gateway/backend/activate/"+name, "", http.StatusOK)
	assert.True(t, backendInList(t, client, h.AdminURL+"/gateway/backend/active", name),
		"backend %q expected in /gateway/backend/active after activate", name)

	postRaw(t, client, h.AdminURL+"/gateway/backend/deactivate/"+name, "", http.StatusOK)
	assert.False(t, backendInList(t, client, h.AdminURL+"/gateway/backend/active", name),
		"backend %q expected absent from /gateway/backend/active after deactivate", name)

	postRaw(t, client, h.AdminURL+"/gateway/backend/modify/delete", name, http.StatusOK)
	assert.False(t, backendInList(t, client, h.AdminURL+"/gateway/backend/all", name),
		"backend %q expected absent from /gateway/backend/all after delete", name)
}

// TestE2E_Admin_BackendWireShape asserts that a backend JSON returned from
// /gateway/backend/all contains the required wire fields.
func TestE2E_Admin_BackendWireShape(t *testing.T) {
	h := harness.New(t)
	client := h.AdminClient("")

	addBody := map[string]any{
		"name":         "shape-be",
		"proxyTo":      "http://fake:8888",
		"active":       true,
		"routingGroup": "etl",
	}
	postJSON(t, client, h.AdminURL+"/gateway/backend/modify/add", addBody, http.StatusOK)

	resp, err := client.Get(h.AdminURL + "/gateway/backend/all")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var items []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&items))
	require.NotEmpty(t, items, "expected at least one backend in list")

	var found map[string]any
	for _, it := range items {
		if it["name"] == "shape-be" {
			found = it
			break
		}
	}
	require.NotNil(t, found, "added backend not present in list")

	assert.Equal(t, "shape-be", found["name"])
	assert.Equal(t, "http://fake:8888", found["proxyTo"])
	assert.Equal(t, true, found["active"])
	assert.Equal(t, "etl", found["routingGroup"])

	// audit M6: externalUrl is always present (no omitempty) and falls back to
	// proxyTo when the client did not supply one.
	_, hasExternalURL := found["externalUrl"]
	assert.True(t, hasExternalURL, "externalUrl must be present on the wire")
	assert.Equal(t, "http://fake:8888", found["externalUrl"])
}

// TestE2E_Admin_EntityAPI_AddAndList verifies that a backend POSTed to
// /entity?entityType=GATEWAY_BACKEND is returned by GET /entity/GATEWAY_BACKEND.
func TestE2E_Admin_EntityAPI_AddAndList(t *testing.T) {
	h := harness.New(t)
	client := h.AdminClient("")

	body := map[string]any{
		"name":         "entity-be",
		"proxyTo":      "http://fake:7777",
		"active":       true,
		"routingGroup": "default",
	}
	postJSON(t, client, h.AdminURL+"/entity?entityType=GATEWAY_BACKEND", body, http.StatusOK)

	resp, err := client.Get(h.AdminURL + "/entity/GATEWAY_BACKEND")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var items []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&items))

	names := make([]string, 0, len(items))
	for _, it := range items {
		if n, ok := it["name"].(string); ok {
			names = append(names, n)
		}
	}
	assert.Contains(t, names, "entity-be")
}

// TestE2E_Admin_EntityAPI_ListTypes verifies GET /entity advertises GATEWAY_BACKEND.
func TestE2E_Admin_EntityAPI_ListTypes(t *testing.T) {
	h := harness.New(t)
	client := h.AdminClient("")

	resp, err := client.Get(h.AdminURL + "/entity")
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)

	assert.Contains(t, string(body), "GATEWAY_BACKEND")
}

// TestE2E_Admin_EntityAPI_UnknownType asserts that POSTing to /entity with an
// unknown entityType returns 500 (per Java behavior, qa-tech-lead §4.2c).
func TestE2E_Admin_EntityAPI_UnknownType(t *testing.T) {
	h := harness.New(t)
	client := h.AdminClient("")

	resp, err := client.Post(
		h.AdminURL+"/entity?entityType=TOTALLY_UNKNOWN",
		"application/json",
		strings.NewReader(`{}`),
	)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

// TestE2E_Admin_EntityAPI_UnknownTypeGet asserts that GET /entity/<unknown>
// returns 200 with an empty array (USE_STORIES §4.2c — Go-native behavior).
func TestE2E_Admin_EntityAPI_UnknownTypeGet(t *testing.T) {
	h := harness.New(t)
	client := h.AdminClient("")

	resp, err := client.Get(h.AdminURL + "/entity/TOTALLY_UNKNOWN")
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "[]", strings.TrimSpace(string(body)))
}

// TestE2E_Admin_EntityAPI_SeedsMonitorStatus asserts that immediately after
// POST /entity creates an active backend, /webapp/getAllBackends reports the
// backend with a non-empty status (PENDING or HEALTHY), proving the entity
// upsert seeds the in-memory monitor state.
func TestE2E_Admin_EntityAPI_SeedsMonitorStatus(t *testing.T) {
	h := harness.New(t)
	client := h.AdminClient("")

	body := map[string]any{
		"name":         "seed-be",
		"proxyTo":      "http://fake:6666",
		"active":       true,
		"routingGroup": "default",
	}
	postJSON(t, client, h.AdminURL+"/entity?entityType=GATEWAY_BACKEND", body, http.StatusOK)

	resp, err := client.Post(
		h.AdminURL+"/webapp/getAllBackends",
		"application/json",
		strings.NewReader(""),
	)
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
		if b["name"] == "seed-be" {
			found = b
			break
		}
	}
	require.NotNil(t, found, "seeded backend not visible in /webapp/getAllBackends immediately after entity add")

	status, _ := found["status"].(string)
	assert.Containsf(t, []string{"PENDING", "HEALTHY"}, status,
		"expected status PENDING or HEALTHY for newly-seeded backend, got %q", status)
}

// TestE2E_Admin_PublicBackends_NoAuth asserts /api/public/backends serves
// without any Authorization header (uses plain http.Get, not AdminClient).
func TestE2E_Admin_PublicBackends_NoAuth(t *testing.T) {
	h := harness.New(t)

	addBody := map[string]any{
		"name":         "public-be",
		"proxyTo":      "http://fake:5555",
		"active":       true,
		"routingGroup": "default",
	}
	postJSON(t, h.AdminClient(""), h.AdminURL+"/gateway/backend/modify/add", addBody, http.StatusOK)

	resp, err := http.Get(h.AdminURL + "/api/public/backends")
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// ---- helpers ----

// postJSON marshals body to JSON, POSTs it, and asserts the status code.
func postJSON(t *testing.T, c *http.Client, url string, body any, wantStatus int) {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	resp, err := c.Post(url, "application/json", bytes.NewReader(raw))
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	require.NoError(t, resp.Body.Close())
	require.Equalf(t, wantStatus, resp.StatusCode, "POST %s", url)
}

// postRaw POSTs a plain text body and asserts the status code.
func postRaw(t *testing.T, c *http.Client, url, body string, wantStatus int) {
	t.Helper()
	resp, err := c.Post(url, "text/plain", strings.NewReader(body))
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	require.NoError(t, resp.Body.Close())
	require.Equalf(t, wantStatus, resp.StatusCode, "POST %s", url)
}

// backendInList reports whether the JSON array at url contains a backend with the given name.
func backendInList(t *testing.T, c *http.Client, url, name string) bool {
	t.Helper()
	resp, err := c.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "GET %s", url)

	var items []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		// null body decodes as zero-length slice; treat as "not present".
		return false
	}
	for _, it := range items {
		if it["name"] == name {
			return true
		}
	}
	return false
}
