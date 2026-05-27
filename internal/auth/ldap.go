package auth

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-ldap/ldap/v3"

	"github.com/hclincode/trino-goway/internal/config"
)

// LDAPMiddleware authenticates requests via HTTP Basic credentials bound against an LDAP server.
// On success it attaches a Principal with the user's memberOf attribute.
type LDAPMiddleware struct {
	cfg config.LDAPConfig
	log *slog.Logger
}

// NewLDAP creates an LDAPMiddleware.
func NewLDAP(cfg config.LDAPConfig, log *slog.Logger) *LDAPMiddleware {
	return &LDAPMiddleware{cfg: cfg, log: log}
}

// Handler returns a chi-compatible middleware that validates HTTP Basic credentials against LDAP.
func (m *LDAPMiddleware) Handler() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			username, password, ok := r.BasicAuth()
			if !ok || username == "" || password == "" {
				writeUnauthorized(w, "Basic auth required")
				return
			}

			memberOf, err := m.authenticate(username, password)
			if err != nil {
				m.log.Debug("auth: ldap: authentication failed", "user", username, "err", err)
				writeUnauthorized(w, "invalid credentials")
				return
			}

			ctx := withPrincipal(r.Context(), &Principal{
				Name:     username,
				MemberOf: memberOf,
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// authenticate binds to LDAP with user credentials and returns the memberOf attribute.
func (m *LDAPMiddleware) authenticate(username, password string) (string, error) {
	conn, err := ldap.DialURL(m.cfg.URL)
	if err != nil {
		return "", fmt.Errorf("auth: ldap: dial %q: %w", m.cfg.URL, err)
	}
	defer conn.Close()

	// Bind as the service account to perform the user search.
	if m.cfg.BindDN != "" {
		if err := conn.Bind(m.cfg.BindDN, m.cfg.BindPass); err != nil {
			return "", fmt.Errorf("auth: ldap: service bind: %w", err)
		}
	}

	attr := m.cfg.UserAttr
	if attr == "" {
		attr = "uid"
	}

	// Search for the user's DN.
	searchReq := ldap.NewSearchRequest(
		m.cfg.UserBase,
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		1,
		0,
		false,
		fmt.Sprintf("(%s=%s)", attr, ldap.EscapeFilter(username)),
		[]string{"dn", "memberOf"},
		nil,
	)

	result, err := conn.Search(searchReq)
	if err != nil {
		return "", fmt.Errorf("auth: ldap: search: %w", err)
	}
	if len(result.Entries) == 0 {
		return "", fmt.Errorf("auth: ldap: user %q not found", username)
	}

	userDN := result.Entries[0].DN

	// Bind as the user to verify the password.
	if err := conn.Bind(userDN, password); err != nil {
		return "", fmt.Errorf("auth: ldap: user bind: %w", err)
	}

	memberOf := result.Entries[0].GetAttributeValue("memberOf")
	return memberOf, nil
}
