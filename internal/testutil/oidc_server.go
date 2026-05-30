package testutil

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

	s.server = httptest.NewServer(mux)
	s.URL = s.server.URL

	t.Cleanup(s.server.Close)

	return s
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

