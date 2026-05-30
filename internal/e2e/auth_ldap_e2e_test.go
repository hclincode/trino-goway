//go:build e2e

package e2e_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/e2e/harness"
	"github.com/hclincode/trino-goway/internal/testutil"
)

const (
	ldapAliceDN       = "uid=alice,ou=users,dc=example,dc=com"
	ldapAlicePassword = "alice-pw"
	ldapBobDN         = "uid=bob,ou=users,dc=example,dc=com"
	ldapBobPassword   = "bob-pw"
)

// TestE2E_LDAP_ValidCredentials_Admitted verifies that HTTP Basic auth with a
// seeded user/password is admitted to a USER-protected admin endpoint.
func TestE2E_LDAP_ValidCredentials_Admitted(t *testing.T) {
	ldap := testutil.NewLDAPServer(t, []testutil.LDAPUser{
		{
			DN:       ldapAliceDN,
			Password: ldapAlicePassword,
			MemberOf: []string{"cn=admins,dc=example,dc=com"},
		},
	})

	h := harness.New(t,
		harness.WithAuthLDAP("ldap://"+ldap.Addr(), ldap.BindDN(), ldap.BindPassword(), ldap.UserBase()),
		harness.WithAdminRoleRegex("cn=admins,.*"),
	)

	resp := doBasicAuth(t, h.AdminURL+"/trino-gateway/api/queryHistory", "alice", ldapAlicePassword)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestE2E_LDAP_InvalidCredentials_401 verifies that a wrong password is
// rejected with 401.
func TestE2E_LDAP_InvalidCredentials_401(t *testing.T) {
	ldap := testutil.NewLDAPServer(t, []testutil.LDAPUser{
		{
			DN:       ldapAliceDN,
			Password: ldapAlicePassword,
			MemberOf: []string{"cn=admins,dc=example,dc=com"},
		},
	})

	h := harness.New(t,
		harness.WithAuthLDAP("ldap://"+ldap.Addr(), ldap.BindDN(), ldap.BindPassword(), ldap.UserBase()),
		harness.WithAdminRoleRegex("cn=admins,.*"),
	)

	t.Run("wrong password", func(t *testing.T) {
		resp := doBasicAuth(t, h.AdminURL+"/trino-gateway/api/queryHistory", "alice", "wrongpassword")
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("missing basic auth", func(t *testing.T) {
		resp := doAuthorized(t, h.AdminURL+"/trino-gateway/api/queryHistory", "")
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

// TestE2E_LDAP_MemberOfMapsToRole verifies that the user's memberOf attribute
// is matched against auth.authorization.admin to grant ADMIN role.
func TestE2E_LDAP_MemberOfMapsToRole(t *testing.T) {
	ldap := testutil.NewLDAPServer(t, []testutil.LDAPUser{
		{
			DN:       ldapAliceDN,
			Password: ldapAlicePassword,
			MemberOf: []string{"cn=platform-admins,dc=example,dc=com"},
		},
		{
			DN:       ldapBobDN,
			Password: ldapBobPassword,
			MemberOf: nil,
		},
	})

	h := harness.New(t,
		harness.WithAuthLDAP("ldap://"+ldap.Addr(), ldap.BindDN(), ldap.BindPassword(), ldap.UserBase()),
		harness.WithAdminRoleRegex("cn=platform-admins,.*"),
	)

	saveBackendBody := `{"name":"ldap-test","proxyTo":"http://fake:9999","active":true,"routingGroup":"default"}`

	t.Run("admin memberOf passes ADMIN gate", func(t *testing.T) {
		resp := postBasicAuth(t, h.AdminURL+"/webapp/saveBackend", "alice", ldapAlicePassword, saveBackendBody)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("no memberOf fails ADMIN gate", func(t *testing.T) {
		resp := postBasicAuth(t, h.AdminURL+"/webapp/saveBackend", "bob", ldapBobPassword, saveBackendBody)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})
}

// TestE2E_LDAP_MissingUrl_StartupFails verifies that the gateway refuses to
// start when auth.type=LDAP but no ldap.url is provided.
func TestE2E_LDAP_MissingUrl_StartupFails(t *testing.T) {
	cfg := `proxy:
  port: %d
admin:
  port: %d
db:
  driver: postgres
  dsn: "%s"
routing:
  defaultGroup: default
  type: EXTERNAL
auth:
  type: LDAP
  ldap:
    userBase: "ou=users,dc=example,dc=com"
`
	err := runBinaryWithBadConfig(t, cfg)
	require.Error(t, err, "binary must exit non-zero when auth.ldap.url is missing")
}

// TestE2E_LDAP_MissingUserBase_StartupFails verifies that the gateway refuses
// to start when auth.type=LDAP and ldap.url is set but ldap.userBase is missing.
func TestE2E_LDAP_MissingUserBase_StartupFails(t *testing.T) {
	cfg := `proxy:
  port: %d
admin:
  port: %d
db:
  driver: postgres
  dsn: "%s"
routing:
  defaultGroup: default
  type: EXTERNAL
auth:
  type: LDAP
  ldap:
    url: "ldap://127.0.0.1:389"
`
	err := runBinaryWithBadConfig(t, cfg)
	require.Error(t, err, "binary must exit non-zero when auth.ldap.userBase is missing")
}

// doBasicAuth issues a GET with HTTP Basic credentials and returns the response.
func doBasicAuth(t *testing.T, url, user, pass string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	req.SetBasicAuth(user, pass)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}

// postBasicAuth issues a POST with a JSON body and HTTP Basic credentials.
func postBasicAuth(t *testing.T, url, user, pass, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(user, pass)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}
