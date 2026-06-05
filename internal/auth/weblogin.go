package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"

	"github.com/hclincode/trino-goway/internal/config"
)

// OIDCWebLogin drives the interactive Web-UI OAuth2 authorization-code flow:
// it builds the IdP authorization URL (/sso) and exchanges the returned code for
// an id_token (/oidc/callback). It is distinct from OIDCMiddleware, which only
// validates Bearer tokens on API requests.
type OIDCWebLogin struct {
	cfg        config.OIDCConfig
	httpClient *http.Client
	authzURL   string
	tokenURL   string
	jwks       keyfunc.Keyfunc
}

// discoveryDocument is the subset of the OIDC discovery metadata we consume.
type discoveryDocument struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
}

// NewOIDCWebLogin resolves the authorization and token endpoints (via the
// configured overrides or OIDC discovery) and loads the JWKS used to validate
// the id_token returned from the token exchange.
func NewOIDCWebLogin(ctx context.Context, cfg config.OIDCConfig, httpClient *http.Client) (*OIDCWebLogin, error) {
	if cfg.RedirectURL == "" {
		return nil, fmt.Errorf("auth: oidc weblogin: redirectUrl is required")
	}
	if cfg.ClientID == "" {
		return nil, fmt.Errorf("auth: oidc weblogin: clientId is required")
	}

	authzURL := cfg.AuthorizationEndpoint
	tokenURL := cfg.TokenEndpoint
	if authzURL == "" || tokenURL == "" {
		doc, err := discover(ctx, httpClient, cfg.IssuerURL)
		if err != nil {
			return nil, err
		}
		if authzURL == "" {
			authzURL = doc.AuthorizationEndpoint
		}
		if tokenURL == "" {
			tokenURL = doc.TokenEndpoint
		}
	}
	if authzURL == "" || tokenURL == "" {
		return nil, fmt.Errorf("auth: oidc weblogin: could not resolve authorization/token endpoints")
	}

	jwks, err := keyfunc.NewDefaultCtx(ctx, []string{cfg.JWKSURL})
	if err != nil {
		return nil, fmt.Errorf("auth: oidc weblogin: load jwks: %w", err)
	}

	return &OIDCWebLogin{
		cfg:        cfg,
		httpClient: httpClient,
		authzURL:   authzURL,
		tokenURL:   tokenURL,
		jwks:       jwks,
	}, nil
}

// AuthCodeURL returns the IdP authorization URL for the given state and nonce.
// The caller persists state+nonce in a short-lived cookie and verifies them on
// the callback to defend against CSRF and replay.
func (w *OIDCWebLogin) AuthCodeURL(state, nonce string) string {
	scopes := w.cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{"openid"}
	}
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", w.cfg.ClientID)
	q.Set("redirect_uri", w.cfg.RedirectURL)
	q.Set("scope", strings.Join(scopes, " "))
	q.Set("state", state)
	q.Set("nonce", nonce)

	sep := "?"
	if strings.Contains(w.authzURL, "?") {
		sep = "&"
	}
	return w.authzURL + sep + q.Encode()
}

// tokenResponse is the subset of the OAuth2 token endpoint response we consume.
type tokenResponse struct {
	IDToken     string `json:"id_token"`
	AccessToken string `json:"access_token"`
	Error       string `json:"error"`
	ErrorDesc   string `json:"error_description"`
}

// Exchange swaps the authorization code for tokens and returns the validated
// id_token. The id_token signature is checked against the JWKS and, when nonce
// is non-empty, the token's nonce claim must match.
func (w *OIDCWebLogin) Exchange(ctx context.Context, code, nonce string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", w.cfg.RedirectURL)
	form.Set("client_id", w.cfg.ClientID)
	if w.cfg.ClientSecret != "" {
		form.Set("client_secret", w.cfg.ClientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("auth: oidc weblogin: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("auth: oidc weblogin: token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("auth: oidc weblogin: read token response: %w", err)
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("auth: oidc weblogin: decode token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || tr.Error != "" {
		return "", fmt.Errorf("auth: oidc weblogin: token endpoint failed: status %d: %s", resp.StatusCode, tr.errorMessage())
	}
	if tr.IDToken == "" {
		return "", fmt.Errorf("auth: oidc weblogin: token response missing id_token")
	}

	if err := w.validateIDToken(tr.IDToken, nonce); err != nil {
		return "", err
	}
	return tr.IDToken, nil
}

// validateIDToken verifies the id_token signature against the JWKS and, when a
// nonce is supplied, that the token's nonce claim matches.
func (w *OIDCWebLogin) validateIDToken(idToken, nonce string) error {
	claims := jwt.MapClaims{}
	if _, err := jwt.ParseWithClaims(idToken, claims, w.jwks.Keyfunc); err != nil {
		return fmt.Errorf("auth: oidc weblogin: validate id_token: %w", err)
	}
	if nonce != "" {
		got, _ := claims["nonce"].(string)
		if got != nonce {
			return fmt.Errorf("auth: oidc weblogin: id_token nonce mismatch")
		}
	}
	return nil
}

func (t tokenResponse) errorMessage() string {
	if t.ErrorDesc != "" {
		return t.Error + ": " + t.ErrorDesc
	}
	return t.Error
}

// discover fetches the OIDC discovery document from the issuer's
// well-known endpoint.
func discover(ctx context.Context, httpClient *http.Client, issuerURL string) (*discoveryDocument, error) {
	if issuerURL == "" {
		return nil, fmt.Errorf("auth: oidc weblogin: issuerUrl is required for discovery")
	}
	discoveryURL := strings.TrimRight(issuerURL, "/") + "/.well-known/openid-configuration"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("auth: oidc weblogin: build discovery request: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("auth: oidc weblogin: discovery request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auth: oidc weblogin: discovery returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("auth: oidc weblogin: read discovery: %w", err)
	}
	var doc discoveryDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("auth: oidc weblogin: decode discovery: %w", err)
	}
	return &doc, nil
}

// RandomToken returns a cryptographically random URL-safe token, used for the
// OAuth2 state and nonce values.
func RandomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: oidc weblogin: generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
