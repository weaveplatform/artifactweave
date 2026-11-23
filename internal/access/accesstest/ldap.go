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

// DirectoryGroup is a groupOfUniqueNames entry SeedDirectoryGroups writes
// under ou=groups.
type DirectoryGroup struct {
	CN, Description string
	// Members are the people whose DNs the group's uniqueMember lists, and
	// Groups the groups it lists by DN, which nest it.
	Members []DirectoryUser
	Groups  []string
}

// DN is the group's entry under suffix.
func (g DirectoryGroup) DN(suffix string) string { return "cn=" + g.CN + ",ou=groups," + suffix }

// The groups SeedDirectoryGroups writes.
var (
	// Developers holds Alice and Bob; Readers holds Bob alone.
	Developers = DirectoryGroup{CN: "Developers", Description: "the developers", Members: []DirectoryUser{Alice, Bob}}
	Readers    = DirectoryGroup{CN: "readers", Description: "read-only staff", Members: []DirectoryUser{Bob}}
	// CycleA and CycleB list each other, a nesting loop a client that
	// followed nested groups would have to survive; CycleA also lists Alice.
	CycleA = DirectoryGroup{CN: "cycle-a", Members: []DirectoryUser{Alice}, Groups: []string{"cycle-b"}}
	CycleB = DirectoryGroup{CN: "cycle-b", Groups: []string{"cycle-a"}}
)

// DirectoryGroups are every group SeedDirectoryGroups writes.
var DirectoryGroups = []DirectoryGroup{Developers, Readers, CycleA, CycleB}

// noMember is the uniqueMember of a group whose members were all removed:
// groupOfUniqueNames requires one, so directories conventionally pad it.
const noMember = "cn=nobody"

// SeedDirectoryGroups writes ou=groups and DirectoryGroups into the
// directory SeedDirectory seeded. The image's memberOf overlay keeps each
// person's memberOf in step with uniqueMember.
func SeedDirectoryGroups(t testing.TB, ep containertest.LDAPEndpoint) {
	t.Helper()
	conn := adminConn(t, ep)
	defer func() { _ = conn.Close() }()
	add := goldap.NewAddRequest("ou=groups,"+ep.BaseDN, nil)
	add.Attribute("objectClass", []string{"organizationalUnit"})
	add.Attribute("ou", []string{"groups"})
	if err := conn.Add(add); err != nil {
		t.Fatal(err)
	}
	for _, g := range DirectoryGroups {
		add := goldap.NewAddRequest(g.DN(ep.BaseDN), nil)
		add.Attribute("objectClass", []string{"groupOfUniqueNames"})
		add.Attribute("cn", []string{g.CN})
		if g.Description != "" {
			add.Attribute("description", []string{g.Description})
		}
		add.Attribute("uniqueMember", memberDNs(ep, g.Members, g.Groups))
		if err := conn.Add(add); err != nil {
			t.Fatal(err)
		}
	}
}

// SetGroupMembers replaces the people g lists with members.
func SetGroupMembers(t testing.TB, ep containertest.LDAPEndpoint, g DirectoryGroup, members ...DirectoryUser) {
	t.Helper()
	conn := adminConn(t, ep)
	defer func() { _ = conn.Close() }()
	mod := goldap.NewModifyRequest(g.DN(ep.BaseDN), nil)
	mod.Replace("uniqueMember", memberDNs(ep, members, g.Groups))
	if err := conn.Modify(mod); err != nil {
		t.Fatal(err)
	}
}

func memberDNs(ep containertest.LDAPEndpoint, members []DirectoryUser, groups []string) []string {
	out := make([]string, 0, len(members)+len(groups))
	for _, u := range members {
		out = append(out, u.DN(ep.BaseDN))
	}
	for _, g := range groups {
		out = append(out, DirectoryGroup{CN: g}.DN(ep.BaseDN))
	}
	if len(out) == 0 {
		out = []string{noMember}
	}
	return out
}

func adminConn(t testing.TB, ep containertest.LDAPEndpoint) *goldap.Conn {
	t.Helper()
	conn, err := goldap.DialURL(ep.URL(""))
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Bind(ep.AdminDN, ep.AdminPassword); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	return conn
}
