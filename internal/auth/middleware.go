package auth

import (
	"context"
	"net/http"
)

// Middleware is a chi-compatible HTTP middleware function.
type Middleware = func(http.Handler) http.Handler

// Principal holds the authenticated identity for the current request.
type Principal struct {
	Name     string // username / JWT subject
	MemberOf string // LDAP memberOf attribute or comma-joined JWT groups claim
}

type contextKey struct{}

// FromContext returns the Principal attached to ctx, or nil if none was set.
func FromContext(ctx context.Context) *Principal {
	p, _ := ctx.Value(contextKey{}).(*Principal)
	return p
}

// withPrincipal returns a new context with p attached.
func withPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, p)
}

// writeUnauthorized writes a 401 JSON error response.
func writeUnauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", "Bearer")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
}
