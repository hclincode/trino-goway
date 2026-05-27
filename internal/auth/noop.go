package auth

import "net/http"

// Noop returns a middleware that passes every request through without authentication.
// It attaches an anonymous Principal so downstream handlers can always call FromContext.
func Noop() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := withPrincipal(r.Context(), &Principal{Name: "anonymous"})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
