package auth

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"

	"github.com/hclincode/trino-goway/internal/config"
)

// OIDCMiddleware validates Bearer JWTs using a JWKS endpoint.
// JWKS is fetched once at construction and refreshed in the background every JWKSTTLSecs seconds.
type OIDCMiddleware struct {
	cfg    config.OIDCConfig
	log    *slog.Logger
	jwks   atomic.Pointer[keyfunc.Keyfunc]
	cancel context.CancelFunc
	done   chan struct{}
}

// NewOIDC creates an OIDCMiddleware, fetches the initial JWKS, and starts the background refresher.
// The returned middleware is ready to use immediately.
func NewOIDC(ctx context.Context, cfg config.OIDCConfig, log *slog.Logger) (*OIDCMiddleware, error) {
	m := &OIDCMiddleware{
		cfg:  cfg,
		log:  log,
		done: make(chan struct{}),
	}

	if err := m.refresh(ctx); err != nil {
		return nil, fmt.Errorf("auth: oidc: initial JWKS fetch: %w", err)
	}

	refreshCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel

	ttl := time.Duration(cfg.JWKSTTLSecs) * time.Second
	if ttl == 0 {
		ttl = 5 * time.Minute
	}

	// goroutine exits when refreshCtx is cancelled
	go func() {
		defer close(m.done)
		ticker := time.NewTicker(ttl)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := m.refresh(refreshCtx); err != nil {
					m.log.Warn("auth: oidc: JWKS refresh failed", "err", err)
				}
			case <-refreshCtx.Done():
				return
			}
		}
	}()

	return m, nil
}

// Stop cancels the background JWKS refresher and waits for it to exit.
func (m *OIDCMiddleware) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	<-m.done
}

// Handler returns a chi-compatible middleware that validates the Bearer JWT on each request.
func (m *OIDCMiddleware) Handler() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := bearerToken(r)
			if raw == "" {
				writeUnauthorized(w, "missing Bearer token")
				return
			}

			kfp := m.jwks.Load()
			if kfp == nil {
				writeUnauthorized(w, "JWKS not available")
				return
			}
			kf := *kfp

			claims := jwt.MapClaims{}
			_, err := jwt.ParseWithClaims(raw, claims, kf.Keyfunc)
			if err != nil {
				m.log.Debug("auth: oidc: JWT validation failed", "err", err)
				writeUnauthorized(w, "invalid token")
				return
			}

			sub, _ := claims.GetSubject()
			memberOf := groupsClaim(claims)

			ctx := withPrincipal(r.Context(), &Principal{
				Name:     sub,
				MemberOf: memberOf,
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func (m *OIDCMiddleware) refresh(ctx context.Context) error {
	k, err := keyfunc.NewDefaultCtx(ctx, []string{m.cfg.JWKSURL})
	if err != nil {
		return err
	}
	// keyfunc.NewDefaultCtx swallows the first HTTP failure (NoErrorReturnFirstHTTPReq=true).
	// Verify the JWKS actually loaded keys; otherwise treat it as a fetch failure so the
	// previously-stored keyfunc (if any) is preserved.
	keys, err := k.Storage().KeyReadAll(ctx)
	if err != nil {
		return fmt.Errorf("read keys: %w", err)
	}
	if len(keys) == 0 {
		return fmt.Errorf("JWKS %q returned no keys", m.cfg.JWKSURL)
	}
	m.jwks.Store(&k)
	return nil
}

// bearerToken extracts the token from "Authorization: Bearer <token>".
func bearerToken(r *http.Request) string {
	v := r.Header.Get("Authorization")
	if !strings.HasPrefix(v, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(v, "Bearer ")
}

// groupsClaim extracts groups from the "groups" or "memberOf" JWT claim as a comma-joined string.
func groupsClaim(claims jwt.MapClaims) string {
	for _, key := range []string{"groups", "memberOf"} {
		raw, ok := claims[key]
		if !ok {
			continue
		}
		switch v := raw.(type) {
		case string:
			return v
		case []interface{}:
			parts := make([]string, 0, len(v))
			for _, item := range v {
				if s, ok := item.(string); ok {
					parts = append(parts, s)
				}
			}
			return strings.Join(parts, ",")
		}
	}
	return ""
}
