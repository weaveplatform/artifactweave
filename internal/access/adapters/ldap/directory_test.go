package ldap_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	goldap "github.com/go-ldap/ldap/v3"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessldap "github.com/weaveplatform/artifactweave/internal/access/adapters/ldap"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	coreldap "github.com/weaveplatform/artifactweave/internal/access/core/ldap"
	"github.com/weaveplatform/artifactweave/pkg/containertest"
)

// directory is one seeded OpenLDAP container and a client that trusts its
// CA, shared by the subtests of one test.
type directory struct {
	ep     containertest.LDAPEndpoint
	client accessldap.Client
}

func newDirectory(t *testing.T) directory {
	t.Helper()
	ep := containertest.OpenLDAP(t)
	accesstest.SeedDirectory(t, ep)
	return directory{ep: ep, client: accessldap.Client{RootCAs: ep.CA}}
}

// connection is a connection to d with the defaults and set applied.
func (d directory) connection(set func(*accessapi.LDAPConnection), password string) coreldap.Connection {
	c := coreldap.Defaults("corp")
	c.URL = d.ep.URL(d.ep.BaseDN)
	set(&c)
	return coreldap.Connection{LDAPConnection: c, ManagerPassword: coreldap.SecretOf(password)}
}

func dnPattern(c *accessapi.LDAPConnection) { c.UserDNPattern = "uid={0},ou=people" }

func (d directory) search(c *accessapi.LDAPConnection) {
	c.SearchFilter, c.SearchBase, c.ManagerDN = "(uid={0})", "ou=people|ou=contractors", d.ep.ReaderDN
}

func TestAccDirectoryAuthenticate(t *testing.T) {
	d := newDirectory(t)
	ctx := t.Context()
	both := func(c *accessapi.LDAPConnection) { dnPattern(c); d.search(c) }
	for _, tc := range []struct {
		name     string
		set      func(*accessapi.LDAPConnection)
		password string
		user     accesstest.DirectoryUser
		login    string
		want     error
		email    string
	}{
		{name: "dn pattern", set: dnPattern, user: accesstest.Alice, email: accesstest.Alice.Email},
		{
			name: "dn pattern in any case", set: dnPattern, user: accesstest.Alice, login: "ALICE",
			email: accesstest.Alice.Email,
		},
		{
			name: "dn pattern wrong password", set: dnPattern, user: accesstest.Alice, password: "wrong",
			want: coreldap.ErrRefused,
		},
		{name: "dn pattern unknown user", set: dnPattern, login: "nobody", password: "pw", want: coreldap.ErrRefused},
		{
			name: "dn pattern disabled account", set: dnPattern, user: accesstest.Carol, password: "pw",
			want: coreldap.ErrRefused,
		},
		{name: "dn pattern escapes the name", set: dnPattern, user: accesstest.Erin, email: accesstest.Erin.Email},
		{name: "search", set: d.search, user: accesstest.Bob, email: accesstest.Bob.Email},
		{name: "search second base", set: d.search, user: accesstest.Dave},
		{
			name: "search wrong password", set: d.search, user: accesstest.Bob, password: "wrong",
			want: coreldap.ErrRefused,
		},
		{name: "search unknown user", set: d.search, login: "nobody", password: "pw", want: coreldap.ErrUnknownUser},
		{name: "search escapes the filter", set: d.search, user: accesstest.Erin, email: accesstest.Erin.Email},
		{
			name: "search wildcard is no match", set: d.search, login: "*", password: "pw",
			want: coreldap.ErrUnknownUser,
		},
		{
			name: "search disabled account", set: d.search, user: accesstest.Carol, password: "pw",
			want: coreldap.ErrRefused,
		},
		{name: "dn pattern refused then search", set: func(c *accessapi.LDAPConnection) {
			both(c)
			c.UserDNPattern = "uid={0},ou=nowhere"
		}, user: accesstest.Dave},
		{name: "search without paging, one level", set: func(c *accessapi.LDAPConnection) {
			d.search(c)
			c.PagingSupportEnabled, c.SearchSubTree = false, false
		}, user: accesstest.Bob, email: accesstest.Bob.Email},
		{name: "empty password", set: dnPattern, user: accesstest.Alice, password: "-", want: coreldap.ErrRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			login, password := tc.login, tc.password
			if login == "" {
				login = tc.user.UID
			}
			switch password {
			case "":
				password = tc.user.Password
			case "-":
				password = ""
			}
			conn := d.connection(tc.set, d.ep.ReaderPassword)
			entry, err := d.client.Authenticate(ctx, conn, login, password)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Authenticate(%q) = %+v, %v; want %v", login, entry, err, tc.want)
			}
			if tc.want == nil && (entry.Email != tc.email || !sameDN(t, entry.DN, tc.user.DN(d.ep.BaseDN))) {
				t.Fatalf("entry %+v, want %s with %q", entry, tc.user.DN(d.ep.BaseDN), tc.email)
			}
		})
	}
}

