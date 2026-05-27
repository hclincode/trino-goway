package proxy

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/config"
)

// stringPtr returns a pointer to s, for the Backend/Payload null-vs-set distinction.
func stringPtr(s string) *string { return &s }

// buildCookieProxy constructs a Proxy with cookie-relevant configuration set.
// upstream is the http server the proxy forwards to.
func buildCookieProxy(t *testing.T, upstream *httptest.Server, secret string, ttl time.Duration, wireCompat bool) *Proxy {
	t.Helper()
	router := &fakeRouter{backendURL: upstream.URL}
	return New(Config{
		Proxy: config.ProxyConfig{
			ResponseSize: config.DataSize{Bytes: 1_048_576},
		},
		Cookie: config.CookieConfig{
			Secret:     secret,
			TTL:        config.Duration{D: ttl},
			WireCompat: wireCompat,
		},
		Client: upstream.Client(),
		Router: router,
		Log:    discardLogger(),
	})
}

// TestCookie_HMACFixture pins the wire format: a fixed UnsignedGatewayCookie
// must produce a known 64-char lowercase hex signature. Computed once with
// secret="test-secret"; any change to JSON serialization (field order, null
// handling) will break this and surface wire-incompat with Java.
func TestCookie_HMACFixture(t *testing.T) {
	t.Parallel()

	const expectedSig = "123194ddd6bace445a41232e3bc2822489fa57947b034038ffe8a9eed497b986"

	p := New(Config{
		Cookie: config.CookieConfig{Secret: "test-secret", WireCompat: true},
		Log:    discardLogger(),
	})

	unsigned := UnsignedGatewayCookie{
		Backend:      stringPtr("https://trino-cluster-a.example.com:8080"),
		DeletePaths:  []string{"/logout", "/oauth2/logout"},
		Name:         "TG.OAUTH2",
		Payload:      nil,
		Priority:     0,
		RoutingPaths: []string{"/oauth2", "/logout", "/oauth2/logout"},
		Ts:           1716540000000,
		Ttl:          "10.00m",
	}

	sig, err := p.sign(unsigned)
	require.NoError(t, err)
	assert.Equal(t, expectedSig, sig, "HMAC must match pinned wire-format fixture")
	assert.Len(t, sig, 64, "HMAC hex must be exactly 64 chars")
}

// TestCookie_EncodeDecodeRoundTrip verifies that issueCookie → encodeCookie →
// decodeCookie preserves every field, including null payload and airlift TTL.
func TestCookie_EncodeDecodeRoundTrip(t *testing.T) {
	t.Parallel()

	p := New(Config{
		Cookie: config.CookieConfig{
			Secret:     "test-secret",
			TTL:        config.Duration{D: 10 * time.Minute},
			WireCompat: true,
		},
		Log: discardLogger(),
	})

	w := httptest.NewRecorder()
	require.NoError(t, p.issueCookie(w, "https://backend-a.example:8080"))

	setCookie := w.Header().Get("Set-Cookie")
	require.NotEmpty(t, setCookie)

	cookieValue := extractCookieValue(t, setCookie, cookieName)
	raw, err := p.decodeCookie(cookieValue)
	require.NoError(t, err)

	var decoded GatewayCookie
	require.NoError(t, json.Unmarshal(raw, &decoded))

	require.NotNil(t, decoded.Backend)
	assert.Equal(t, "https://backend-a.example:8080", *decoded.Backend)
	assert.Equal(t, "TG.OAUTH2", decoded.Name)
	assert.Equal(t, 0, decoded.Priority)
	assert.Nil(t, decoded.Payload, "payload must round-trip as nil (serialized as null)")
	assert.Equal(t, []string{"/logout", "/oauth2/logout"}, decoded.DeletePaths)
	assert.Equal(t, []string{"/oauth2", "/logout", "/oauth2/logout"}, decoded.RoutingPaths)
	assert.Equal(t, "10.00m", decoded.Ttl, "TTL must be airlift Duration string, not nanoseconds")
	assert.Len(t, decoded.Signature, 64)

	assert.Contains(t, string(raw), `"payload":null`,
		"payload must appear as explicit null in JSON, not be omitted")
}

// TestCookie_HMACTamper verifies that flipping one byte of the cookie value
// causes the proxy to return HTTP 500 (per gateway-cookies-and-sticky-routing study,
// HMAC failure throws and propagates as 500).
func TestCookie_HMACTamper(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	p := buildCookieProxy(t, upstream, "test-secret", 10*time.Minute, true)

	rec := httptest.NewRecorder()
	require.NoError(t, p.issueCookie(rec, upstream.URL))
	good := extractCookieValue(t, rec.Header().Get("Set-Cookie"), cookieName)

	tampered := tamperOneByte(t, good)
	require.NotEqual(t, good, tampered)

	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: tampered})
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code,
		"tampered cookie must produce HTTP 500, not 200 or silent re-route")
}

