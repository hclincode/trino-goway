package testutil

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// uiLogin performs a form login against the fake and returns the resulting cookie.
func uiLogin(t testing.TB, f *TrinoFake, user, pass string) *http.Cookie {
	t.Helper()

	form := url.Values{"username": {user}, "password": {pass}}
	resp, err := http.PostForm(f.URL+"/ui/login", form)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode, "login should succeed for valid creds")

	for _, c := range resp.Cookies() {
		if c.Name == uiCookieName {
			return c
		}
	}
	t.Fatalf("login response did not set the %s cookie", uiCookieName)
	return nil
}

func TestTrinoFake_UILogin_SetsCookie(t *testing.T) {
	t.Parallel()

	f := NewTrinoFake(t)
	f.SetUICredentials("admin", "secret")

	c := uiLogin(t, f, "admin", "secret")
	assert.NotEmpty(t, c.Value)
	assert.True(t, c.HttpOnly, "session cookie should be HttpOnly")
	assert.Equal(t, "/", c.Path)
	assert.Equal(t, 1, f.LoginCount())
}

func TestTrinoFake_UILogin_WrongCredsForbidden(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		registerCreds bool
		user, pass    string
	}{
		{name: "wrong password", registerCreds: true, user: "admin", pass: "nope"},
		{name: "wrong user", registerCreds: true, user: "mallory", pass: "secret"},
		{name: "empty form", registerCreds: true, user: "", pass: ""},
		{name: "no creds configured", registerCreds: false, user: "admin", pass: "secret"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := NewTrinoFake(t)
			if tc.registerCreds {
				f.SetUICredentials("admin", "secret")
			}

			form := url.Values{"username": {tc.user}, "password": {tc.pass}}
			resp, err := http.PostForm(f.URL+"/ui/login", form)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusForbidden, resp.StatusCode)
			assert.Empty(t, resp.Cookies(), "rejected login must not set a session cookie")
			assert.Equal(t, 1, f.LoginCount())
		})
	}
}

func TestTrinoFake_UIStats_RequiresCookie(t *testing.T) {
	t.Parallel()

	f := NewTrinoFake(t)
	f.SetUICredentials("admin", "secret")
	f.SetUIStats(3, 5, 2)

	// Without a cookie → 401.
	resp, err := http.Get(f.URL + "/ui/api/stats")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, 0, f.UIStatsHits(), "unauthorized request should not count as a hit")

	// With a cookie → 200 + the configured counts.
	c := uiLogin(t, f, "admin", "secret")
	req, err := http.NewRequest(http.MethodGet, f.URL+"/ui/api/stats", nil)
	require.NoError(t, err)
	req.AddCookie(c)
	req.Header.Set("X-Forwarded-Proto", "https")

	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var stats struct {
		ActiveWorkers  int `json:"activeWorkers"`
		RunningQueries int `json:"runningQueries"`
		QueuedQueries  int `json:"queuedQueries"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&stats))
	assert.Equal(t, 3, stats.ActiveWorkers)
	assert.Equal(t, 5, stats.RunningQueries)
	assert.Equal(t, 2, stats.QueuedQueries)
	assert.Equal(t, 1, f.UIStatsHits())
	assert.Equal(t, "https", f.LastForwardedProto())
}

func TestTrinoFake_UIQuery_PerUserQueued(t *testing.T) {
	t.Parallel()

	f := NewTrinoFake(t)
	f.SetUICredentials("admin", "secret")
	f.SetQueuedQueries(map[string]int{"alice": 2, "bob": 1})

	// Without a cookie → 401.
	resp, err := http.Get(f.URL + "/ui/api/query?state=QUEUED")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	c := uiLogin(t, f, "admin", "secret")
	req, err := http.NewRequest(http.MethodGet, f.URL+"/ui/api/query?state=QUEUED", nil)
	require.NoError(t, err)
	req.AddCookie(c)

	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var queries []struct {
		QueryID     string `json:"queryId"`
		SessionUser string `json:"sessionUser"`
		State       string `json:"state"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&queries))

	tally := map[string]int{}
	for _, q := range queries {
		assert.Equal(t, "QUEUED", q.State)
		assert.NotEmpty(t, q.QueryID)
		tally[q.SessionUser]++
	}
	assert.Equal(t, map[string]int{"alice": 2, "bob": 1}, tally)
	assert.Equal(t, 1, f.UIQueryHits())
}

