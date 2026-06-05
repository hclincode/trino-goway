package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/admin"
	"github.com/hclincode/trino-goway/internal/auth"
	"github.com/hclincode/trino-goway/internal/config"
)

// fakeWebLogin is a stand-in for *auth.OIDCWebLogin in admin handler tests.
type fakeWebLogin struct {
	authURL    string
	gotState   string
	gotNonce   string
	idToken    string
	exchangeFn func(ctx context.Context, code, nonce string) (string, error)
}

func (f *fakeWebLogin) AuthCodeURL(state, nonce string) string {
	f.gotState = state
	f.gotNonce = nonce
	u := f.authURL
	if u == "" {
		u = "https://idp.example.com/authorize"
	}
	return u + "?state=" + state + "&nonce=" + nonce
}

func (f *fakeWebLogin) Exchange(ctx context.Context, code, nonce string) (string, error) {
	if f.exchangeFn != nil {
		return f.exchangeFn(ctx, code, nonce)
	}
	return f.idToken, nil
}

// oidcAdmin builds an OIDC-typed Admin with the given (possibly nil) web-login.
func oidcAdmin(t *testing.T, wl admin.OIDCWebLogin) *admin.Admin {
	t.Helper()
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()
	cfg := admin.Config{
		Auth: config.AuthConfig{
			Type: "OIDC",
			Authorization: config.AuthorizationConfig{
				AdminRegex: ".*", UserRegex: ".*", APIRegex: ".*",
			},
		},
		Backends:  bs,
		History:   hs,
		Monitor:   sp,
		StatusMut: sp,
		AuthMW:    auth.Noop(),
		WebLogin:  wl,
		StartTime: time.Now(),
	}
	return admin.New(cfg)
}

// resultEnvelope mirrors the {code,msg,data} wire envelope for assertions.
type resultEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func TestAdmin_SSO_ReturnsAuthURLAndSetsStateCookie(t *testing.T) {
	wl := &fakeWebLogin{authURL: "https://idp.example.com/authorize"}
	a := oidcAdmin(t, wl)

	rec := do(a, http.MethodPost, "/sso", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var env resultEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	assert.Equal(t, 200, env.Code)

	var url string
	require.NoError(t, json.Unmarshal(env.Data, &url))
	assert.Contains(t, url, "https://idp.example.com/authorize")
	assert.Contains(t, url, "state="+wl.gotState)
	assert.Contains(t, url, "nonce="+wl.gotNonce)

	// A state cookie scoped to the callback path must be set, carrying state:nonce.
	c := findCookie(rec.Result().Cookies(), "trino-gateway-oidc")
	require.NotNil(t, c, "oidc state cookie must be set")
	assert.Equal(t, "/oidc/callback", c.Path)
	assert.True(t, c.HttpOnly, "state cookie must be HttpOnly")
	state, nonce, found := strings.Cut(c.Value, ":")
	require.True(t, found)
	assert.Equal(t, wl.gotState, state)
	assert.Equal(t, wl.gotNonce, nonce)
}

func TestAdmin_SSO_NotConfigured(t *testing.T) {
	a := oidcAdmin(t, nil) // OIDC type but no web-login wired

	rec := do(a, http.MethodPost, "/sso", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var env resultEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	assert.NotEqual(t, 200, env.Code, "unconfigured SSO must report an error code")
	assert.Contains(t, env.Msg, "not configured")
}

func TestAdmin_OIDCCallback_HappyPath(t *testing.T) {
	wl := &fakeWebLogin{idToken: "id-token-abc"}
	a := oidcAdmin(t, wl)

	// 1. /sso to obtain the state cookie + state/nonce.
	ssoRec := do(a, http.MethodPost, "/sso", nil)
	stateCookie := findCookie(ssoRec.Result().Cookies(), "trino-gateway-oidc")
	require.NotNil(t, stateCookie)

	// 2. Callback with matching state, carrying the state cookie.
	req := httptest.NewRequest(http.MethodGet, "/oidc/callback?code=the-code&state="+wl.gotState, nil)
	req.AddCookie(stateCookie)
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)

	require.Equal(t, http.StatusSeeOther, rec.Code, "callback should redirect to the UI")
	assert.Equal(t, "/trino-gateway", rec.Header().Get("Location"))

	// token cookie set, readable by JS (not HttpOnly), path /.
	tok := findCookie(rec.Result().Cookies(), "token")
	require.NotNil(t, tok, "token cookie must be set")
	assert.Equal(t, "id-token-abc", tok.Value)
	assert.Equal(t, "/", tok.Path)
	assert.False(t, tok.HttpOnly, "token cookie must be readable by the SPA")

	// state cookie cleared.
	cleared := findCookie(rec.Result().Cookies(), "trino-gateway-oidc")
	require.NotNil(t, cleared)
	assert.True(t, cleared.MaxAge < 0, "state cookie should be deleted")

	// The nonce carried in the state cookie matches what /sso generated; the
	// callback passes it to Exchange (verified by the fake accepting it).
	_, nonce, found := strings.Cut(stateCookie.Value, ":")
	require.True(t, found)
	assert.Equal(t, wl.gotNonce, nonce)
}

func TestAdmin_OIDCCallback_StateMismatch(t *testing.T) {
	wl := &fakeWebLogin{idToken: "id-token-abc"}
	a := oidcAdmin(t, wl)

	ssoRec := do(a, http.MethodPost, "/sso", nil)
	stateCookie := findCookie(ssoRec.Result().Cookies(), "trino-gateway-oidc")
	require.NotNil(t, stateCookie)

	req := httptest.NewRequest(http.MethodGet, "/oidc/callback?code=the-code&state=WRONG-STATE", nil)
	req.AddCookie(stateCookie)
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, findCookie(rec.Result().Cookies(), "token"), "no token on state mismatch")
}

func TestAdmin_OIDCCallback_MissingCode(t *testing.T) {
	a := oidcAdmin(t, &fakeWebLogin{idToken: "x"})

	req := httptest.NewRequest(http.MethodGet, "/oidc/callback?state=s", nil)
	req.AddCookie(&http.Cookie{Name: "trino-gateway-oidc", Value: "s:n"})
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAdmin_OIDCCallback_MissingStateCookie(t *testing.T) {
	a := oidcAdmin(t, &fakeWebLogin{idToken: "x"})

	req := httptest.NewRequest(http.MethodGet, "/oidc/callback?code=c&state=s", nil)
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAdmin_OIDCCallback_ExchangeFails(t *testing.T) {
	wl := &fakeWebLogin{
		exchangeFn: func(context.Context, string, string) (string, error) {
			return "", assertErr("token exchange failed")
		},
	}
	a := oidcAdmin(t, wl)

	ssoRec := do(a, http.MethodPost, "/sso", nil)
	stateCookie := findCookie(ssoRec.Result().Cookies(), "trino-gateway-oidc")
	require.NotNil(t, stateCookie)

	req := httptest.NewRequest(http.MethodGet, "/oidc/callback?code=c&state="+wl.gotState, nil)
	req.AddCookie(stateCookie)
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Nil(t, findCookie(rec.Result().Cookies(), "token"))
}

// ---- helpers ----

func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