// TestCookie_TTLExpiry verifies that a cookie with ts older than ttl is
// rejected and the response carries a delete-cookie header.
func TestCookie_TTLExpiry(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	p := buildCookieProxy(t, upstream, "test-secret", 10*time.Minute, true)

	expired := buildExpiredCookieValue(t, p, upstream.URL, 11*time.Minute)

	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: expired})
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "expired-but-valid-HMAC cookie must not 500")
	assertCookieDeleted(t, w)
}

// TestCookie_IssueTrigger_OAuth2 verifies that a request to /oauth2/* with no
// cookie present causes the proxy to set a fresh TG.OAUTH2 cookie.
func TestCookie_IssueTrigger_OAuth2(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	p := buildCookieProxy(t, upstream, "test-secret", 10*time.Minute, true)

	req := httptest.NewRequest(http.MethodPost, "/oauth2/authorize", nil)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	setCookie := w.Header().Get("Set-Cookie")
	require.NotEmpty(t, setCookie, "first /oauth2 request must Set-Cookie TG.OAUTH2")
	assert.Contains(t, setCookie, cookieName+"=")
	assert.NotContains(t, setCookie, "delete",
		"first /oauth2 request must issue, not delete, the cookie")
}

// TestCookie_NoIssueOnNonOAuth2 verifies that a request to a non-/oauth2 path
// with no cookie does NOT trigger issuance.
func TestCookie_NoIssueOnNonOAuth2(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	p := buildCookieProxy(t, upstream, "test-secret", 10*time.Minute, true)

	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, w.Header().Get("Set-Cookie"),
		"non-/oauth2 paths must not auto-issue the gateway cookie")
}

// TestCookie_DeleteOnLogout verifies that GET /logout with a valid cookie
// produces a delete-cookie header (because /logout is in deletePaths).
func TestCookie_DeleteOnLogout(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	p := buildCookieProxy(t, upstream, "test-secret", 10*time.Minute, true)

	rec := httptest.NewRecorder()
	require.NoError(t, p.issueCookie(rec, upstream.URL))
	good := extractCookieValue(t, rec.Header().Get("Set-Cookie"), cookieName)

	req := httptest.NewRequest(http.MethodGet, "/logout", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: good})
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assertCookieDeleted(t, w)
}

// TestCookie_DeleteOnOAuth2Logout verifies the second deletePaths entry as well.
func TestCookie_DeleteOnOAuth2Logout(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	p := buildCookieProxy(t, upstream, "test-secret", 10*time.Minute, true)

	rec := httptest.NewRecorder()
	require.NoError(t, p.issueCookie(rec, upstream.URL))
	good := extractCookieValue(t, rec.Header().Get("Set-Cookie"), cookieName)

	req := httptest.NewRequest(http.MethodGet, "/oauth2/logout", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: good})
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assertCookieDeleted(t, w)
}

// TestCookie_DisabledWhenSecretEmpty verifies that when Secret is empty,
// the proxy ignores cookies entirely — no issue, no validate, no delete.
func TestCookie_DisabledWhenSecretEmpty(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	p := buildCookieProxy(t, upstream, "", 10*time.Minute, true)

	req := httptest.NewRequest(http.MethodPost, "/oauth2/authorize", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "garbage"})
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "secret-disabled mode must not 500 on bad cookies")
	assert.Empty(t, w.Header().Get("Set-Cookie"))
}

// TestCookie_FormatTTL_WireCompatVsNanos verifies that wireCompat=true produces
// an airlift Duration string and wireCompat=false produces raw nanoseconds.
func TestCookie_FormatTTL_WireCompatVsNanos(t *testing.T) {
	t.Parallel()

	compat := New(Config{Cookie: config.CookieConfig{WireCompat: true}, Log: discardLogger()})
	rawNanos := New(Config{Cookie: config.CookieConfig{WireCompat: false}, Log: discardLogger()})

	assert.Equal(t, "10.00m", compat.formatTTL(10*time.Minute))
	assert.Equal(t, "600000000000", rawNanos.formatTTL(10*time.Minute))
}

// TestCookie_ParseTTL_AllForms verifies parseTTL handles airlift, Go duration,
// and raw nanosecond integer encodings.
func TestCookie_ParseTTL_AllForms(t *testing.T) {
	t.Parallel()

	p := New(Config{Cookie: config.CookieConfig{}, Log: discardLogger()})

	cases := map[string]time.Duration{
		"10.00m":       10 * time.Minute,
		"1.00h":        time.Hour,
		"500.00ms":     500 * time.Millisecond,
		"1.00s":        time.Second,
		"10m":          10 * time.Minute,
		"1h30m":        90 * time.Minute,
		"600000000000": 10 * time.Minute,
	}
	for input, want := range cases {
		input, want := input, want
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, want, p.parseTTL(input))
		})
	}
}

