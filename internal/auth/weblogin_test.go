package auth

import (
	"context"
	"net/http"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/config"
	"github.com/hclincode/trino-goway/internal/testutil"
)

// TestOIDCWebLogin_FullFlow exercises the authorization-code flow end to end
// against the in-process OIDC server: discovery resolves the endpoints, the
// authorize URL is well-formed, and Exchange returns a nonce-validated id_token.
func TestOIDCWebLogin_FullFlow(t *testing.T) {
	srv := testutil.NewOIDCServer(t)

	cfg := config.OIDCConfig{
		IssuerURL:   srv.URL,
		ClientID:    "trino-gateway",
		JWKSURL:     srv.JWKSURL(),
		RedirectURL: "https://gw.example.com/oidc/callback",
		Scopes:      []string{"openid", "profile"},
		// AuthorizationEndpoint/TokenEndpoint omitted → resolved via discovery.
	}

	wl, err := NewOIDCWebLogin(context.Background(), cfg, http.DefaultClient)
	require.NoError(t, err)

	// AuthCodeURL carries the required OAuth2 parameters.
	authURL := wl.AuthCodeURL("the-state", "the-nonce")
	assert.Contains(t, authURL, srv.AuthorizeURL())
	assert.Contains(t, authURL, "response_type=code")
	assert.Contains(t, authURL, "client_id=trino-gateway")
	assert.Contains(t, authURL, "state=the-state")
	assert.Contains(t, authURL, "nonce=the-nonce")
	assert.Contains(t, authURL, "scope=openid+profile")

	// Drive the authorize endpoint (it redirects with a code) to mimic the IdP.
	code := authorizeCode(t, authURL)

	idToken, err := wl.Exchange(context.Background(), code, "the-nonce")
	require.NoError(t, err)
	require.NotEmpty(t, idToken)

	// The returned token is a valid JWT carrying the nonce we requested.
	claims := jwt.MapClaims{}
	_, _, err = jwt.NewParser().ParseUnverified(idToken, claims)
	require.NoError(t, err)
	assert.Equal(t, "the-nonce", claims["nonce"])
	assert.Equal(t, "web-user", claims["sub"])
}

func TestOIDCWebLogin_ExplicitEndpoints_NoDiscovery(t *testing.T) {
	srv := testutil.NewOIDCServer(t)

	cfg := config.OIDCConfig{
		ClientID:              "trino-gateway",
		JWKSURL:               srv.JWKSURL(),
		RedirectURL:           "https://gw.example.com/oidc/callback",
		AuthorizationEndpoint: srv.AuthorizeURL(),
		TokenEndpoint:         srv.TokenURL(),
		// IssuerURL omitted: must not be needed when endpoints are explicit.
	}

	wl, err := NewOIDCWebLogin(context.Background(), cfg, http.DefaultClient)
	require.NoError(t, err)

	code := authorizeCode(t, wl.AuthCodeURL("s", "n"))
	idToken, err := wl.Exchange(context.Background(), code, "n")
	require.NoError(t, err)
	assert.NotEmpty(t, idToken)
}

func TestOIDCWebLogin_NonceMismatchRejected(t *testing.T) {
	srv := testutil.NewOIDCServer(t)
	cfg := config.OIDCConfig{
		IssuerURL:   srv.URL,
		ClientID:    "trino-gateway",
		JWKSURL:     srv.JWKSURL(),
		RedirectURL: "https://gw.example.com/oidc/callback",
	}
	wl, err := NewOIDCWebLogin(context.Background(), cfg, http.DefaultClient)
	require.NoError(t, err)

	code := authorizeCode(t, wl.AuthCodeURL("s", "real-nonce"))
	// Ask Exchange to verify against a different nonce than the token carries.
	_, err = wl.Exchange(context.Background(), code, "attacker-nonce")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonce mismatch")
}

func TestOIDCWebLogin_RequiresRedirectURL(t *testing.T) {
	srv := testutil.NewOIDCServer(t)
	cfg := config.OIDCConfig{
		IssuerURL: srv.URL,
		ClientID:  "trino-gateway",
		JWKSURL:   srv.JWKSURL(),
		// RedirectURL omitted.
	}
	_, err := NewOIDCWebLogin(context.Background(), cfg, http.DefaultClient)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "redirectUrl is required")
}

// authorizeCode drives the OIDC server's authorize endpoint and returns the
// code from the redirect Location.
func authorizeCode(t *testing.T, authURL string) string {
	t.Helper()
	// Do not follow the redirect; capture the Location.
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(authURL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusFound, resp.StatusCode)

	loc, err := resp.Location()
	require.NoError(t, err)
	code := loc.Query().Get("code")
	require.NotEmpty(t, code)
	return code
}