// sameDN compares DNs as the directory does: the server may escape a
// character another way than the client did.
func sameDN(t *testing.T, a, b string) bool {
	t.Helper()
	x, err := goldap.ParseDN(a)
	if err != nil {
		t.Fatal(err)
	}
	y, err := goldap.ParseDN(b)
	if err != nil {
		t.Fatal(err)
	}
	return x.EqualFold(y)
}

func TestAccDirectoryTLS(t *testing.T) {
	d := newDirectory(t)
	ctx := t.Context()
	conn := d.connection(d.search, d.ep.ReaderPassword)
	conn.URL = d.ep.TLSURL(d.ep.BaseDN)
	entry, err := d.client.Authenticate(ctx, conn, accesstest.Bob.UID, accesstest.Bob.Password)
	if err != nil || entry.Email != accesstest.Bob.Email {
		t.Fatalf("ldaps login = %+v, %v", entry, err)
	}
	if err := d.client.Test(ctx, conn); err != nil {
		t.Fatalf("ldaps test: %v", err)
	}
	// The system's trust has never heard of the container's CA.
	var failed *coreldap.DirectoryError
	_, err = accessldap.Client{}.Authenticate(ctx, conn, accesstest.Bob.UID, accesstest.Bob.Password)
	if !errors.As(err, &failed) || failed.Op != "tls" || !strings.Contains(failed.Reason, "certificate") {
		t.Fatalf("untrusted ldaps login: %v", err)
	}
	if err := (accessldap.Client{}).Test(ctx, conn); !errors.As(err, &failed) || failed.Op != "tls" {
		t.Fatalf("untrusted ldaps test: %v", err)
	}
	// A plain client speaking to the TLS port sees no LDAP answer.
	plain := conn
	plain.URL = strings.Replace(conn.URL, "ldaps://", "ldap://", 1)
	short := accessldap.Client{Timeout: 2 * time.Second}
	if _, err := short.Authenticate(ctx, plain, accesstest.Bob.UID, accesstest.Bob.Password); !errors.As(err, &failed) {
		t.Fatalf("plain LDAP to the TLS port: %v", err)
	}
}

func TestAccDirectoryTest(t *testing.T) {
	d := newDirectory(t)
	ctx := t.Context()
	var failed *coreldap.DirectoryError
	for _, tc := range []struct {
		name     string
		set      func(*accessapi.LDAPConnection)
		password string
		op       string
		reason   string
	}{
		{name: "search with the manager", set: d.search, password: d.ep.ReaderPassword},
		{name: "dn pattern connects", set: dnPattern},
		{
			name: "wrong manager password", set: d.search, password: "wrong", op: "manager bind",
			reason: "refused search.manager_dn",
		},
		{name: "manager without password", set: d.search, op: "manager bind", reason: "no search.manager_password"},
		{name: "missing search base", set: func(c *accessapi.LDAPConnection) {
			d.search(c)
			c.SearchBase = "ou=missing"
		}, password: d.ep.ReaderPassword, op: "search", reason: `"ou=missing,dc=example,dc=org" does not exist`},
		{name: "unreachable", set: func(c *accessapi.LDAPConnection) {
			dnPattern(c)
			c.URL = "ldap://127.0.0.1:1/" + d.ep.BaseDN
		}, op: "connect", reason: "could not connect to 127.0.0.1:1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := d.client.Test(ctx, d.connection(tc.set, tc.password))
			if tc.op == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.As(err, &failed) || failed.Op != tc.op || !strings.Contains(failed.Reason, tc.reason) {
				t.Fatalf("Test = %v, want %s: ...%s", err, tc.op, tc.reason)
			}
			if strings.Contains(err.Error(), d.ep.ReaderPassword) {
				t.Fatalf("the error shows the manager password: %v", err)
			}
		})
	}
}

func TestAccDirectoryCancellation(t *testing.T) {
	d := newDirectory(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var failed *coreldap.DirectoryError
	_, err := d.client.Authenticate(ctx, d.connection(d.search, d.ep.ReaderPassword),
		accesstest.Bob.UID, accesstest.Bob.Password)
	if !errors.As(err, &failed) || !strings.Contains(failed.Reason, "context canceled") {
		t.Fatalf("cancelled login: %v", err)
	}
}
