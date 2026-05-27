package auth

import "net/http"

// NewTestMiddleware returns a Middleware that attaches the given Principal to every request.
// Intended for use in tests that need to exercise role-based authorization with a specific
// principal identity. Production code should use Noop, NewOIDC, NewLDAP, etc.
func NewTestMiddleware(p *Principal) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := withPrincipal(r.Context(), p)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
