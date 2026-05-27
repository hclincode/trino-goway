package auth

import (
	"net/http"
	"regexp"

	"github.com/hclincode/trino-goway/internal/config"
)

// Role constants mirror @RolesAllowed values in the Java gateway.
const (
	RoleAdmin = "ADMIN"
	RoleUser  = "USER"
	RoleAPI   = "API"
)

// HasRole reports whether principal holds the given role.
// The role is resolved by matching principal.MemberOf against the regex from cfg.
// Returns false if the principal is nil or if no regex is configured for the role.
func HasRole(principal *Principal, role string, cfg config.AuthorizationConfig) bool {
	if principal == nil {
		return false
	}
	var pattern string
	switch role {
	case RoleAdmin:
		pattern = cfg.AdminRegex
	case RoleUser:
		pattern = cfg.UserRegex
	case RoleAPI:
		pattern = cfg.APIRegex
	default:
		return false
	}
	if pattern == "" {
		return false
	}
	matched, err := regexp.MatchString(pattern, principal.MemberOf)
	if err != nil {
		return false
	}
	return matched
}

// RequireRole returns a middleware that rejects requests where the authenticated principal
// does not hold the given role. Must be used after an auth middleware that sets a Principal.
func RequireRole(role string, cfg config.AuthorizationConfig) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := FromContext(r.Context())
			if !HasRole(p, role, cfg) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":"forbidden"}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
