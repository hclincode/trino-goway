package testutil_test

import (
	"testing"

	"github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/testutil"
)

func newSeededLDAP(t *testing.T) *testutil.LDAPServer {
	t.Helper()
	return testutil.NewLDAPServer(t, []testutil.LDAPUser{
		{
			DN:       "uid=alice,ou=users,dc=example,dc=com",
			Password: "alice-secret",
			MemberOf: []string{"cn=admins,dc=example,dc=com", "cn=engineers,dc=example,dc=com"},
		},
		{
			DN:       "uid=bob,ou=users,dc=example,dc=com",
			Password: "bob-secret",
			MemberOf: []string{"cn=users,dc=example,dc=com"},
		},
	})
}

func dialLDAP(t *testing.T, s *testutil.LDAPServer) *ldap.Conn {
	t.Helper()
	conn, err := ldap.DialURL("ldap://" + s.Addr())
	require.NoError(t, err, "dial LDAP")
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestLDAPServer_ValidBind(t *testing.T) {
	t.Parallel()

	s := newSeededLDAP(t)

	tests := []struct {
		name     string
		dn       string
		password string
	}{
		{
			name:     "service account bind",
			dn:       s.BindDN(),
			password: s.BindPassword(),
		},
		{
			name:     "user bind alice",
			dn:       "uid=alice,ou=users,dc=example,dc=com",
			password: "alice-secret",
		},
		{
			name:     "user bind bob",
			dn:       "uid=bob,ou=users,dc=example,dc=com",
			password: "bob-secret",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			conn := dialLDAP(t, s)
			assert.NoError(t, conn.Bind(tc.dn, tc.password))
		})
	}
}

func TestLDAPServer_InvalidBind(t *testing.T) {
	t.Parallel()

	s := newSeededLDAP(t)

	tests := []struct {
		name     string
		dn       string
		password string
	}{
		{
			name:     "service account wrong password",
			dn:       s.BindDN(),
			password: "wrong",
		},
		{
			name:     "user wrong password",
			dn:       "uid=alice,ou=users,dc=example,dc=com",
			password: "not-alice-secret",
		},
		{
			name:     "unknown user",
			dn:       "uid=ghost,ou=users,dc=example,dc=com",
			password: "anything",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			conn := dialLDAP(t, s)
			err := conn.Bind(tc.dn, tc.password)
			require.Error(t, err)
			assert.True(t,
				ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials),
				"expected InvalidCredentials, got %v", err,
			)
		})
	}
}

func TestLDAPServer_MemberOf(t *testing.T) {
	t.Parallel()

	s := newSeededLDAP(t)
	conn := dialLDAP(t, s)

	// Service account bind first — mirrors what internal/auth/ldap.go does.
	require.NoError(t, conn.Bind(s.BindDN(), s.BindPassword()))

	req := ldap.NewSearchRequest(
		s.UserBase(),
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		0,
		0,
		false,
		"(uid=alice)",
		[]string{"dn", "memberOf"},
		nil,
	)

	res, err := conn.Search(req)
	require.NoError(t, err)
	require.Len(t, res.Entries, 1, "expected exactly one match for uid=alice")

	entry := res.Entries[0]
	assert.Equal(t, "uid=alice,ou=users,dc=example,dc=com", entry.DN)

	memberOf := entry.GetAttributeValues("memberOf")
	assert.ElementsMatch(t,
		[]string{"cn=admins,dc=example,dc=com", "cn=engineers,dc=example,dc=com"},
		memberOf,
	)
}

func TestLDAPServer_FullAuthFlow(t *testing.T) {
	// Exercises the exact sequence performed by internal/auth/ldap.go: service-account
	// bind, search by uid, then user-DN bind with the supplied password.
	t.Parallel()

	s := newSeededLDAP(t)
	conn := dialLDAP(t, s)

	require.NoError(t, conn.Bind(s.BindDN(), s.BindPassword()))

	req := ldap.NewSearchRequest(
		s.UserBase(),
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		1,
		0,
		false,
		"(uid=bob)",
		[]string{"dn", "memberOf"},
		nil,
	)
	res, err := conn.Search(req)
	require.NoError(t, err)
	require.Len(t, res.Entries, 1)

	userDN := res.Entries[0].DN
	assert.NoError(t, conn.Bind(userDN, "bob-secret"))
}
