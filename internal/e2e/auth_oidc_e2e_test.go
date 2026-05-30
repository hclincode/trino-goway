//go:build e2e

package e2e_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/e2e/harness"
	"github.com/hclincode/trino-goway/internal/testutil"
)

// TestE2E_OIDC_ValidToken_Admitted verifies that a valid Bearer JWT signed by
// the configured JWKS is admitted to a USER-protected admin endpoint.
func TestE2E_OIDC_ValidToken_Admitted(t *testing.T) {
	oidc := testutil.NewOIDCServer(t)
	h := harness.New(t,
		harness.WithAuthOIDC(oidc.JWKSURL()),
		harness.WithAdminRoleRegex(".*"),
	)

	token := oidc.IssueToken("alice", []string{"admins"}, 10*time.Minute)

	resp := doAuthorized(t, h.AdminURL+"/trino-gateway/api/queryHistory", "Bearer "+token)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestE2E_OIDC_InvalidToken_401 verifies that malformed and expired tokens
// are rejected with 401.
func TestE2E_OIDC_InvalidToken_401(t *testing.T) {
	oidc := testutil.NewOIDCServer(t)
	h := harness.New(t,
		harness.WithAuthOIDC(oidc.JWKSURL()),
		harness.WithAdminRoleRegex(".*"),
	)

	t.Run("malformed token", func(t *testing.T) {
		resp := doAuthorized(t, h.AdminURL+"/trino-gateway/api/queryHistory", "Bearer invalidtoken123")
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("expired token", func(t *testing.T) {
		// ttl < 0 yields an exp claim in the past.
		expired := oidc.IssueToken("alice", []string{"admins"}, -1*time.Second)
		resp := doAuthorized(t, h.AdminURL+"/trino-gateway/api/queryHistory", "Bearer "+expired)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("missing bearer", func(t *testing.T) {
		resp := doAuthorized(t, h.AdminURL+"/trino-gateway/api/queryHistory", "")
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

// TestE2E_OIDC_GroupsClaimMapsToRole verifies that the JWT "groups" claim is
// matched against auth.authorization.admin to grant ADMIN role.
func TestE2E_OIDC_GroupsClaimMapsToRole(t *testing.T) {
	oidc := testutil.NewOIDCServer(t)
	h := harness.New(t,
		harness.WithAuthOIDC(oidc.JWKSURL()),
		harness.WithAdminRoleRegex("platform-admin"),
	)

	saveBackendBody := `{"name":"oidc-test","proxyTo":"http://fake:9999","active":true,"routingGroup":"default"}`

	t.Run("admin group passes ADMIN gate", func(t *testing.T) {
		token := oidc.IssueToken("alice", []string{"platform-admin"}, 10*time.Minute)
		resp := postWithAuth(t, h.AdminURL+"/webapp/saveBackend", "Bearer "+token, saveBackendBody)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("non-admin group fails ADMIN gate", func(t *testing.T) {
		token := oidc.IssueToken("bob", []string{"regular-user"}, 10*time.Minute)
		resp := postWithAuth(t, h.AdminURL+"/webapp/saveBackend", "Bearer "+token, saveBackendBody)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})
}

// TestE2E_OIDC_JWKSRefresh verifies that the background JWKS refresher picks
// up a rotated key within jwksTtlSecs: tokens signed by the old key are
// rejected, while tokens signed by the new key are accepted.
func TestE2E_OIDC_JWKSRefresh(t *testing.T) {
	oidc := testutil.NewOIDCServer(t)
	h := harness.New(t,
		harness.WithAuthOIDC(oidc.JWKSURL()),
		harness.WithJWKSTTL(1),
		harness.WithAdminRoleRegex(".*"),
	)

	// Sanity: the initial token works.
	tokenOld := oidc.IssueToken("alice", nil, 10*time.Minute)
	{
		resp := doAuthorized(t, h.AdminURL+"/trino-gateway/api/queryHistory", "Bearer "+tokenOld)
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, "pre-rotation: token must be accepted")
	}

	oidc.RotateKey()

	// Poll for up to ~6s — the ticker interval is 1s but the keyfunc fetch is
	// not instantaneous and CI clocks can be jittery.
	deadline := time.Now().Add(6 * time.Second)
	var lastStatus int
	for time.Now().Before(deadline) {
		resp := doAuthorized(t, h.AdminURL+"/trino-gateway/api/queryHistory", "Bearer "+tokenOld)
		lastStatus = resp.StatusCode
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if lastStatus == http.StatusUnauthorized {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.Equalf(t, http.StatusUnauthorized, lastStatus,
		"post-rotation: stale token must be rejected once JWKS refreshes (last status=%d)", lastStatus)

	tokenNew := oidc.IssueToken("alice", nil, 10*time.Minute)
	resp := doAuthorized(t, h.AdminURL+"/trino-gateway/api/queryHistory", "Bearer "+tokenNew)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "post-rotation: freshly signed token must be accepted")
}

// TestE2E_OIDC_MissingJwksUrl_StartupFails verifies that the gateway refuses
// to start when auth.type=OIDC but no jwksUrl is provided.
func TestE2E_OIDC_MissingJwksUrl_StartupFails(t *testing.T) {
	cfg := `proxy:
  port: %d
admin:
  port: %d
db:
  driver: postgres
  dsn: "%s"
routing:
  defaultGroup: default
  type: EXTERNAL
auth:
  type: OIDC
`
	err := runBinaryWithBadConfig(t, cfg)
	require.Error(t, err, "binary must exit non-zero when auth.oidc.jwksUrl is missing")
}

// TestE2E_OIDC_UnreachableJWKS_StartupFails verifies that the gateway refuses
// to start when jwksUrl points to an unreachable endpoint (fail-fast on JWKS
// fetch during startup).
func TestE2E_OIDC_UnreachableJWKS_StartupFails(t *testing.T) {
	cfg := `proxy:
  port: %d
admin:
  port: %d
db:
  driver: postgres
  dsn: "%s"
routing:
  defaultGroup: default
  type: EXTERNAL
auth:
  type: OIDC
  oidc:
    jwksUrl: "http://127.0.0.1:1/jwks.json"
`
	err := runBinaryWithBadConfig(t, cfg)
	require.Error(t, err, "binary must exit non-zero when jwksUrl is unreachable on startup")
}

// doAuthorized issues a GET with an optional Authorization header and returns
// the response. Callers are responsible for closing the body.
func doAuthorized(t *testing.T, url, authValue string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	if authValue != "" {
		req.Header.Set("Authorization", authValue)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}

// postWithAuth issues a POST with a JSON body and Authorization header.
func postWithAuth(t *testing.T, url, authValue, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if authValue != "" {
		req.Header.Set("Authorization", authValue)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}

// runBinaryWithBadConfig writes cfg (which must contain three %d/%s verbs for
// proxyPort, adminPort, dbDSN respectively) into a temp YAML file and runs the
// trino-goway binary against it with a 10-second deadline. It returns the
// exit error from cmd.Wait, which is non-nil when the binary exits non-zero.
func runBinaryWithBadConfig(t *testing.T, cfgTmpl string) error {
	t.Helper()

	bin := harness.BinaryPath(t)
	dsn := testutil.PostgresContainerDSN(t)
	proxyPort := testutil.FreePort(t)
	adminPort := testutil.FreePort(t)
	for adminPort == proxyPort {
		adminPort = testutil.FreePort(t)
	}

	cfgPath := filepath.Join(t.TempDir(), "bad-config.yaml")
	rendered := fmt.Sprintf(cfgTmpl, proxyPort, adminPort, dsn)
	require.NoError(t, os.WriteFile(cfgPath, []byte(rendered), 0o600))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "--config", cfgPath)
	// Capture stderr to surface the failure reason on test failure.
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	return cmd.Run()
}
