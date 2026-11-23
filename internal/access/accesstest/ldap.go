package accesstest

import (
	"testing"

	goldap "github.com/go-ldap/ldap/v3"

	"github.com/weaveplatform/artifactweave/pkg/containertest"
)

// DirectoryUser is a person SeedDirectory writes.
type DirectoryUser struct {
	// UID is the login name and the entry's uid.
	UID string
	// OU is the organisational unit under the suffix that holds the entry.
	OU string
	// Email is the entry's mail, or "" for none.
	Email string
	// Password is the entry's userPassword, or "" for an entry with none,
	// which can never bind: a directory account that is disabled.
	Password string
}

// DN is the user's entry under suffix.
func (u DirectoryUser) DN(suffix string) string {
	return "uid=" + goldap.EscapeDN(u.UID) + ",ou=" + u.OU + "," + suffix
}

// The people SeedDirectory writes, under ou=people unless named otherwise.
var (
	// Alice and Bob have mail and passwords.
	Alice = DirectoryUser{UID: "alice", OU: "people", Email: "alice@example.org", Password: "alice-directory-pw"}
	Bob   = DirectoryUser{UID: "bob", OU: "people", Email: "bob@example.org", Password: "bob-directory-pw"}
	// Carol has no password, so the directory refuses every bind as her.
	Carol = DirectoryUser{UID: "carol", OU: "people", Email: "carol@example.org"}
	// Dave is under ou=contractors, which only a second search base finds,
	// and has no mail.
	Dave = DirectoryUser{UID: "dave", OU: "contractors", Password: "dave-directory-pw"}
	// Erin's uid holds the characters a filter and a DN must escape.
	Erin = DirectoryUser{UID: "erin*(x),y", OU: "people", Email: "erin@example.org", Password: "erin-directory-pw"}
)

// DirectoryUsers are every person SeedDirectory writes.
var DirectoryUsers = []DirectoryUser{Alice, Bob, Carol, Dave, Erin}

// SeedDirectory writes ou=people and ou=contractors and DirectoryUsers into
// the directory at ep, binding as its administrator, and fails t if it
// cannot. Each test that seeds starts its own directory, so the entries
// never exist already.
func SeedDirectory(t testing.TB, ep containertest.LDAPEndpoint) {
	t.Helper()
	conn, err := goldap.DialURL(ep.URL(""))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.Bind(ep.AdminDN, ep.AdminPassword); err != nil {
		t.Fatal(err)
	}
	for _, ou := range []string{"people", "contractors"} {
		add := goldap.NewAddRequest("ou="+ou+","+ep.BaseDN, nil)
		add.Attribute("objectClass", []string{"organizationalUnit"})
		add.Attribute("ou", []string{ou})
		if err := conn.Add(add); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range DirectoryUsers {
		add := goldap.NewAddRequest(u.DN(ep.BaseDN), nil)
		add.Attribute("objectClass", []string{"inetOrgPerson"})
		add.Attribute("uid", []string{u.UID})
		add.Attribute("cn", []string{u.UID})
		add.Attribute("sn", []string{u.UID})
		if u.Email != "" {
			add.Attribute("mail", []string{u.Email})
		}
		if u.Password != "" {
			add.Attribute("userPassword", []string{u.Password})
		}
		if err := conn.Add(add); err != nil {
			t.Fatal(err)
		}
	}
}