// TestCookie_RawURLEncoding verifies the non-wireCompat branch uses raw URL encoding.
func TestCookie_RawURLEncoding(t *testing.T) {
	t.Parallel()

	p := New(Config{
		Cookie: config.CookieConfig{
			Secret:     "test-secret",
			TTL:        config.Duration{D: 10 * time.Minute},
			WireCompat: false,
		},
		Log: discardLogger(),
	})

	w := httptest.NewRecorder()
	require.NoError(t, p.issueCookie(w, "https://b.example:8080"))
	cookieValue := extractCookieValue(t, w.Header().Get("Set-Cookie"), cookieName)
	assert.NotContains(t, cookieValue, "=", "raw URL encoding must not pad with '='")

	raw, err := p.decodeCookie(cookieValue)
	require.NoError(t, err)
	var gc GatewayCookie
	require.NoError(t, json.Unmarshal(raw, &gc))
	assert.Equal(t, "600000000000", gc.Ttl, "non-wireCompat TTL is raw nanoseconds")
}

// TestCookie_MatchesAny verifies exact-match semantics of the deletePaths matcher.
func TestCookie_MatchesAny(t *testing.T) {
	t.Parallel()

	paths := []string{"/logout", "/oauth2/logout"}
	assert.True(t, matchesAny("/logout", paths))
	assert.True(t, matchesAny("/oauth2/logout", paths))
	assert.False(t, matchesAny("/logout/extra", paths), "exact match only")
	assert.False(t, matchesAny("/login", paths))
	assert.False(t, matchesAny("", nil))
}

// --- helpers ---

// extractCookieValue parses a Set-Cookie header and returns the value of the
// cookie with the given name.
func extractCookieValue(t *testing.T, setCookie, name string) string {
	t.Helper()
	require.NotEmpty(t, setCookie)
	parts := strings.SplitN(setCookie, ";", 2)
	require.NotEmpty(t, parts)
	kv := strings.SplitN(parts[0], "=", 2)
	require.Len(t, kv, 2)
	require.Equal(t, name, kv[0])
	return kv[1]
}

// tamperOneByte returns a copy of value with one base64 byte changed.
// Uses character substitution to keep the result a valid base64 length.
func tamperOneByte(t *testing.T, value string) string {
	t.Helper()
	require.NotEmpty(t, value)
	b := []byte(value)
	// Find the first alphanumeric (data) byte and mutate it.
	for i, c := range b {
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			if c == 'A' {
				b[i] = 'B'
			} else {
				b[i] = 'A'
			}
			return string(b)
		}
	}
	t.Fatalf("tamperOneByte: no mutable byte found in %q", value)
	return ""
}

// buildExpiredCookieValue constructs a properly-signed cookie whose ts is set
// `age` in the past, exceeding the TTL.
func buildExpiredCookieValue(t *testing.T, p *Proxy, backendURL string, age time.Duration) string {
	t.Helper()
	ttl := p.cfg.Cookie.TTL.D
	backend := backendURL
	pastTs := time.Now().Add(-age).UnixMilli()

	unsigned := UnsignedGatewayCookie{
		Backend:      &backend,
		DeletePaths:  []string{"/logout", "/oauth2/logout"},
		Name:         cookieName,
		Payload:      nil,
		Priority:     0,
		RoutingPaths: []string{"/oauth2", "/logout", "/oauth2/logout"},
		Ts:           pastTs,
		Ttl:          p.formatTTL(ttl),
	}
	sig, err := p.sign(unsigned)
	require.NoError(t, err)

	gc := GatewayCookie{
		Backend:      unsigned.Backend,
		DeletePaths:  unsigned.DeletePaths,
		Name:         unsigned.Name,
		Payload:      unsigned.Payload,
		Priority:     unsigned.Priority,
		RoutingPaths: unsigned.RoutingPaths,
		Signature:    sig,
		Ts:           unsigned.Ts,
		Ttl:          unsigned.Ttl,
	}
	data, err := json.Marshal(gc)
	require.NoError(t, err)
	if p.cfg.Cookie.WireCompat {
		return base64.URLEncoding.EncodeToString(data)
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

// assertCookieDeleted asserts the response sets TG.OAUTH2 with a delete marker.
func assertCookieDeleted(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	setCookie := w.Header().Get("Set-Cookie")
	require.NotEmpty(t, setCookie, "expected a Set-Cookie header")
	assert.Contains(t, setCookie, cookieName+"=delete")
	assert.Contains(t, setCookie, "Max-Age=0")
}
