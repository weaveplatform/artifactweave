package identity_test

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/identity"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// imported is fullGroup as an LDAP import of the same directory group
// would send it: every request field reset, no members.
func imported(name string) model.Group {
	full := fullGroup(name)
	return model.Group{
		Name: name, Description: "from the directory", Realm: full.Realm,
		RealmAttributes: full.RealmAttributes,
	}
}

// TestUnitImportGroup covers Create a Group's and Replace or Create a
// Group's LDAP import: a new group is created as given without members; a
// create over a taken name is ErrExists; a replace takes over only a group
// imported from the same LDAP group, whose members and platform auditor
// flag it keeps, and refuses any other as ErrInvalid without changing it.
func TestUnitImportGroup(t *testing.T) {
	ctx := context.Background()
	s, db := newService(t)
	g := imported("ops")
	g.Members = []string{"admin"}
	created, err := s.ImportGroup(ctx, g, false)
	if err != nil || !created {
		t.Fatalf("create: %v, %v", created, err)
	}
	if got, err := db.Groups().Get(ctx, "ops"); err != nil || !groupsEqual(got, imported("ops")) {
		t.Fatalf("created %+v, %v", got, err)
	}
	if _, err := s.ImportGroup(ctx, imported("ops"), false); !errors.Is(err, identity.ErrExists) {
		t.Fatalf("a second create: %v", err)
	}

	if err := db.Groups().Create(ctx, fullGroup("team")); err != nil {
		t.Fatal(err)
	}
	created, err = s.ImportGroup(ctx, imported("team"), true)
	if err != nil || created {
		t.Fatalf("replace: %v, %v", created, err)
	}
	want := imported("team")
	want.Members, want.Roles.PlatformAuditor = fullGroup("team").Members, true
	if got, err := db.Groups().Get(ctx, "team"); err != nil || !groupsEqual(got, want) {
		t.Fatalf("replaced %+v, %v; want %+v", got, err, want)
	}

	for name, other := range map[string]model.Group{
		"another LDAP group": {Name: "team", Realm: model.RealmLDAP, RealmAttributes: "ldapGroupName=x"},
		"an internal group":  {Name: "team", Realm: model.RealmInternal},
	} {
		if _, err := s.ImportGroup(ctx, other, true); !errors.Is(err, identity.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got, err := db.Groups().Get(ctx, "team"); err != nil || !groupsEqual(got, want) {
		t.Fatalf("a refused import changed the group: %+v, %v", got, err)
	}
	_, err = s.ImportGroup(ctx, model.Group{Name: "a/b"}, true)
	wantInvalid(t, "an invalid name", err)
}

// raced finds no group by name, as a read that a concurrent create has
// just overtaken.
type raced struct{ *inmem.Store }

func (r raced) Groups() port.GroupStore { return racedGroups{r.Store.Groups()} }

type racedGroups struct{ port.GroupStore }

func (racedGroups) Get(context.Context, string) (model.Group, error) {
	return model.Group{}, kernel.ErrNotFound
}

// TestUnitImportGroupStoreFailures: a create that loses a race is ErrExists,
// as CreateGroup answers it, and a failed read reaches the caller.
func TestUnitImportGroupStoreFailures(t *testing.T) {
	ctx := context.Background()
	_, db := newService(t)
	if err := db.Groups().Create(ctx, imported("ops")); err != nil {
		t.Fatal(err)
	}
	s := &identity.Service{Store: raced{db}, Hash: cheap}
	if _, err := s.ImportGroup(ctx, imported("ops"), true); !errors.Is(err, identity.ErrExists) {
		t.Fatalf("a lost race: %v", err)
	}
	s = &identity.Service{Store: &faulty{Store: db, fail: "groups.get"}, Hash: cheap}
	if _, err := s.ImportGroup(ctx, imported("ops"), true); !errors.Is(err, errBoom) {
		t.Fatalf("a failed read: %v", err)
	}
}
