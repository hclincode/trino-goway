package testutil

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// OIDCServer is an in-process OIDC server for auth E2E tests.
// It serves a JWKS endpoint and issues RS256 JWTs signed with an in-memory RSA key.
type OIDCServer struct {
	// URL is the base URL of the OIDC server (HTTP, not HTTPS).
	URL string

	mu     sync.RWMutex
	key    *rsa.PrivateKey
	kid    string
	keyNum int
	server *httptest.Server
}

// NewOIDCServer starts an in-process OIDC server and returns it.
// The server is closed automatically when t.Cleanup runs.
func NewOIDCServer(t testing.TB) *OIDCServer {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("testutil: oidc: generate key: %v", err)
	}

	s := &OIDCServer{
		key:    key,
		kid:    "key-1",
		keyNum: 1,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jwks.json", s.handleJWKS)
	mux.HandleFunc("/.well-known/openid-configuration", s.handleDiscovery)
	mux.HandleFunc("/authorize", s.handleAuthorize)
	mux.HandleFunc("/token", s.handleToken)

	s.server = httptest.NewServer(mux)
	s.URL = s.server.URL

	t.Cleanup(s.server.Close)

	return s
}

// AuthorizeURL returns the authorization endpoint, for the gateway's
// auth.oidc.authorizationEndpoint config value in tests.
func (s *OIDCServer) AuthorizeURL() string { return s.URL + "/authorize" }

// TokenURL returns the token endpoint, for the gateway's
// auth.oidc.tokenEndpoint config value in tests.
func (s *OIDCServer) TokenURL() string { return s.URL + "/token" }

// handleDiscovery serves the minimal OIDC discovery document the gateway reads
// to resolve the authorization and token endpoints.
func (s *OIDCServer) handleDiscovery(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer":                 s.URL,
		"authorization_endpoint": s.AuthorizeURL(),
		"token_endpoint":         s.TokenURL(),
		"jwks_uri":               s.JWKSURL(),
	})
}

// handleAuthorize simulates the IdP login page auto-approving: it redirects back
// to the client's redirect_uri with a code that encodes the requested nonce, so
// the token endpoint can mint an id_token carrying that nonce.
func (s *OIDCServer) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirectURI := q.Get("redirect_uri")
	state := q.Get("state")
	nonce := q.Get("nonce")
	if redirectURI == "" {
		http.Error(w, "missing redirect_uri", http.StatusBadRequest)
		return
	}
	// Encode the nonce into the code so /token can echo it into the id_token.
	code := "code." + base64.RawURLEncoding.EncodeToString([]byte(nonce))

	u, err := neturl.Parse(redirectURI)
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	rq := u.Query()
	rq.Set("code", code)
	rq.Set("state", state)
	u.RawQuery = rq.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// handleToken exchanges the authorization code for an id_token. The nonce is
// recovered from the code and embedded in the issued token.
func (s *OIDCServer) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	code := r.PostFormValue("code")
	nonce := ""
	if rest, ok := strings.CutPrefix(code, "code."); ok {
		if raw, err := base64.RawURLEncoding.DecodeString(rest); err == nil {
			nonce = string(raw)
		}
	}

	idToken := s.IssueTokenWithClaims("web-user", []string{"admins"}, time.Hour, map[string]any{
		"nonce": nonce,
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id_token":     idToken,
		"access_token": idToken,
		"token_type":   "Bearer",
		"expires_in":   3600,
	})
}

// JWKSURL returns the URL of the JWKS endpoint.
// Use this for the gateway's auth.oidc.jwksUrl config value in tests.
func (s *OIDCServer) JWKSURL() string {
	return s.URL + "/.well-known/jwks.json"
}

// IssueToken creates and signs an RS256 JWT with the given subject, groups, and TTL.
// The groups slice is included as both the "groups" array claim and the
// "memberOf" comma-joined string claim. The "exp" claim is set to time.Now().Add(ttl).
func (s *OIDCServer) IssueToken(sub string, groups []string, ttl time.Duration) string {
	s.mu.RLock()
	key := s.key
	kid := s.kid
	s.mu.RUnlock()

	now := time.Now()
	claims := jwt.MapClaims{
		"sub":      sub,
		"iss":      s.URL,
		"iat":      now.Unix(),
		"exp":      now.Add(ttl).Unix(),
		"groups":   groups,
		"memberOf": strings.Join(groups, ","),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid

	signed, err := token.SignedString(key)
	if err != nil {
		panic("testutil: oidc: sign token: " + err.Error())
	}
	return signed
}

// IssueTokenWithClaims is like IssueToken but merges the supplied extra claims
// (e.g. "nonce") into the token before signing.
func (s *OIDCServer) IssueTokenWithClaims(sub string, groups []string, ttl time.Duration, extra map[string]any) string {
	s.mu.RLock()
	key := s.key
	kid := s.kid
	s.mu.RUnlock()

	now := time.Now()
	claims := jwt.MapClaims{
		"sub":      sub,
		"iss":      s.URL,
		"iat":      now.Unix(),
		"exp":      now.Add(ttl).Unix(),
		"groups":   groups,
		"memberOf": strings.Join(groups, ","),
	}
	for k, v := range extra {
		claims[k] = v
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid

	signed, err := token.SignedString(key)
	if err != nil {
		panic("testutil: oidc: sign token: " + err.Error())
	}
	return signed
}

// RotateKey generates a new RSA key pair and updates the JWKS endpoint.
// Tokens issued before RotateKey will be rejected by a gateway that has
// refreshed its keyfunc after the rotation.
func (s *OIDCServer) RotateKey() {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic("testutil: oidc: rotate key: " + err.Error())
	}

	s.mu.Lock()
	s.keyNum++
	s.key = key
	s.kid = "key-" + strconv.Itoa(s.keyNum)
	s.mu.Unlock()
}

func (s *OIDCServer) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	pub := &s.key.PublicKey
	kid := s.kid
	s.mu.RUnlock()

	jwk := map[string]string{
		"kty": "RSA",
		"use": "sig",
		"alg": "RS256",
		"kid": kid,
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(encodeExponent(pub.E)),
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{jwk}})
}

// encodeExponent encodes an RSA public exponent as the minimal big-endian byte sequence.
// For the common exponent 65537 this yields {0x01, 0x00, 0x01}.
func encodeExponent(e int) []byte {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(e))
	for i, b := range buf {
		if b != 0 {
			return buf[i:]
		}
	}
	return []byte{0}
}
