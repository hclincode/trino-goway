//go:build e2e

package e2e_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/e2e/harness"
)

// updateCookieGolden, when true, rewrites the wire-compat golden file from the
// running Go implementation. Set via `go test -tags=e2e -run TestE2E_Cookie -update`.
var updateCookieGolden = flag.Bool("update", false, "update cookie golden files")

const (
	cookieName   = "TG.OAUTH2"
	cookieSecret = "test-secret-32bytes-padding-here"
)

// unsignedCookie mirrors proxy.UnsignedGatewayCookie. Field order is alphabetical
// by JSON tag to match the Go encoder's declaration-order serialization, which is
// the input the HMAC is computed over.
type unsignedCookie struct {
	Backend      *string  `json:"backend"`
	DeletePaths  []string `json:"deletePaths"`
	Name         string   `json:"name"`
	Payload      *string  `json:"payload"`
	Priority     int      `json:"priority"`
	RoutingPaths []string `json:"routingPaths"`
	Ts           int64    `json:"ts"`
	Ttl          string   `json:"ttl"`
}

// signedCookie mirrors proxy.GatewayCookie (adds Signature field, alphabetical).
type signedCookie struct {
	Backend      *string  `json:"backend"`
	DeletePaths  []string `json:"deletePaths"`
	Name         string   `json:"name"`
	Payload      *string  `json:"payload"`
	Priority     int      `json:"priority"`
	RoutingPaths []string `json:"routingPaths"`
	Signature    string   `json:"signature"`
	Ts           int64    `json:"ts"`
	Ttl          string   `json:"ttl"`
}

// signCookie returns the lowercase-hex HMAC-SHA256 of the JSON encoding of u
// using the given secret. Matches proxy.Proxy.sign.
func signCookie(t *testing.T, secret string, u unsignedCookie) string {
	t.Helper()
	payload, err := json.Marshal(u)
	require.NoError(t, err)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// encodeCookieValue marshals s to JSON, base64-URLEncodes it (with padding,
// matching wireCompat=true), and returns the cookie wire value.
func encodeCookieValue(t *testing.T, s signedCookie) string {
	t.Helper()
	data, err := json.Marshal(s)
	require.NoError(t, err)
	return base64.URLEncoding.EncodeToString(data)
}

// buildSignedCookieValue constructs a TG.OAUTH2 cookie wire value with the
// given backend URL and issuance timestamp, signed with secret.
func buildSignedCookieValue(t *testing.T, secret, backend string, ts int64, ttl string) string {
	t.Helper()
	u := unsignedCookie{
		Backend:      &backend,
		DeletePaths:  []string{"/logout", "/oauth2/logout"},
		Name:         cookieName,
		Payload:      nil,
		Priority:     0,
		RoutingPaths: []string{"/oauth2", "/logout", "/oauth2/logout"},
		Ts:           ts,
		Ttl:          ttl,
	}
	sig := signCookie(t, secret, u)
	return encodeCookieValue(t, signedCookie{
		Backend:      u.Backend,
		DeletePaths:  u.DeletePaths,
		Name:         u.Name,
		Payload:      u.Payload,
		Priority:     u.Priority,
		RoutingPaths: u.RoutingPaths,
		Signature:    sig,
		Ts:           u.Ts,
		Ttl:          u.Ttl,
	})
}

// getOAuth2 issues GET <ProxyURL>/oauth2/<suffix> with the optional cookie
// header attached. Returns the response; caller closes the body.
func getOAuth2(t *testing.T, h *harness.Harness, suffix, cookieHeader string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, h.ProxyURL+"/oauth2/"+suffix, nil)
	require.NoError(t, err)
	req.Header.Set("X-Trino-User", "cookie-e2e")
	if cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	}
	resp, err := h.ProxyClient().Do(req)
	require.NoError(t, err)
	return resp
}

// findSetCookie returns the first Set-Cookie header whose value starts with
// "TG.OAUTH2=", or "" if none.
func findSetCookie(headers http.Header) string {
	for _, sc := range headers.Values("Set-Cookie") {
		if strings.HasPrefix(sc, cookieName+"=") {
			return sc
		}
	}
	return ""
}

