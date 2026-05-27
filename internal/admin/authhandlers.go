package admin

import (
	"net/http"

	"github.com/hclincode/trino-goway/internal/auth"
)

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

// handleSSO redirects to OIDC, or returns 500 if not configured.
// POST /sso
func (a *Admin) handleSSO(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Auth.Type != "OIDC" {
		http.Error(w, "SSO not configured", http.StatusInternalServerError)
		return
	}
	// Full OIDC flow deferred; redirect to issuer URL as a stub.
	http.Redirect(w, r, a.cfg.Auth.OIDC.IssuerURL, http.StatusFound)
}

// handleOIDCCallback handles the OIDC redirect callback.
// GET /oidc/callback
func (a *Admin) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Auth.Type != "OIDC" {
		http.Error(w, "OIDC not configured", http.StatusInternalServerError)
		return
	}
	// Full OIDC callback handling deferred for v1.
	http.Error(w, "OIDC callback not implemented", http.StatusNotImplemented)
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
	}
	writeJSON(w, http.StatusOK, resultOK(resp))
}
