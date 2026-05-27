package proxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const cookieName = "TG.OAUTH2"

// UnsignedGatewayCookie is the payload that is HMAC-signed.
// Fields are in alphabetical order by JSON tag — encoding/json preserves field declaration order,
// so alphabetical order here guarantees a stable JSON serialization for HMAC input.
// NO omitempty: null-valued pointer fields must serialize as null for wire-compat with Java.
type UnsignedGatewayCookie struct {
	Backend      *string  `json:"backend"`
	DeletePaths  []string `json:"deletePaths"`
	Name         string   `json:"name"`
	Payload      *string  `json:"payload"`
	Priority     int      `json:"priority"`
	RoutingPaths []string `json:"routingPaths"`
	Ts           int64    `json:"ts"`
	Ttl          string   `json:"ttl"`
}

// GatewayCookie is the signed cookie that is base64-encoded and sent to the client.
// Fields are in alphabetical order by JSON tag.
// NO omitempty: null-valued pointer fields must serialize as null for wire-compat with Java.
type GatewayCookie struct {
	Backend      *string  `json:"backend"`
	DeletePaths  []string `json:"deletePaths"`
	Name         string   `json:"name"`
	Payload      *string  `json:"payload"`
	Priority     int      `json:"priority"`
	RoutingPaths []string `json:"routingPaths"`
	Signature    string   `json:"signature"`
	Ts           int64    `json:"ts"`
	Ttl          string   `json:"ttl"`
}

// applyCookies implements the TG.OAUTH2 cookie lifecycle on every response.
// Returns false if a tampered cookie was detected — caller must respond 500
// per gateway-cookies-and-sticky-routing study (HMAC failure throws).
func (p *Proxy) applyCookies(w http.ResponseWriter, r *http.Request, backendURL string) bool {
	if p.cfg.Cookie.Secret == "" {
		return true
	}

	existing, err := r.Cookie(cookieName)
	if err == nil {
		valid, deletePaths, tampered := p.validateCookie(existing.Value)
		if tampered {
			return false
		}
		if !valid {
			deleteCookie(w)
			return true
		}
		if matchesAny(r.URL.Path, deletePaths) {
			deleteCookie(w)
			return true
		}
		return true
	}

	if strings.HasPrefix(r.URL.Path, "/oauth2") {
		if err := p.issueCookie(w, backendURL); err != nil {
			p.log.Error("proxy: cookie: issue failed", "err", err)
		}
	}
	return true
}

