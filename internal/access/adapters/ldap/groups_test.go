package ldap_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	coreldap "github.com/weaveplatform/artifactweave/internal/access/core/ldap"
)

func names(groups []coreldap.DirectoryGroup) []string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		out = append(out, g.Name)
	}
	slices.Sort(out)
	return out
}

func static() accessapi.LDAPGroupSetting {
	return accessapi.LDAPGroupSetting{
		Name: "corp-groups", EnabledLDAP: "corp", GroupBaseDN: "ou=groups", GroupNameAttribute: "cn",
		GroupMemberAttribute: "uniqueMember", Filter: "(objectClass=groupOfUniqueNames)",
		DescriptionAttribute: "description", Strategy: accessapi.LDAPStatic,
	}
}

// TestAccDirectoryGroups reads groups from a real OpenLDAP directory by
// each strategy: every group a setting selects, a user's groups by member
// lists (STATIC), by the user's memberOf (DYNAMIC) and by the user's DN
// (HIERARCHICAL), a nesting loop, and the refusals a group read gives.
func TestAccDirectoryGroups(t *testing.T) {
	d := newDirectory(t)
	accesstest.SeedDirectoryGroups(t, d.ep)
	ctx := t.Context()
	conn := d.connection(func(c *accessapi.LDAPConnection) { dnPattern(c); d.search(c) }, d.ep.ReaderPassword)
	alice := accesstest.Alice.DN(d.ep.BaseDN)
	bob := accesstest.Bob.DN(d.ep.BaseDN)

	// Lookup finds people by the DN pattern and by the search.
	if e, err := d.client.Lookup(ctx, conn, "alice"); err != nil || !strings.EqualFold(e.DN, alice) {
		t.Fatalf("Lookup alice %+v, %v", e, err)
	}
	if e, err := d.client.Lookup(ctx, conn, "dave"); err != nil ||
		!strings.EqualFold(e.DN, accesstest.Dave.DN(d.ep.BaseDN)) {
		t.Fatalf("Lookup dave %+v, %v", e, err)
	}
	if _, err := d.client.Lookup(ctx, conn, "nobody"); !errors.Is(err, coreldap.ErrUnknownUser) {
		t.Fatalf("Lookup nobody: %v", err)
	}
	patternOnly := d.connection(dnPattern, d.ep.ReaderPassword)
	patternOnly.ManagerDN = d.ep.ReaderDN
	if _, err := d.client.Lookup(ctx, patternOnly, "dave"); !errors.Is(err, coreldap.ErrUnknownUser) {
		t.Fatalf("Lookup outside the DN pattern without a search: %v", err)
	}

	// Every group the filter selects, with its description; the empty
	// filter selects every entry one level under the base.
	all, err := d.client.Groups(ctx, conn, static())
	if err != nil || !slices.Equal(names(all), []string{"Developers", "cycle-a", "cycle-b", "readers"}) {
		t.Fatalf("Groups %v, %v", names(all), err)
	}
	for _, g := range all {
		if g.Name == "Developers" && (g.Description != "the developers" ||
			!strings.EqualFold(g.DN, accesstest.Developers.DN(d.ep.BaseDN))) {
			t.Fatalf("Developers %+v", g)
		}
	}
	bare := static()
	bare.Filter, bare.GroupBaseDN = "objectClass=groupOfUniqueNames", ""
	if got, err := d.client.Groups(ctx, conn, bare); err != nil || len(got) != 0 {
		t.Fatalf("one level under the suffix %v, %v", names(got), err)
	}
	bare.SubTree = true
	if got, err := d.client.Groups(ctx, conn, bare); err != nil || len(got) != 4 {
		t.Fatalf("the whole subtree %v, %v", names(got), err)
	}

	// STATIC: the groups whose uniqueMember lists the user. The loop
	// between cycle-a and cycle-b is never followed.
	if got, err := d.client.UserGroups(ctx, conn, static(), "alice", alice); err != nil ||
		!slices.Equal(names(got), []string{"Developers", "cycle-a"}) {
		t.Fatalf("alice's static groups %v, %v", names(got), err)
	}
	// The Entra ID form of the member attribute, with Active Directory's
	// in-chain matching rule, is passed to the directory as written;
	// OpenLDAP knows no such rule and so finds no group, or refuses it.
	entra := static()
	entra.GroupMemberAttribute = "member:1.2.840.113556.1.4.1941:"
	var failed *coreldap.DirectoryError
	if got, err := d.client.UserGroups(ctx, conn, entra, "alice", alice); len(got) != 0 ||
		(err != nil && !errors.As(err, &failed)) {
		t.Fatalf("an unknown matching rule %v, %v", names(got), err)
	}

	// DYNAMIC: the groups the user's memberOf names, which the image's
	// overlay keeps.
	dynamic := static()
	dynamic.Strategy, dynamic.GroupMemberAttribute = accessapi.LDAPDynamic, "memberOf"
	if got, err := d.client.UserGroups(ctx, conn, dynamic, "bob", bob); err != nil ||
		!slices.Equal(names(got), []string{"Developers", "readers"}) {
		t.Fatalf("bob's dynamic groups %v, %v", names(got), err)
	}
	// A group outside the setting's base is not the user's.
	elsewhere := dynamic
	elsewhere.GroupBaseDN = "ou=people"
	if got, err := d.client.UserGroups(ctx, conn, elsewhere, "bob", bob); err != nil || len(got) != 0 {
		t.Fatalf("groups outside the base %v, %v", names(got), err)
	}
	// A user value that names a group rather than a DN is searched by name:
	// cn holds "alice", so the people one level under ou=people, read as
	// groups, give her one.
	byName := dynamic
	byName.GroupMemberAttribute, byName.GroupBaseDN, byName.Filter = "cn", "ou=people", "(objectClass=inetOrgPerson)"
	if got, err := d.client.UserGroups(ctx, conn, byName, "alice", alice); err != nil ||
		!slices.Equal(names(got), []string{"alice"}) {
		t.Fatalf("groups by name %v, %v", names(got), err)
	}
	if got, err := d.client.UserGroups(ctx, conn, dynamic, "ghost", "uid=ghost,ou=people,"+d.ep.BaseDN); err != nil ||
		len(got) != 0 {
		t.Fatalf("a user entry that is gone %v, %v", names(got), err)
	}

	// HIERARCHICAL: the ou RDNs of the user's DN below the URL's base.
	hier := accessapi.LDAPGroupSetting{
		Name: "units", EnabledLDAP: "corp", GroupNameAttribute: "ou", GroupMemberAttribute: "ou",
		Strategy: accessapi.LDAPHierarchical,
	}
	got, err := d.client.UserGroups(ctx, conn, hier, "dave", accesstest.Dave.DN(d.ep.BaseDN))
	if err != nil || len(got) != 1 || got[0].Name != "contractors" ||
		!strings.EqualFold(got[0].DN, "ou=contractors,"+d.ep.BaseDN) {
		t.Fatalf("dave's units %+v, %v", got, err)
	}
	if got, err := d.client.UserGroups(ctx, conn, hier, "x", "not a dn"); err != nil || len(got) != 0 {
		t.Fatalf("a malformed DN %v, %v", got, err)
	}
	units, err := d.client.Groups(ctx, conn, accessapi.LDAPGroupSetting{
		Name: "units", GroupNameAttribute: "description", Filter: "(objectClass=organizationalUnit)",
		Strategy: accessapi.LDAPHierarchical,
	})
	if err != nil || !slices.Equal(names(units), []string{"contractors", "groups", "people"}) {
		t.Fatalf("units named by their RDN %v, %v", names(units), err)
	}
	// GroupsNamed leaves the comparison to the directory, whose cn rule
	// ignores case; a wildcard in the name is a literal, and a unit without
	// the name attribute is found by its RDN.
	if got, err := d.client.GroupsNamed(ctx, conn, static(), "DEVELOPERS"); err != nil ||
		!slices.Equal(names(got), []string{"Developers"}) {
		t.Fatalf("GroupsNamed DEVELOPERS %v, %v", names(got), err)
	}
	if got, err := d.client.GroupsNamed(ctx, conn, static(), "Dev*"); err != nil || len(got) != 0 {
		t.Fatalf("GroupsNamed Dev* %v, %v", names(got), err)
	}
	if got, err := d.client.GroupsNamed(ctx, conn, accessapi.LDAPGroupSetting{
		Name: "units", GroupNameAttribute: "description", GroupMemberAttribute: "ou",
		Filter: "(objectClass=organizationalUnit)", Strategy: accessapi.LDAPHierarchical,
	}, "Contractors"); err != nil || !slices.Equal(names(got), []string{"contractors"}) {
		t.Fatalf("GroupsNamed of a unit by its RDN %v, %v", names(got), err)
	}
	// No member attribute: no groups.
	none := static()
	none.GroupMemberAttribute = ""
	if got, err := d.client.UserGroups(ctx, conn, none, "alice", alice); err != nil || got != nil {
		t.Fatalf("no member attribute %v, %v", got, err)
	}

	// Refusals: a filter the directory refuses, a base that does not exist,
	// and a directory nothing answers.
	broken := static()
	broken.Filter = "(objectClass~~x)"
	if _, err := d.client.Groups(ctx, conn, broken); !errors.As(err, &failed) {
		t.Fatalf("a refused filter: %v", err)
	}
	missing := static()
	missing.GroupBaseDN = "ou=missing"
	if got, err := d.client.Groups(ctx, conn, missing); err != nil || got != nil {
		t.Fatalf("a missing base %v, %v", got, err)
	}
	down := conn
	down.URL = "ldap://127.0.0.1:1/" + d.ep.BaseDN
	for name, call := range map[string]func() error{
		"Lookup": func() error { _, err := d.client.Lookup(ctx, down, "alice"); return err },
		"Groups": func() error { _, err := d.client.Groups(ctx, down, static()); return err },
		"GroupsNamed": func() error {
			_, err := d.client.GroupsNamed(ctx, down, static(), "readers")
			return err
		},
		"UserGroups": func() error { _, err := d.client.UserGroups(ctx, down, static(), "alice", alice); return err },
		"Dynamic":    func() error { _, err := d.client.UserGroups(ctx, down, dynamic, "alice", alice); return err },
	} {
		if err := call(); !errors.As(err, &failed) {
			t.Fatalf("%s of an unreachable directory: %v", name, err)
		}
	}
	bad := conn
	bad.URL = "http://nope"
	if _, err := d.client.UserGroups(ctx, bad, hier, "dave", "uid=dave"); err == nil {
		t.Fatal("a hierarchy read through a bad URL")
	}
}
