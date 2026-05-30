//go:build e2e

package e2e_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/e2e/harness"
)

// postStatement issues a POST /v1/statement through the gateway with the given
// SQL body and optional extra headers. Returns the response (caller closes the
// body) and the body bytes already read.
func postStatement(t *testing.T, h *harness.Harness, body string, hdr http.Header) (*http.Response, []byte) {
	t.Helper()
	req := newPostRequest(t, h, body)
	for k, vv := range hdr {
		req.Header.Del(k)
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	resp, err := h.ProxyClient().Do(req)
	require.NoError(t, err)
	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	resp.Body = io.NopCloser(bytes.NewReader(respBody))
	return resp, respBody
}

// newPostRequest builds a POST /v1/statement request without sending it.
// The default X-Trino-User is "e2e"; callers may overwrite it.
func newPostRequest(t *testing.T, h *harness.Harness, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.ProxyURL+"/v1/statement", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("X-Trino-User", "e2e")
	return req
}

// doGet issues a GET against the given URL through the proxy client with
// optional extra headers. Caller is responsible for closing the response body.
func doGet(t *testing.T, h *harness.Harness, url string, hdr http.Header) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	req.Header.Set("X-Trino-User", "e2e")
	for k, vv := range hdr {
		req.Header.Del(k)
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	resp, err := h.ProxyClient().Do(req)
	require.NoError(t, err)
	return resp
}

// registerBackend POSTs a backend definition directly via /entity and polls
// until the backend is visible in /gateway/backend/all. Used when a test needs
// to register a non-TrinoFake backend URL (e.g., httptest server, dead port).
func registerBackend(t *testing.T, h *harness.Harness, name, group, proxyTo string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"name":         name,
		"proxyTo":      proxyTo,
		"active":       true,
		"routingGroup": group,
	})
	require.NoError(t, err)

	resp, err := h.AdminClient("").Post(
		h.AdminURL+"/entity?entityType=GATEWAY_BACKEND",
		"application/json",
		bytes.NewReader(payload),
	)
	require.NoError(t, err, "POST /entity")
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "POST /entity status")

	deadline := time.Now().Add(15 * time.Second)
	client := h.AdminClient("")
	for time.Now().Before(deadline) {
		if backendVisibleInList(t, client, h.AdminURL, name) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("backend %q not visible via /gateway/backend/all within 15s", name)
}

// backendVisibleInList reports whether the named backend appears in
// /gateway/backend/all.
func backendVisibleInList(t *testing.T, c *http.Client, adminURL, name string) bool {
	t.Helper()
	resp, err := c.Get(adminURL + "/gateway/backend/all")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var entries []struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return false
	}
	for _, e := range entries {
		if e.Name == name {
			return true
		}
	}
	return false
}

// contains reports whether s contains v.
func contains(s []string, v string) bool {
	for _, e := range s {
		if e == v {
			return true
		}
	}
	return false
}
