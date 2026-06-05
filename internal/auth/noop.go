package auth

import "net/http"

// Noop returns a middleware that passes every request through without authentication.
// It attaches an anonymous Principal so downstream handlers can always call FromContext.
// An optional Metrics may be supplied; when present, every request is recorded as
// an allow. The variadic form keeps existing zero-argument callers compiling.
func Noop(metrics ...Metrics) Middleware {
	m := metricsFromVariadic(metrics)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := withPrincipal(r.Context(), &Principal{Name: "anonymous"})
			m.AuthRequest(TypeNoop, ResultAllow)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
