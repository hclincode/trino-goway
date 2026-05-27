package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/config"
)

// TestNoop_AttachesAnonymousPrincipal verifies the noop middleware always attaches a Principal.
func TestNoop_AttachesAnonymousPrincipal(t *testing.T) {
	var captured *Principal
	handler := Noop()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.NotNil(t, captured)
	assert.Equal(t, "anonymous", captured.Name)
}

// TestFromContext_NilWhenNotSet verifies FromContext returns nil when no Principal is attached.
func TestFromContext_NilWhenNotSet(t *testing.T) {
	p := FromContext(context.Background())
	assert.Nil(t, p)
}

// TestFromContext_RoundTrip verifies withPrincipal + FromContext works correctly.
func TestFromContext_RoundTrip(t *testing.T) {
	want := &Principal{Name: "alice", MemberOf: "cn=admins,dc=example,dc=com"}
	ctx := withPrincipal(context.Background(), want)
	got := FromContext(ctx)
	require.NotNil(t, got)
	assert.Equal(t, want.Name, got.Name)
	assert.Equal(t, want.MemberOf, got.MemberOf)
}

// TestHasRole verifies role resolution against memberOf regex patterns.
func TestHasRole(t *testing.T) {
	cfg := config.AuthorizationConfig{
		AdminRegex: `.*cn=admins.*`,
		UserRegex:  `.*cn=users.*`,
		APIRegex:   `.*cn=api.*`,
	}

	tests := []struct {
		name     string
		memberOf string
		role     string
		want     bool
	}{
		{"admin match", "cn=admins,dc=example,dc=com", RoleAdmin, true},
		{"admin no match", "cn=users,dc=example,dc=com", RoleAdmin, false},
		{"user match", "cn=users,dc=example,dc=com", RoleUser, true},
		{"api match", "cn=api,dc=example,dc=com", RoleAPI, true},
		{"unknown role", "cn=admins,dc=example,dc=com", "UNKNOWN", false},
		{"nil principal", "", RoleAdmin, false},
		{"empty regex", "cn=anything,dc=example,dc=com", RoleAdmin, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var p *Principal
			if tc.name != "nil principal" {
				p = &Principal{Name: "test", MemberOf: tc.memberOf}
			}
			cfgLocal := cfg
			if tc.name == "empty regex" {
				cfgLocal.AdminRegex = ""
			}
			got := HasRole(p, tc.role, cfgLocal)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestRequireRole_Forbidden verifies that a missing or insufficient principal yields 403.
func TestRequireRole_Forbidden(t *testing.T) {
	cfg := config.AuthorizationConfig{AdminRegex: `.*cn=admins.*`}
	handler := RequireRole(RoleAdmin, cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name     string
		memberOf string
	}{
		{"no principal in context", ""},
		{"wrong group", "cn=users,dc=example,dc=com"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.memberOf != "" {
				ctx := withPrincipal(req.Context(), &Principal{Name: "bob", MemberOf: tc.memberOf})
				req = req.WithContext(ctx)
			}
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			assert.Equal(t, http.StatusForbidden, rr.Code)
		})
	}
}

// TestRequireRole_Allowed verifies an authorized principal passes through.
func TestRequireRole_Allowed(t *testing.T) {
	cfg := config.AuthorizationConfig{AdminRegex: `.*cn=admins.*`}
	var reached bool
	handler := RequireRole(RoleAdmin, cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := withPrincipal(req.Context(), &Principal{Name: "alice", MemberOf: "cn=admins,dc=example,dc=com"})
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.True(t, reached)
}

// TestBearerToken extracts the token correctly from the Authorization header.
func TestBearerToken(t *testing.T) {
	tests := []struct {
		header string
		want   string
	}{
		{"Bearer abc123", "abc123"},
		{"Basic dXNlcjpwYXNz", ""},
		{"", ""},
		{"Bearer ", ""},
	}
	for _, tc := range tests {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if tc.header != "" {
			req.Header.Set("Authorization", tc.header)
		}
		got := bearerToken(req)
		assert.Equal(t, tc.want, got)
	}
}

// TestGroupsClaim extracts groups from JWT claims in various formats.
func TestGroupsClaim(t *testing.T) {
	tests := []struct {
		name   string
		claims map[string]interface{}
		want   string
	}{
		{"string groups", map[string]interface{}{"groups": "cn=admins"}, "cn=admins"},
		{"slice groups", map[string]interface{}{"groups": []interface{}{"cn=admins", "cn=users"}}, "cn=admins,cn=users"},
		{"memberOf key", map[string]interface{}{"memberOf": "cn=admins"}, "cn=admins"},
		{"no groups", map[string]interface{}{"sub": "alice"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := groupsClaim(tc.claims)
			assert.Equal(t, tc.want, got)
		})
	}
}
