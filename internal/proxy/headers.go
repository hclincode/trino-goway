package proxy

import (
	"net/http"
	"strings"

	"github.com/hclincode/trino-goway/internal/routing"
)

// hopByHopHeaders is the set of headers that must not be forwarded upstream.
var hopByHopHeaders = map[string]bool{
	"Connection":          true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailers":            true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

// isHopByHop reports whether the given header name is a hop-by-hop header.
func isHopByHop(name string) bool {
	return hopByHopHeaders[http.CanonicalHeaderKey(name)]
}

// copyHeaders copies all headers from src to dst, skipping hop-by-hop headers.
func copyHeaders(dst, src http.Header) {
	for k, vv := range src {
		if !isHopByHop(k) {
			dst[k] = vv
		}
	}
}

// injectHeaders sets X-Forwarded-* headers and applies externalHeaders on the upstream request.
func (p *Proxy) injectHeaders(upReq *http.Request, r *http.Request, result *routing.RouteResult) {
	// X-Forwarded-For: append client IP to any existing value.
	clientIP := r.RemoteAddr
	if i := strings.LastIndex(clientIP, ":"); i != -1 {
		clientIP = clientIP[:i]
	}
	if existing := upReq.Header.Get("X-Forwarded-For"); existing != "" {
		upReq.Header.Set("X-Forwarded-For", existing+", "+clientIP)
	} else {
		upReq.Header.Set("X-Forwarded-For", clientIP)
	}

	// X-Forwarded-Proto: scheme of the inbound request.
	proto := "http"
	if r.TLS != nil {
		proto = "https"
	}
	upReq.Header.Set("X-Forwarded-Proto", proto)

	// X-Forwarded-Host: original Host header, host-only (no port).
	// Java uses getServerName() which never includes the port; we match that.
	upReq.Header.Set("X-Forwarded-Host", hostOnly(r.Host))

	// X-Forwarded-Port: explicit port from r.Host, or scheme default (80/443).
	// Mirrors Java ProxyRequestHandler.addForwardedHeaders → servletRequest.getServerPort().
	upReq.Header.Set("X-Forwarded-Port", forwardedPort(r.Host, proto))

	// externalHeaders REPLACE semantics: set each key, overwriting any existing value.
	for k, v := range result.ExternalHeaders {
		upReq.Header.Set(k, v)
	}
}

// hostOnly strips the port from a host header value.
// Handles IPv6 literals: "[::1]:8080" → "[::1]".
func hostOnly(hostHeader string) string {
	if hostHeader != "" {
		if hostHeader[0] == '[' {
			if end := strings.IndexByte(hostHeader, ']'); end >= 0 {
				return hostHeader[:end+1]
			}
		} else if i := strings.LastIndexByte(hostHeader, ':'); i >= 0 {
			return hostHeader[:i]
		}
	}
	return hostHeader
}

// forwardedPort returns the value for X-Forwarded-Port given the inbound Host header
// and resolved scheme. Returns the explicit port if present (handling IPv6 literals),
// otherwise the scheme default ("443" for https, "80" otherwise).
func forwardedPort(hostHeader, proto string) string {
	if hostHeader != "" {
		// IPv6 literal: "[addr]:port"
		if hostHeader[0] == '[' {
			if end := strings.IndexByte(hostHeader, ']'); end >= 0 {
				if end+1 < len(hostHeader) && hostHeader[end+1] == ':' {
					return hostHeader[end+2:]
				}
			}
		} else if i := strings.LastIndexByte(hostHeader, ':'); i >= 0 {
			return hostHeader[i+1:]
		}
	}
	if proto == "https" {
		return "443"
	}
	return "80"
}