// issueCookie signs and sets a new TG.OAUTH2 cookie on the response.
// deletePaths and routingPaths match the Java OAuth2 cookie defaults
// (gateway-cookies-and-sticky-routing study).
func (p *Proxy) issueCookie(w http.ResponseWriter, backendURL string) error {
	ttl := p.cfg.Cookie.TTL.D
	now := time.Now().UnixMilli()
	backend := backendURL

	unsigned := UnsignedGatewayCookie{
		Backend:      &backend,
		DeletePaths:  []string{"/logout", "/oauth2/logout"},
		Name:         cookieName,
		Payload:      nil,
		Priority:     0,
		RoutingPaths: []string{"/oauth2", "/logout", "/oauth2/logout"},
		Ts:           now,
		Ttl:          p.formatTTL(ttl),
	}

	sig, err := p.sign(unsigned)
	if err != nil {
		return fmt.Errorf("proxy: cookie: sign: %w", err)
	}

	gc := GatewayCookie{
		Backend:      unsigned.Backend,
		DeletePaths:  unsigned.DeletePaths,
		Name:         unsigned.Name,
		Payload:      unsigned.Payload,
		Priority:     unsigned.Priority,
		RoutingPaths: unsigned.RoutingPaths,
		Signature:    sig,
		Ts:           unsigned.Ts,
		Ttl:          unsigned.Ttl,
	}

	val, err := p.encodeCookie(gc)
	if err != nil {
		return fmt.Errorf("proxy: cookie: encode: %w", err)
	}

	maxAge := int(ttl.Milliseconds() / 1000)
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    val,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// validateCookie decodes, verifies the HMAC, and checks TTL expiry.
// Returns (valid, deletePaths, tampered).
// tampered=true means HMAC mismatch — caller should respond 500.
// tampered=false with valid=false means expired or malformed encoding — caller should delete.
func (p *Proxy) validateCookie(value string) (bool, []string, bool) {
	raw, err := p.decodeCookie(value)
	if err != nil {
		return false, nil, true
	}

	var gc GatewayCookie
	if err := json.Unmarshal(raw, &gc); err != nil {
		return false, nil, true
	}

	unsigned := UnsignedGatewayCookie{
		Backend:      gc.Backend,
		DeletePaths:  gc.DeletePaths,
		Name:         gc.Name,
		Payload:      gc.Payload,
		Priority:     gc.Priority,
		RoutingPaths: gc.RoutingPaths,
		Ts:           gc.Ts,
		Ttl:          gc.Ttl,
	}
	expected, err := p.sign(unsigned)
	if err != nil || !hmac.Equal([]byte(expected), []byte(gc.Signature)) {
		return false, nil, true
	}

	ttlDur := p.parseTTL(gc.Ttl)
	if ttlDur > 0 {
		issuedAt := time.UnixMilli(gc.Ts)
		if time.Since(issuedAt) > ttlDur {
			return false, nil, false
		}
	}

	return true, gc.DeletePaths, false
}

// sign computes HMAC-SHA256 over the JSON-serialized unsigned cookie.
func (p *Proxy) sign(u UnsignedGatewayCookie) (string, error) {
	payload, err := json.Marshal(u)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(p.cfg.Cookie.Secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// encodeCookie base64-encodes the JSON-marshaled GatewayCookie.
// wireCompat true → base64.URLEncoding (with padding).
// wireCompat false → base64.RawURLEncoding (no padding).
func (p *Proxy) encodeCookie(gc GatewayCookie) (string, error) {
	data, err := json.Marshal(gc)
	if err != nil {
		return "", err
	}
	if p.cfg.Cookie.WireCompat {
		return base64.URLEncoding.EncodeToString(data), nil
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

// decodeCookie reverses encodeCookie.
func (p *Proxy) decodeCookie(value string) ([]byte, error) {
	if p.cfg.Cookie.WireCompat {
		return base64.URLEncoding.DecodeString(value)
	}
	return base64.RawURLEncoding.DecodeString(value)
}

// deleteCookie sets a Set-Cookie header that instructs the browser to delete TG.OAUTH2.
// MaxAge: -1 makes Go emit "Max-Age=0" (MaxAge: 0 omits the directive entirely).
func deleteCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:   cookieName,
		Value:  "delete",
		Path:   "/",
		MaxAge: -1,
	})
}

// matchesAny reports whether path exactly matches any entry in paths.
func matchesAny(path string, paths []string) bool {
	for _, p := range paths {
		if p == path {
			return true
		}
	}
	return false
}

// formatTTL formats a duration as an airlift Duration string (wireCompat true)
// or as nanoseconds (wireCompat false).
func (p *Proxy) formatTTL(d time.Duration) string {
	if p.cfg.Cookie.WireCompat {
		return airliftDurationString(d)
	}
	return fmt.Sprintf("%d", d.Nanoseconds())
}

// parseTTL parses a TTL string back into a duration.
// Handles both airlift format ("10.00m") and nanosecond integers.
func (p *Proxy) parseTTL(s string) time.Duration {
	// Try nanosecond integer first.
	var ns int64
	if _, err := fmt.Sscanf(s, "%d", &ns); err == nil && !strings.ContainsAny(s, "dhms") {
		return time.Duration(ns)
	}
	// Try standard Go duration.
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	// Try airlift suffixes: "10.00m", "1.00h", etc.
	units := []struct {
		suffix string
		mult   time.Duration
	}{
		{"d", 24 * time.Hour},
		{"h", time.Hour},
		{"ms", time.Millisecond},
		{"us", time.Microsecond},
		{"ns", time.Nanosecond},
		{"m", time.Minute},
		{"s", time.Second},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			numStr := strings.TrimSuffix(s, u.suffix)
			var f float64
			if _, err := fmt.Sscanf(numStr, "%f", &f); err == nil {
				return time.Duration(f * float64(u.mult))
			}
		}
	}
	return 0
}

// airliftDurationString formats a duration using the airlift Duration string representation.
// This matches the Java GatewayDuration wire format.
func airliftDurationString(d time.Duration) string {
	units := []struct {
		div  float64
		name string
	}{
		{float64(24 * time.Hour), "d"},
		{float64(time.Hour), "h"},
		{float64(time.Minute), "m"},
		{float64(time.Second), "s"},
		{float64(time.Millisecond), "ms"},
		{float64(time.Microsecond), "us"},
		{1.0, "ns"},
	}
	nanos := float64(d.Nanoseconds())
	for _, u := range units {
		if nanos/u.div >= 1.0 {
			return fmt.Sprintf("%.2f%s", nanos/u.div, u.name)
		}
	}
	return fmt.Sprintf("%.2fns", nanos)
}