// TestE2E_Cookie_IssuedOnOAuth2Path verifies the gateway issues a TG.OAUTH2
// cookie on the response when a /oauth2/* request arrives without one.
// USE_STORIES §1.5 — sticky-routing cookie surface contract.
func TestE2E_Cookie_IssuedOnOAuth2Path(t *testing.T) {
	h := harness.New(t, harness.WithCookieSecret(cookieSecret))
	h.AddBackend(t, "trino-1", "default")

	resp := getOAuth2(t, h, "callback", "")
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	setCookie := findSetCookie(resp.Header)
	require.NotEmpty(t, setCookie, "expected TG.OAUTH2 Set-Cookie header on /oauth2/* response")

	assert.Contains(t, setCookie, "HttpOnly", "TG.OAUTH2 must be HttpOnly")
	assert.Contains(t, setCookie, "SameSite=Lax", "TG.OAUTH2 must be SameSite=Lax")
	assert.Contains(t, setCookie, "Path=/", "TG.OAUTH2 must be scoped Path=/")
	assert.Contains(t, setCookie, "Max-Age=", "TG.OAUTH2 must include Max-Age attribute")
	assert.NotContains(t, setCookie, "Max-Age=0",
		"freshly-issued cookie must not be a delete-cookie (Max-Age=0)")
}

// TestE2E_Cookie_TamperedHMAC_Returns500 verifies the gateway responds 500
// when a TG.OAUTH2 cookie arrives with a valid encoding but incorrect HMAC.
// Hard Invariant #5: HMAC failure is fail-closed; the request must NOT be
// silently treated as anonymous.
func TestE2E_Cookie_TamperedHMAC_Returns500(t *testing.T) {
	h := harness.New(t, harness.WithCookieSecret(cookieSecret))
	h.AddBackend(t, "trino-1", "default")

	// Build a syntactically-valid cookie signed with the WRONG secret — the
	// gateway will decode + parse it but the HMAC check fails.
	tampered := buildSignedCookieValue(t, "wrong-secret-but-valid-bytes-xxx",
		"http://backend.example:8080", time.Now().UnixMilli(), "10.00m")

	resp := getOAuth2(t, h, "callback", cookieName+"="+tampered)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	assert.Equalf(t, http.StatusInternalServerError, resp.StatusCode,
		"tampered TG.OAUTH2 cookie must produce 500 (Hard Invariant #5); body=%s",
		string(body))
}

// TestE2E_Cookie_EmptySecret_NeverEmits verifies that with no cookie.secret
// configured, the gateway never issues TG.OAUTH2 even on /oauth2/* requests.
func TestE2E_Cookie_EmptySecret_NeverEmits(t *testing.T) {
	h := harness.New(t) // no WithCookieSecret
	h.AddBackend(t, "trino-1", "default")

	resp := getOAuth2(t, h, "callback", "")
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	assert.Empty(t, findSetCookie(resp.Header),
		"empty cookie.secret must suppress TG.OAUTH2 issuance entirely")
}

// TestE2E_Cookie_ExpiryEmitsDeleteCookie verifies the gateway, when presented
// with a properly-signed but expired TG.OAUTH2 cookie, emits a delete-cookie
// (Max-Age=0) on the response AND serves the request (HTTP 200/404 from the
// backend, never 401). Hard Invariant #5 corollary: expiry is silent, not fail.
func TestE2E_Cookie_ExpiryEmitsDeleteCookie(t *testing.T) {
	h := harness.New(t, harness.WithCookieSecret(cookieSecret))
	h.AddBackend(t, "trino-1", "default")

	// Build a cookie issued 1h ago with a 10m TTL — clearly expired.
	pastTs := time.Now().Add(-time.Hour).UnixMilli()
	expired := buildSignedCookieValue(t, cookieSecret,
		"http://backend.example:8080", pastTs, "10.00m")

	resp := getOAuth2(t, h, "userinfo", cookieName+"="+expired)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// Request is served — the fake returns 404 for /oauth2/userinfo, but the
	// gateway never short-circuits with 401 on expired cookies.
	assert.NotEqualf(t, http.StatusUnauthorized, resp.StatusCode,
		"expired cookie must not produce 401; body=%s", string(body))
	assert.NotEqualf(t, http.StatusInternalServerError, resp.StatusCode,
		"expired (but properly-signed) cookie must not produce 500; body=%s", string(body))

	setCookie := findSetCookie(resp.Header)
	require.NotEmpty(t, setCookie, "expired cookie must trigger a delete Set-Cookie header")
	assert.Contains(t, setCookie, "Max-Age=0",
		"delete-cookie must use Max-Age=0 to instruct the browser to drop it")
}

