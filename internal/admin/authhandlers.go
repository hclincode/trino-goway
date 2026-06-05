package admin

import (
	"net/http"
	"strings"

	"github.com/hclincode/trino-goway/internal/auth"
)

// oidcStateCookie carries the OAuth2 state and nonce between /sso and
// /oidc/callback. Scoped to the callback path, short-lived, and HttpOnly so it
// is never readable by page scripts.
const oidcStateCookie = "trino-gateway-oidc"

// oidcStateMaxAge bounds how long a login attempt may stay in flight.
const oidcStateMaxAge = 15 * 60 // 15 minutes, matching Java's OIDC cookie.

// tokenCookie is the post-login cookie the Web UI reads on mount
// (useConsumeOidcCookie). It is intentionally NOT HttpOnly so the SPA can read
// the id_token and send it as a Bearer token on API calls.
const tokenCookie = "token"

// tokenCookieMaxAge mirrors Java's SessionCookie (1 day).
const tokenCookieMaxAge = 60 * 60 * 24

// uiBasePath is where the SPA is served; the callback redirects here on success.
const uiBasePath = "/trino-gateway"

// handleLoginType returns the configured login type.
// POST /loginType → Result<string>
func (a *Admin) handleLoginType(w http.ResponseWriter, r *http.Request) {
	var loginType string
	switch a.cfg.Auth.Type {
	case "OIDC":
		loginType = "oauth"
	case "LDAP":
		loginType = "form"
	default:
		loginType = "none"
	}
	writeJSON(w, http.StatusOK, resultOK(loginType))
}

// loginRequest is the request body for /login.
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleLogin handles login requests.
// POST /login → Result<{"token": string}>
func (a *Admin) handleLogin(w http.ResponseWriter, r *http.Request) {
	// For v1: if auth.Type == "NOOP", return the username as the token.
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusOK, resultErr("bad request"))
		return
	}
	if a.cfg.Auth.Type == "NOOP" || a.cfg.Auth.Type == "" {
		writeJSON(w, http.StatusOK, resultOK(map[string]string{"token": req.Username}))
		return
	}
	// For other auth types, not fully implemented in v1.
	writeJSON(w, http.StatusOK, resultErr("login not implemented for auth type: "+a.cfg.Auth.Type))
}

// handleLogout handles logout requests.
// POST /logout → Result<null>
func (a *Admin) handleLogout(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, resultOK[any](nil))
}

// handleSSO begins the Web-UI OAuth2 authorization-code flow. It returns the IdP
// authorization URL in the Result envelope; the SPA navigates the browser there.
// A short-lived, HttpOnly state cookie carries the CSRF state and nonce to the
// callback.
// POST /sso → Result<string>
func (a *Admin) handleSSO(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Auth.Type != "OIDC" || a.cfg.WebLogin == nil {
		writeJSON(w, http.StatusOK, resultErr("SSO not configured"))
		return
	}

	state, err := auth.RandomToken()
	if err != nil {
		a.cfg.Log.Error("admin: sso: generate state", "err", err)
		writeJSON(w, http.StatusOK, resultErr("internal error"))
		return
	}
	nonce, err := auth.RandomToken()
	if err != nil {
		a.cfg.Log.Error("admin: sso: generate nonce", "err", err)
		writeJSON(w, http.StatusOK, resultErr("internal error"))
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     oidcStateCookie,
		Value:    state + ":" + nonce,
		Path:     "/oidc/callback",
		MaxAge:   oidcStateMaxAge,
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})

	writeJSON(w, http.StatusOK, resultOK(a.cfg.WebLogin.AuthCodeURL(state, nonce)))
}

// handleOIDCCallback completes the authorization-code flow: it verifies the
// state cookie, exchanges the code for an id_token, sets the `token` cookie the
// UI reads on mount, and redirects to the SPA.
// GET /oidc/callback?code=&state=
func (a *Admin) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Auth.Type != "OIDC" || a.cfg.WebLogin == nil {
		http.Error(w, "OIDC not configured", http.StatusInternalServerError)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing authorization code", http.StatusBadRequest)
		return
	}

	state, nonce, ok := readOIDCStateCookie(r)
	if !ok {
		http.Error(w, "missing or malformed login state", http.StatusBadRequest)
		return
	}
	// Constant-vs-query comparison is fine here: state is a high-entropy random
	// token, and a mismatch is a hard failure (CSRF / stale attempt).
	if r.URL.Query().Get("state") != state {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		return
	}

	idToken, err := a.cfg.WebLogin.Exchange(r.Context(), code, nonce)
	if err != nil {
		a.cfg.Log.Warn("admin: oidc callback: token exchange failed", "err", err)
		http.Error(w, "login failed", http.StatusUnauthorized)
		return
	}

	// Clear the one-shot state cookie.
	http.SetCookie(w, &http.Cookie{
		Name:     oidcStateCookie,
		Value:    "",
		Path:     "/oidc/callback",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})

	// Hand the id_token to the SPA. Not HttpOnly: the UI reads it via JS on mount
	// (useConsumeOidcCookie) and sends it as a Bearer token thereafter.
	http.SetCookie(w, &http.Cookie{
		Name:     tokenCookie,
		Value:    idToken,
		Path:     "/",
		MaxAge:   tokenCookieMaxAge,
		HttpOnly: false,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})

	http.Redirect(w, r, uiBasePath, http.StatusSeeOther)
}

// readOIDCStateCookie returns the state and nonce from the OIDC state cookie.
func readOIDCStateCookie(r *http.Request) (state, nonce string, ok bool) {
	c, err := r.Cookie(oidcStateCookie)
	if err != nil {
		return "", "", false
	}
	state, nonce, found := strings.Cut(c.Value, ":")
	if !found || state == "" || nonce == "" {
		return "", "", false
	}
	return state, nonce, true
}

// requestIsHTTPS reports whether the inbound request is HTTPS, either directly
// or via a TLS-terminating proxy (X-Forwarded-Proto). Used to set the Secure
// cookie attribute without breaking plain-HTTP local/test setups.
func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// userinfoResponse is returned by /userinfo.
type userinfoResponse struct {
	UserID      string   `json:"userId"`
	UserName    string   `json:"userName"`
	Roles       []string `json:"roles"`
	Permissions []string `json:"permissions"`
}

// handleUserinfo returns the current user's identity and roles.
// POST /userinfo
func (a *Admin) handleUserinfo(w http.ResponseWriter, r *http.Request) {
	p := auth.FromContext(r.Context())
	resp := userinfoResponse{
		Roles:       []string{},
		Permissions: []string{},
	}
	if p != nil {
		resp.UserID = p.Name
		resp.UserName = p.Name
		if auth.HasRole(p, auth.RoleAdmin, a.cfg.Auth.Authorization) {
			resp.Roles = append(resp.Roles, auth.RoleAdmin)
		}
		if auth.HasRole(p, auth.RoleUser, a.cfg.Auth.Authorization) {
			resp.Roles = append(resp.Roles, auth.RoleUser)
		}
		if auth.HasRole(p, auth.RoleAPI, a.cfg.Auth.Authorization) {
			resp.Roles = append(resp.Roles, auth.RoleAPI)
		}
		resp.Permissions = auth.ResolvePagePermissions(resp.Roles, a.cfg.Auth.Authorization.PagePermissions)
	}
	writeJSON(w, http.StatusOK, resultOK(resp))
}
