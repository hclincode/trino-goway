package auth

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hclincode/trino-goway/internal/config"
)

// fakeAuthMetrics captures auth metric calls.
type fakeAuthMetrics struct {
	mu       sync.Mutex
	requests []string // "type:result"
	refresh  []string
	keys     int
}

func (f *fakeAuthMetrics) AuthRequest(authType, result string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, authType+":"+result)
}
func (f *fakeAuthMetrics) JWKSRefresh(result string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refresh = append(f.refresh, result)
}
func (f *fakeAuthMetrics) JWKSKeys(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = n
}

func TestNoop_RecordsAllow(t *testing.T) {
	m := &fakeAuthMetrics{}
	handler := Noop(m)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	assert.Equal(t, []string{TypeNoop + ":" + ResultAllow}, m.requests)
}

func TestNoop_NilMetricsIsNoOp(t *testing.T) {
	// Zero-argument form must still work (no metrics, no panic).
	handler := Noop()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestLDAP_RecordsDenyOnMissingBasicAuth(t *testing.T) {
	m := &fakeAuthMetrics{}
	mw := NewLDAP(config.LDAPConfig{URL: "ldap://example:389", UserBase: "ou=users"}, quietLogger(), m)
	handler := mw.Handler()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Equal(t, []string{TypeLDAP + ":" + ResultDeny}, m.requests)
}

func TestOIDC_RecordsDenyOnMissingBearer(t *testing.T) {
	m := &fakeAuthMetrics{}
	// Construct middleware directly without a JWKS fetch: an OIDCMiddleware with a
	// nil jwks pointer denies, which exercises the deny-metric path.
	mw := &OIDCMiddleware{log: quietLogger(), metrics: m, done: make(chan struct{})}
	handler := mw.Handler()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Equal(t, []string{TypeOIDC + ":" + ResultDeny}, m.requests)
}
