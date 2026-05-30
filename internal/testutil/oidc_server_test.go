package testutil_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/testutil"
)

func TestOIDCServer_IssuedTokenValidates(t *testing.T) {
	t.Parallel()

	s := testutil.NewOIDCServer(t)

	token := s.IssueToken("alice", []string{"admin"}, time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	kf, err := keyfunc.NewDefaultCtx(ctx, []string{s.JWKSURL()})
	require.NoError(t, err, "fetch JWKS")

	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, kf.Keyfunc)
	require.NoError(t, err, "parse token")
	assert.True(t, parsed.Valid)

	sub, err := claims.GetSubject()
	require.NoError(t, err)
	assert.Equal(t, "alice", sub)
}

func TestOIDCServer_RotationRejectsOldToken(t *testing.T) {
	t.Parallel()

	s := testutil.NewOIDCServer(t)

	oldToken := s.IssueToken("bob", nil, time.Minute)

	s.RotateKey()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	kf, err := keyfunc.NewDefaultCtx(ctx, []string{s.JWKSURL()})
	require.NoError(t, err, "fetch JWKS after rotation")

	_, err = jwt.Parse(oldToken, kf.Keyfunc)
	assert.Error(t, err, "old token should be rejected by post-rotation JWKS")

	newToken := s.IssueToken("bob", nil, time.Minute)
	parsed, err := jwt.Parse(newToken, kf.Keyfunc)
	require.NoError(t, err, "new token should validate after rotation")
	assert.True(t, parsed.Valid)
}

func TestOIDCServer_GroupsClaim(t *testing.T) {
	t.Parallel()

	s := testutil.NewOIDCServer(t)

	token := s.IssueToken("carol", []string{"admin", "user"}, time.Minute)

	parts := strings.Split(token, ".")
	require.Len(t, parts, 3, "JWT must have three segments")

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err, "decode payload")

	var claims map[string]any
	require.NoError(t, json.Unmarshal(payload, &claims))

	rawGroups, ok := claims["groups"].([]any)
	require.True(t, ok, `"groups" claim should be a JSON array`)
	got := make([]string, len(rawGroups))
	for i, v := range rawGroups {
		got[i], _ = v.(string)
	}
	assert.Equal(t, []string{"admin", "user"}, got)

	memberOf, ok := claims["memberOf"].(string)
	require.True(t, ok, `"memberOf" claim should be a string`)
	assert.Equal(t, "admin,user", memberOf)
}
