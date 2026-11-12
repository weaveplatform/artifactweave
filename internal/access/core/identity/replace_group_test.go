package identity_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/identity"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// fullGroup is a group with every field the create body carries set away
// from its default, in a realm other than internal, with the platform
// auditor flag that no group request sets.
func fullGroup(name string) model.Group {
	return model.Group{
		Name:            name,
		Description:     "on call",
		AutoJoin:        true,
		AdminPrivileges: true,
		Realm:           "ldap",
		RealmAttributes: "ldapGroupName=ops;groupsStrategy=STATIC;groupDn=cn=ops,ou=groups",
		ExternalID:      "ext-1",
		Members:         []string{"admin", "anonymous"},
		Roles: model.RoleFlags{
			WatchManager: true, PolicyViewer: true, PolicyManager: true, ReportsManager: true,
			ResourcesManager: true, ManageWebhook: true, PlatformAuditor: true,
		},
	}
}

func TestUnitReplaceGroupCreates(t *testing.T) {
	ctx := context.Background()
	s, db := newService(t)
	g := model.Group{
		Name: "ops", Description: "d", AdminPrivileges: true, ExternalID: "e",
		// A caller cannot pick the realm of a group it creates.
		Realm: "ldap", RealmAttributes: "x",
		Roles: model.RoleFlags{ManageWebhook: true},
	}
	created, err := s.ReplaceGroup(ctx, g, []string{"anonymous", "admin", "admin"})
	if err != nil || !created {
		t.Fatalf("create: %v, %v", created, err)
	}
	got, err := db.Groups().Get(ctx, "ops")
	want := g
	want.Realm, want.RealmAttributes = model.RealmInternal, ""
	want.Members = []string{"admin", "anonymous"}
	if err != nil || !groupsEqual(got, want) {
		t.Fatalf("created %+v, %v; want %+v", got, err, want)
	}
}

// TestUnitReplaceGroupResetsEachField replaces a group whose every field is
// set with a request that sets none, field by field: each is reset to Create
// a Group's default, the members are removed, and only what a request
// cannot carry (realm, realm attributes, platform auditor) is kept.
func TestUnitReplaceGroupResetsEachField(t *testing.T) {
	ctx := context.Background()
	s, db := newService(t)
	if err := db.Groups().Create(ctx, fullGroup("ops")); err != nil {
		t.Fatal(err)
	}
	created, err := s.ReplaceGroup(ctx, model.Group{Name: "ops"}, nil)
	if err != nil || created {
		t.Fatalf("replace: %v, %v", created, err)
	}
	got, err := db.Groups().Get(ctx, "ops")
	if err != nil {
		t.Fatal(err)
	}
	old := fullGroup("ops")
	for _, f := range []struct {
		field     string
		got, want any
	}{
		{"description", got.Description, ""},
		{"auto_join", got.AutoJoin, false},
		{"admin_privileges", got.AdminPrivileges, false},
		{"external_id", got.ExternalID, ""},
		{"watch_manager", got.Roles.WatchManager, false},
		{"policy_viewer", got.Roles.PolicyViewer, false},
		{"policy_manager", got.Roles.PolicyManager, false},
		{"reports_manager", got.Roles.ReportsManager, false},
		{"manage_resources", got.Roles.ResourcesManager, false},
		{"manage_webhook", got.Roles.ManageWebhook, false},
		{"members", len(got.Members), 0},
		{"realm", got.Realm, old.Realm},
		{"realm_attributes", got.RealmAttributes, old.RealmAttributes},
		{"platform_auditor", got.Roles.PlatformAuditor, true},
	} {
		if f.got != f.want {
			t.Errorf("%s after a replacement that left it out: %v, want %v", f.field, f.got, f.want)
		}
	}
	// A user's memberships follow the group's: nobody is in it any more.
	u, err := db.Users().Get(ctx, "admin")
	if err != nil || slices.Contains(u.Groups, "ops") {
		t.Fatalf("admin's groups after the members were removed: %v, %v", u.Groups, err)
	}
}

// TestUnitReplaceGroupSetsEachField replaces a group with a request that sets
// every field, which takes them all, and with members that replace the old
// ones rather than join them.
func TestUnitReplaceGroupSetsEachField(t *testing.T) {
	ctx := context.Background()
	s, db := newService(t)
	if _, err := s.ReplaceGroup(ctx, model.Group{Name: "ops"}, []string{"anonymous"}); err != nil {
		t.Fatal(err)
	}
	next := fullGroup("ops")
	next.Realm, next.RealmAttributes, next.Roles.PlatformAuditor = "", "", false
	if created, err := s.ReplaceGroup(ctx, next, []string{"admin"}); err != nil || created {
		t.Fatalf("replace: %v, %v", created, err)
	}
	got, err := db.Groups().Get(ctx, "ops")
	want := next
	want.Realm, want.Members = model.RealmInternal, []string{"admin"}
	if err != nil || !groupsEqual(got, want) {
		t.Fatalf("replaced %+v, %v; want %+v", got, err, want)
	}
}