func TestTrinoFake_Metrics_AuthHeaderRecorded(t *testing.T) {
	t.Parallel()

	const body = "trino_running 5\ntrino_queued 2\n"

	t.Run("basic auth", func(t *testing.T) {
		t.Parallel()

		f := NewTrinoFake(t)
		f.SetMetrics(body)

		req, err := http.NewRequest(http.MethodGet, f.URL+"/metrics", nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("svc:pw")))

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		basicUser, trinoUser := f.LastMetricsAuth()
		assert.Equal(t, "svc", basicUser)
		assert.Empty(t, trinoUser)
		assert.Equal(t, 1, f.MetricsHits())
	})

	t.Run("x-trino-user", func(t *testing.T) {
		t.Parallel()

		f := NewTrinoFake(t)
		f.SetMetrics(body)

		req, err := http.NewRequest(http.MethodGet, f.URL+"/metrics", nil)
		require.NoError(t, err)
		req.Header.Set("X-Trino-User", "svc")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		basicUser, trinoUser := f.LastMetricsAuth()
		assert.Empty(t, basicUser)
		assert.Equal(t, "svc", trinoUser)
	})

	t.Run("no auth rejected", func(t *testing.T) {
		t.Parallel()

		f := NewTrinoFake(t)
		f.SetMetrics(body)

		resp, err := http.Get(f.URL + "/metrics")
		require.NoError(t, err)
		resp.Body.Close()

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		assert.Equal(t, 0, f.MetricsHits())
	})
}

func TestTrinoFake_Metrics_BodyVerbatim(t *testing.T) {
	t.Parallel()

	const body = "# HELP trino_running running queries\n" +
		"trino_execution_name_QueryManager_RunningQueries 3.9\n" +
		"trino_execution_name_QueryManager_QueuedQueries 1\n"

	f := NewTrinoFake(t)
	f.SetMetrics(body)

	req, err := http.NewRequest(http.MethodGet, f.URL+"/metrics", nil)
	require.NoError(t, err)
	req.Header.Set("X-Trino-User", "svc")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/openmetrics-text")

	got := readAll(t, resp.Body)
	assert.Equal(t, body, got, "metrics body should be returned byte-for-byte")
}

func TestTrinoFake_Metrics_ConfigurablePath(t *testing.T) {
	t.Parallel()

	f := NewTrinoFake(t)
	f.SetMetricsPath("/prometheus")
	f.SetMetrics("trino_running 0\n")

	// Default /metrics path is now a 404.
	resp, err := http.Get(f.URL + "/metrics")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	req, err := http.NewRequest(http.MethodGet, f.URL+"/prometheus", nil)
	require.NoError(t, err)
	req.Header.Set("X-Trino-User", "svc")

	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 1, f.MetricsHits())
}

// TestTrinoFake_DefaultsAreInert guards INFO_API parity: a fake constructed without any
// cluster-stats configuration rejects UI logins, serves no UI surface, and exposes an
// empty metrics body — so existing callers see exactly the pre-Phase-12 behavior.
func TestTrinoFake_DefaultsAreInert(t *testing.T) {
	t.Parallel()

	f := NewTrinoFake(t)

	// Login is rejected (no creds configured).
	form := url.Values{"username": {"admin"}, "password": {"secret"}}
	resp, err := http.PostForm(f.URL+"/ui/login", form)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	// Stats/query without a cookie → 401.
	for _, path := range []string{"/ui/api/stats", "/ui/api/query?state=QUEUED"} {
		resp, err := http.Get(f.URL + path)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, path)
	}

	// Metrics with auth → 200 but an empty body (nothing configured).
	req, err := http.NewRequest(http.MethodGet, f.URL+"/metrics", nil)
	require.NoError(t, err)
	req.Header.Set("X-Trino-User", "svc")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, readAll(t, resp.Body))

	assert.Zero(t, f.UIStatsHits())
	assert.Zero(t, f.UIQueryHits())
	assert.Zero(t, f.LastForwardedProto())
}

func readAll(t testing.TB, r interface{ Read([]byte) (int, error) }) string {
	t.Helper()
	var sb strings.Builder
	buf := make([]byte, 512)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return sb.String()
}
