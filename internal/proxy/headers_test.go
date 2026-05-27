package proxy

import (
	"bytes"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hclincode/trino-goway/internal/routing"
)

// TestInjectHeaders_HTTPS verifies r.TLS != nil produces X-Forwarded-Proto: https.
func TestInjectHeaders_HTTPS(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	router := &fakeRouter{backendURL: upstream.URL}
	p := buildProxy(t, router, upstream.Client())

	inbound := httptest.NewRequest(http.MethodGet, "https://gateway.example/v1/info", nil)
	inbound.RemoteAddr = "10.0.0.5:54321"
	inbound.TLS = &tls.ConnectionState{}

	upReq := p.buildUpstreamRequest(inbound.Context(), upstream.URL, inbound, bytes.NewReader(nil))
	p.injectHeaders(upReq, inbound, &routing.RouteResult{BackendURL: upstream.URL})

	assert.Equal(t, "https", upReq.Header.Get("X-Forwarded-Proto"),
		"r.TLS != nil must produce X-Forwarded-Proto: https")
	assert.Equal(t, "10.0.0.5", upReq.Header.Get("X-Forwarded-For"))
}

// TestInjectHeaders_ExternalHeaders_Replace verifies that values in
// RouteResult.ExternalHeaders REPLACE any pre-existing header on the upstream
// request (single value, not appended).
func TestInjectHeaders_ExternalHeaders_Replace(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	router := &fakeRouter{backendURL: upstream.URL}
	p := buildProxy(t, router, upstream.Client())

	inbound := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	inbound.RemoteAddr = "127.0.0.1:1234"
	inbound.Header.Set("X-Custom", "old")

	upReq := p.buildUpstreamRequest(inbound.Context(), upstream.URL, inbound, bytes.NewReader(nil))
	// Sanity: upstream request starts with the inbound header copied through.
	assert.Equal(t, "old", upReq.Header.Get("X-Custom"))

	p.injectHeaders(upReq, inbound, &routing.RouteResult{
		BackendURL:      upstream.URL,
		ExternalHeaders: map[string]string{"X-Custom": "new"},
	})

	assert.Equal(t, []string{"new"}, upReq.Header.Values("X-Custom"),
		"externalHeaders must REPLACE, not append — exactly one value remains")
}

// TestInjectHeaders_AppendsXForwardedFor verifies that an existing
// X-Forwarded-For value is preserved and the client IP is appended.
func TestInjectHeaders_AppendsXForwardedFor(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	router := &fakeRouter{backendURL: upstream.URL}
	p := buildProxy(t, router, upstream.Client())

	inbound := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	inbound.RemoteAddr = "10.0.0.5:443"
	inbound.Header.Set("X-Forwarded-For", "203.0.113.7")

	upReq := p.buildUpstreamRequest(inbound.Context(), upstream.URL, inbound, bytes.NewReader(nil))
	p.injectHeaders(upReq, inbound, &routing.RouteResult{BackendURL: upstream.URL})

	assert.Equal(t, "203.0.113.7, 10.0.0.5", upReq.Header.Get("X-Forwarded-For"),
		"client IP must be appended to existing X-Forwarded-For")
}

// TestInjectHeaders_XForwardedHost_HostOnly verifies X-Forwarded-Host is set
// to host-only (no port), matching Java getServerName() semantics.
func TestInjectHeaders_XForwardedHost_HostOnly(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cases := []struct {
		name     string
		host     string
		wantHost string
	}{
		{"host with port", "gateway.example:8080", "gateway.example"},
		{"host only", "gateway.example", "gateway.example"},
		{"ipv6 with port", "[::1]:9000", "[::1]"},
		{"ipv6 no port", "[::1]", "[::1]"},
	}

	router := &fakeRouter{backendURL: upstream.URL}
	p := buildProxy(t, router, upstream.Client())

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inbound := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
			inbound.Host = tc.host
			inbound.RemoteAddr = "10.0.0.5:54321"

			upReq := p.buildUpstreamRequest(inbound.Context(), upstream.URL, inbound, bytes.NewReader(nil))
			p.injectHeaders(upReq, inbound, &routing.RouteResult{BackendURL: upstream.URL})

			assert.Equal(t, tc.wantHost, upReq.Header.Get("X-Forwarded-Host"),
				"X-Forwarded-Host must be host-only, no port")
		})
	}
}

// TestInjectHeaders_XForwardedPort exercises every branch of forwardedPort:
// explicit port (HTTP and HTTPS), HTTPS default (443), HTTP default (80),
// IPv6 literal with explicit port, IPv6 literal without explicit port.
// Mirrors Java ProxyRequestHandler.addForwardedHeaders → getServerPort().
func TestInjectHeaders_XForwardedPort(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cases := []struct {
		name     string
		host     string
		tls      bool
		wantPort string
	}{
		{"explicit port http", "gateway.example:8080", false, "8080"},
		{"explicit port https", "gateway.example:8443", true, "8443"},
		{"no port http -> 80", "gateway.example", false, "80"},
		{"no port https -> 443", "gateway.example", true, "443"},
		{"ipv6 with port", "[::1]:9000", false, "9000"},
		{"ipv6 no port https -> 443", "[::1]", true, "443"},
	}

	router := &fakeRouter{backendURL: upstream.URL}
	p := buildProxy(t, router, upstream.Client())

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			inbound := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
			inbound.Host = tc.host
			inbound.RemoteAddr = "10.0.0.5:54321"
			if tc.tls {
				inbound.TLS = &tls.ConnectionState{}
			}

			upReq := p.buildUpstreamRequest(inbound.Context(), upstream.URL, inbound, bytes.NewReader(nil))
			p.injectHeaders(upReq, inbound, &routing.RouteResult{BackendURL: upstream.URL})

			assert.Equal(t, tc.wantPort, upReq.Header.Get("X-Forwarded-Port"),
				"X-Forwarded-Port must reflect explicit port or scheme default")
		})
	}
}