// TestUnitReplaceGroupRefusals: an unknown member leaves the group, or its
// absence, as it was, and a name that cannot be stored is ErrInvalid.
func TestUnitReplaceGroupRefusals(t *testing.T) {
	ctx := context.Background()
	s, db := newService(t)
	if _, err := s.ReplaceGroup(ctx, model.Group{Name: "absent"}, []string{"nobody"}); !errors.Is(
		err, kernel.ErrNotFound,
	) {
		t.Fatalf("create with an unknown member: %v", err)
	}
	if _, err := db.Groups().Get(ctx, "absent"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("a refused create stored the group: %v", err)
	}
	if err := db.Groups().Create(ctx, fullGroup("ops")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplaceGroup(
		ctx,
		model.Group{Name: "ops"},
		[]string{"admin", "nobody"},
	); !errors.Is(
		err,
		kernel.ErrNotFound,
	) {
		t.Fatalf("replace with an unknown member: %v", err)
	}
	if got, err := db.Groups().Get(ctx, "ops"); err != nil || !groupsEqual(got, fullGroup("ops")) {
		t.Fatalf("a refused replacement changed the group: %+v, %v", got, err)
	}
	for _, name := range []string{"", " ops", "a/b"} {
		_, err := s.ReplaceGroup(ctx, model.Group{Name: name}, nil)
		wantInvalid(t, "group name "+name, err)
	}
}

// txProbe marks the context its Tx hands on, and its group store refuses a
// call without the mark, so a read or write outside the transaction fails.
type txProbe struct {
	*inmem.Store
	txs int
}

type inTx struct{}

var errOutsideTx = errors.New("outside the transaction")

func (p *txProbe) Tx(ctx context.Context, fn func(context.Context) error) error {
	p.txs++
	return p.Store.Tx(context.WithValue(ctx, inTx{}, true), fn) //nolint:wrapcheck // test double
}

func (p *txProbe) Groups() port.GroupStore { return probeGroups{p.Store.Groups()} }

type probeGroups struct{ port.GroupStore }

func (g probeGroups) Get(ctx context.Context, name string) (model.Group, error) {
	if ctx.Value(inTx{}) == nil {
		return model.Group{}, errOutsideTx
	}
	return g.GroupStore.Get(ctx, name) //nolint:wrapcheck // test double
}

func (g probeGroups) Create(ctx context.Context, x model.Group) error {
	if ctx.Value(inTx{}) == nil {
		return errOutsideTx
	}
	return g.GroupStore.Create(ctx, x) //nolint:wrapcheck // test double
}

func (g probeGroups) Update(ctx context.Context, x model.Group) error {
	if ctx.Value(inTx{}) == nil {
		return errOutsideTx
	}
	return g.GroupStore.Update(ctx, x) //nolint:wrapcheck // test double
}

// TestUnitReplaceGroupOneTransaction: the read that decides between create
// and replace and the write it decides on run in one transaction, so a
// concurrent create or delete cannot fall between them.
func TestUnitReplaceGroupOneTransaction(t *testing.T) {
	ctx := context.Background()
	_, db := newService(t)
	p := &txProbe{Store: db}
	s := &identity.Service{Store: p, Hash: cheap}
	if _, err := s.ReplaceGroup(ctx, model.Group{Name: "ops"}, nil); err != nil || p.txs != 1 {
		t.Fatalf("create: %v in %d transactions", err, p.txs)
	}
	if _, err := s.ReplaceGroup(ctx, model.Group{Name: "ops"}, nil); err != nil || p.txs != 2 {
		t.Fatalf("replace: %v in %d transactions", err, p.txs-1)
	}
	f := &faulty{Store: db, fail: "groups.get"}
	s = &identity.Service{Store: f, Hash: cheap}
	if _, err := s.ReplaceGroup(ctx, model.Group{Name: "ops"}, nil); !errors.Is(err, errBoom) {
		t.Fatalf("a failing read: %v", err)
	}
}

// groupsEqual compares two groups field by field, the members as lists.
func groupsEqual(a, b model.Group) bool {
	if !slices.Equal(a.Members, b.Members) {
		return false
	}
	a.Members, b.Members = nil, nil
	return reflect.DeepEqual(a, b)
}
