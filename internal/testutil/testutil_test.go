package testutil_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/testutil"
)

func TestFreePort(t *testing.T) {
	t.Parallel()

	t.Run("returns valid port above 1024", func(t *testing.T) {
		t.Parallel()
		port := testutil.FreePort(t)
		assert.Greater(t, port, 1024, "FreePort should return a port above 1024")
		assert.LessOrEqual(t, port, 65535, "FreePort should return a port at or below 65535")
	})

	t.Run("two calls return different ports", func(t *testing.T) {
		t.Parallel()
		port1 := testutil.FreePort(t)
		port2 := testutil.FreePort(t)
		assert.NotEqual(t, port1, port2, "two FreePort calls should return different ports")
	})
}

func TestFakeBackend_HealthCheck(t *testing.T) {
	t.Parallel()

	backend := testutil.NewFakeBackend(t)

	resp, err := http.Get(backend.URL + "/v1/info")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var body struct {
		Starting bool `json:"starting"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.False(t, body.Starting, `"starting" field should be false`)

	reqs := backend.Requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, http.MethodGet, reqs[0].Method)
	assert.Equal(t, "/v1/info", reqs[0].URL.Path)
}

func TestFakeBackend_Statement(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		opts    []testutil.FakeBackendOption
		wantID  string
		wantURI bool
	}{
		{
			name:    "default queryID",
			opts:    nil,
			wantID:  "q_test_01",
			wantURI: true,
		},
		{
			name:    "custom queryID via option",
			opts:    []testutil.FakeBackendOption{testutil.WithQueryIDInResponse("my_custom_query_id")},
			wantID:  "my_custom_query_id",
			wantURI: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			backend := testutil.NewFakeBackend(t, tc.opts...)

			resp, err := http.Post(backend.URL+"/v1/statement", "application/json", strings.NewReader(`{"query":"SELECT 1"}`))
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

			var body struct {
				ID      string `json:"id"`
				NextURI string `json:"nextUri"`
			}
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

			assert.Equal(t, tc.wantID, body.ID)
			if tc.wantURI {
				assert.Contains(t, body.NextURI, tc.wantID, "nextUri should contain the queryID")
				assert.Contains(t, body.NextURI, "/v1/statement/", "nextUri should contain the statement path")
			}

			reqs := backend.Requests()
			require.Len(t, reqs, 1)
			assert.Equal(t, http.MethodPost, reqs[0].Method)
			assert.Equal(t, "/v1/statement", reqs[0].URL.Path)
		})
	}
}

func TestFakeBackend_WithStatusCode(t *testing.T) {
	t.Parallel()

	backend := testutil.NewFakeBackend(t, testutil.WithStatusCode(http.StatusServiceUnavailable))

	resp, err := http.Get(backend.URL + "/v1/info")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

func TestFakeBackend_WithBody(t *testing.T) {
	t.Parallel()

	const customBody = `{"error":"custom error"}`
	backend := testutil.NewFakeBackend(t, testutil.WithBody(customBody))

	resp, err := http.Get(backend.URL + "/v1/info")
	require.NoError(t, err)
	defer resp.Body.Close()

	var raw json.RawMessage
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&raw))
	assert.JSONEq(t, customBody, string(raw))
}

func TestFakeBackend_WithRedirectTo(t *testing.T) {
	t.Parallel()

	// Use an httptest server as the redirect target to avoid network dependencies.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)

	backend := testutil.NewFakeBackend(t, testutil.WithRedirectTo(target.URL+"/redirected"))

	// Use a client that does NOT follow redirects so we can inspect the 302.
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Get(backend.URL + "/v1/info")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, target.URL+"/redirected", resp.Header.Get("Location"))
}

func TestFakeBackend_RecordsMultipleRequests(t *testing.T) {
	t.Parallel()

	backend := testutil.NewFakeBackend(t)

	for i := 0; i < 3; i++ {
		resp, err := http.Get(backend.URL + "/v1/info")
		require.NoError(t, err)
		resp.Body.Close()
	}

	reqs := backend.Requests()
	assert.Len(t, reqs, 3, "backend should record all received requests")
}
