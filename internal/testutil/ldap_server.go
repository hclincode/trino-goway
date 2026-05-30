package testutil

import (
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/nmcclain/ldap"
)

// LDAPUser describes a single user entry to seed into the mock LDAP server.
type LDAPUser struct {
	// DN is the full distinguished name, e.g. "uid=alice,ou=users,dc=example,dc=com".
	DN string
	// Password is the simple-bind password for this user.
	Password string
	// MemberOf is the list of group DNs returned as the "memberOf" attribute.
	MemberOf []string
}

// LDAPServer is an in-process LDAP server used by auth E2E tests. It speaks just enough
// LDAP to satisfy the gateway flow in internal/auth/ldap.go: a service-account bind, a
// user search, and a user bind.
type LDAPServer struct {
	server   *ldap.Server
	listener net.Listener
	addr     string
	userBase string
	bindDN   string
	bindPass string

	stopOnce sync.Once
	doneCh   chan struct{}
}

const (
	ldapDefaultUserBase = "ou=users,dc=example,dc=com"
	ldapDefaultBindDN   = "cn=service,dc=example,dc=com"
	ldapDefaultBindPass = "service-secret"
)

// NewLDAPServer starts an in-process LDAP server seeded with the given users. The server
// listens on a random free port on 127.0.0.1 and is shut down via t.Cleanup.
func NewLDAPServer(t testing.TB, users []LDAPUser) *LDAPServer {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("testutil: ldap: listen: %v", err)
	}

	s := &LDAPServer{
		listener: ln,
		addr:     ln.Addr().String(),
		userBase: ldapDefaultUserBase,
		bindDN:   ldapDefaultBindDN,
		bindPass: ldapDefaultBindPass,
		doneCh:   make(chan struct{}),
	}

	h := &ldapHandler{
		bindDN:   s.bindDN,
		bindPass: s.bindPass,
		users:    append([]LDAPUser(nil), users...),
	}

	srv := ldap.NewServer()
	srv.EnforceLDAP = true
	srv.QuitChannel(make(chan bool))
	srv.BindFunc("", h)
	srv.SearchFunc("", h)
	s.server = srv

	// goroutine exits when the listener is closed by Stop via t.Cleanup
	go func() {
		defer close(s.doneCh)
		_ = srv.Serve(ln)
	}()

	t.Cleanup(s.stop)

	return s
}

// Addr returns "host:port" for the LDAP server. Use in gateway config as
// auth.ldap.url = "ldap://" + s.Addr().
func (s *LDAPServer) Addr() string { return s.addr }

// BindDN returns the service-account DN seeded into the server.
func (s *LDAPServer) BindDN() string { return s.bindDN }

// BindPassword returns the service-account password seeded into the server.
func (s *LDAPServer) BindPassword() string { return s.bindPass }

// UserBase returns the user-search base DN. Use in gateway config as
// auth.ldap.userBase = s.UserBase().
func (s *LDAPServer) UserBase() string { return s.userBase }

func (s *LDAPServer) stop() {
	s.stopOnce.Do(func() {
		// Closing the listener returns from Accept; Serve exits.
		_ = s.listener.Close()
		// Drain the Quit channel as well — Serve selects on it after Accept errors.
		select {
		case s.server.Quit <- true:
		default:
		}
		<-s.doneCh
	})
}

// ldapHandler implements ldap.Binder and ldap.Searcher backed by an in-memory user list.
type ldapHandler struct {
	bindDN   string
	bindPass string
	users    []LDAPUser
}

// Bind succeeds for the seeded service account, for any seeded user whose password
// matches, and for the RFC 4513 anonymous bind (empty DN + empty password). Everything
// else returns InvalidCredentials.
func (h *ldapHandler) Bind(bindDN, bindPW string, _ net.Conn) (ldap.LDAPResultCode, error) {
	if bindDN == "" && bindPW == "" {
		return ldap.LDAPResultSuccess, nil
	}
	if strings.EqualFold(bindDN, h.bindDN) && bindPW == h.bindPass {
		return ldap.LDAPResultSuccess, nil
	}
	for _, u := range h.users {
		if strings.EqualFold(u.DN, bindDN) && u.Password == bindPW {
			return ldap.LDAPResultSuccess, nil
		}
	}
	return ldap.LDAPResultInvalidCredentials, nil
}

// Search returns every seeded user as an Entry. The server-side filter applied by
// nmcclain/ldap (EnforceLDAP=true) narrows the result to the entry whose attributes
// match the request filter — typically "(uid=<name>)" from internal/auth/ldap.go.
func (h *ldapHandler) Search(_ string, _ ldap.SearchRequest, _ net.Conn) (ldap.ServerSearchResult, error) {
	entries := make([]*ldap.Entry, 0, len(h.users))
	for _, u := range h.users {
		uid := uidFromDN(u.DN)
		attrs := []*ldap.EntryAttribute{
			{Name: "dn", Values: []string{u.DN}},
			{Name: "uid", Values: []string{uid}},
			{Name: "cn", Values: []string{uid}},
		}
		if len(u.MemberOf) > 0 {
			attrs = append(attrs, &ldap.EntryAttribute{
				Name:   "memberOf",
				Values: append([]string(nil), u.MemberOf...),
			})
		}
		entries = append(entries, &ldap.Entry{DN: u.DN, Attributes: attrs})
	}
	return ldap.ServerSearchResult{
		Entries:    entries,
		Referrals:  []string{},
		Controls:   []ldap.Control{},
		ResultCode: ldap.LDAPResultSuccess,
	}, nil
}

// uidFromDN extracts the value of the first RDN if it is uid= or cn=. Returns "" if the
// DN does not start with a recognized attribute.
func uidFromDN(dn string) string {
	first, _, _ := strings.Cut(dn, ",")
	first = strings.TrimSpace(first)
	for _, prefix := range []string{"uid=", "cn=", "UID=", "CN="} {
		if strings.HasPrefix(first, prefix) {
			return first[len(prefix):]
		}
	}
	return ""
}

// compile-time check that the handler satisfies both server interfaces
var (
	_ ldap.Binder   = (*ldapHandler)(nil)
	_ ldap.Searcher = (*ldapHandler)(nil)
)