// TestE2E_Cookie_WireCompat_GoldenBytes pins the JSON wire format of the
// signed TG.OAUTH2 cookie body. A drift in field order, null handling, or HMAC
// input would break wire-compat with Java's GatewayCookie (Hard Invariant #10).
//
// NOTE: the golden file in this commit is generated from the Go implementation
// itself, not from a live Java gateway. It guards against Go-internal drift.
// A follow-up should replace the golden bytes with output captured from the
// Java GatewayCookieFilter so this test asserts true cross-implementation
// parity, not just Go self-consistency.
func TestE2E_Cookie_WireCompat_GoldenBytes(t *testing.T) {
	h := harness.New(t, harness.WithCookieSecret(cookieSecret))
	h.AddBackend(t, "trino-1", "default")

	resp := getOAuth2(t, h, "authorize", "")
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	setCookie := findSetCookie(resp.Header)
	require.NotEmpty(t, setCookie, "expected TG.OAUTH2 Set-Cookie for wire-compat capture")

	// Extract the cookie value (everything between "=" and the first ";").
	value := extractCookieAttrValue(t, setCookie, cookieName)
	raw, err := base64.URLEncoding.DecodeString(value)
	require.NoError(t, err, "wire-compat cookie value must be standard base64 URL-encoded")

	// Decode + re-marshal with canonical (key-sorted) JSON so the golden file
	// is stable across runs even though the live cookie embeds runtime values
	// (Ts, Backend URL with random port, Signature). The golden compares only
	// the field set + types — the only stable wire-format property the cookie
	// guarantees at runtime.
	stable := stabilizeCookieJSON(t, raw)

	goldenPath := filepath.Join("testdata", "cookie_wire_compat.golden")
	if *updateCookieGolden {
		require.NoError(t, os.WriteFile(goldenPath, stable, 0o644))
		t.Logf("updated %s", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if os.IsNotExist(err) {
		require.NoError(t, os.WriteFile(goldenPath, stable, 0o644),
			"bootstrap: write initial golden file")
		t.Logf("bootstrapped %s — re-run to verify", goldenPath)
		return
	}
	require.NoError(t, err, "read golden file")

	assert.Equalf(t, string(want), string(stable),
		"wire-format drift: cookie JSON shape no longer matches %s "+
			"(re-run with -update to regenerate; replace with Java-captured bytes for true parity)",
		goldenPath)
}

// extractCookieAttrValue parses a Set-Cookie header value and returns the
// substring after "<name>=" up to (but not including) the first ";". Fails
// the test if the cookie name is not the leading attribute.
func extractCookieAttrValue(t *testing.T, setCookie, name string) string {
	t.Helper()
	prefix := name + "="
	require.Truef(t, strings.HasPrefix(setCookie, prefix),
		"Set-Cookie does not start with %s: %q", prefix, setCookie)
	rest := setCookie[len(prefix):]
	if i := strings.Index(rest, ";"); i >= 0 {
		return rest[:i]
	}
	return rest
}

// stabilizeCookieJSON decodes the cookie JSON body, then re-emits a fixed-shape
// representation: field names sorted, runtime-variable fields (ts, backend,
// signature) replaced with the constant "<runtime>". The result is what gets
// pinned in the golden file — a stable wire-shape signature, not the literal
// byte stream of any single run.
func stabilizeCookieJSON(t *testing.T, raw []byte) []byte {
	t.Helper()
	var obj map[string]any
	require.NoError(t, json.Unmarshal(raw, &obj), "cookie body must be valid JSON")

	for _, k := range []string{"ts", "backend", "signature"} {
		if _, ok := obj[k]; ok {
			obj[k] = "<runtime>"
		}
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	require.NoError(t, enc.Encode(obj))
	return buf.Bytes()
}

