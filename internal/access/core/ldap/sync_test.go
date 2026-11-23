package ldap

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// syncWorld is a store with one enabled connection, corp, and a scripted
// directory.
type syncWorld struct {
	db   *inmem.Store
	svc  Service
	dir  *scripted
	sync Sync
}

func newSyncWorld(t *testing.T) syncWorld {
	t.Helper()
	db := inmem.New()
	svc := Service{
		Configs: db.Configs(), Settings: db.Settings(), Tx: db, Guard: db.Projects().Guard,
		Now: func() time.Time { return time.Unix(1, 0) },
	}
	dir := &scripted{
		answers: map[string]func(Connection, string, string) (Entry, error){},
		users:   map[string]string{}, groups: map[string][]DirectoryGroup{},
		members: map[string]map[string][]DirectoryGroup{},
	}
	w := syncWorld{db: db, svc: svc, dir: dir, sync: Sync{
		Settings: svc, Directory: dir, Users: db.Users(), Groups: db.Groups(), Tx: db.Tx,
	}}
	if _, err := svc.Create(t.Context(), searchChange("corp")); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w syncWorld) setting(t *testing.T, name string, set func(*accessapi.LDAPGroupChange)) {
	t.Helper()
	ch := accessapi.LDAPGroupChange{
		Name: name, EnabledLDAP: ptr("corp"), GroupBaseDN: ptr("ou=groups"),
		GroupMemberAttribute: ptr("uniqueMember"), Filter: ptr("(objectClass=groupOfUniqueNames)"),
		DescriptionAttribute: ptr("description"),
	}
	if set != nil {
		set(&ch)
	}
	if _, err := w.svc.CreateGroupSetting(t.Context(), ch); err != nil {
		t.Fatal(err)
	}
}

func (w syncWorld) user(t *testing.T, name, realm string, groups ...string) {
	t.Helper()
	ctx := t.Context()
	if err := w.db.Users().Create(ctx, model.User{Name: name, Realm: realm, Status: model.UserEnabled}); err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		if err := w.db.Groups().AddMembers(ctx, g, name); err != nil {
			t.Fatal(err)
		}
	}
}

func (w syncWorld) group(t *testing.T, g model.Group) {
	t.Helper()
	if err := w.db.Groups().Create(t.Context(), g); err != nil {
		t.Fatal(err)
	}
}

func (w syncWorld) groupsOf(t *testing.T, name string) []string {
	t.Helper()
	u, err := w.db.Users().Get(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	out := slices.Clone(u.Groups)
	slices.Sort(out)
	return out
}

func dg(name string) DirectoryGroup {
	return DirectoryGroup{Name: name, DN: "cn=" + name + ",ou=groups,dc=example,dc=com", Description: name + " team"}
}

func TestUnitSyncSearchStatuses(t *testing.T) {
	w := newSyncWorld(t)
	ctx := t.Context()
	w.setting(t, "corp-groups", nil)
	w.group(t, model.Group{
		Name: "devs", Realm: model.RealmLDAP,
		RealmAttributes: RealmAttributes("Devs", accessapi.LDAPStatic, "CN=Devs,OU=Groups,DC=Example,DC=Com"),
	})
	w.group(t, model.Group{Name: "ops"})
	w.dir.groups["corp-groups"] = []DirectoryGroup{dg("Devs"), dg("ops"), dg("qa")}
	g, err := w.svc.GroupSetting(ctx, "corp-groups")
	if err != nil {
		t.Fatal(err)
	}
	found, err := w.sync.Search(ctx, g, "")
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(found))
	for i, f := range found {
		got[i] = f.Name + ":" + string(f.Status)
	}
	want := []string{"Devs:IN_ARTIFACTORY", "ops:REQUIRES_UPDATE", "qa:DOES_NOT_EXIST"}
	if !slices.Equal(got, want) {
		t.Fatalf("statuses %v, want %v", got, want)
	}
	// A user's search reads that user's groups; a user no one finds is
	// 7.x's refusal.
	w.dir.users["alice"] = "uid=alice,ou=people,dc=example,dc=com"
	w.dir.members["corp-groups"] = map[string][]DirectoryGroup{"alice": {dg("qa")}}
	if found, err := w.sync.Search(ctx, g, "alice"); err != nil || len(found) != 1 || found[0].Name != "qa" {
		t.Fatalf("alice's search %+v, %v", found, err)
	}
	var unknown *UnknownUserError
	if _, err := w.sync.Search(ctx, g, "nobody"); !errors.As(err, &unknown) ||
		!errors.Is(err, kernel.ErrInvalid) || err.Error() != "Could not find DN for user: 'nobody'" {
		t.Fatalf("unknown user: %v", err)
	}
	// The setting searched with must name an enabled connection and be valid.
	other := g
	other.EnabledLDAP = "missing"
	if _, err := w.sync.Search(ctx, other, ""); !errors.Is(err, ErrNoConnection) {
		t.Fatalf("no connection: %v", err)
	}
	other = g
	other.Strategy = "NESTED"
	var invalid *accessapi.LDAPGroupInvalid
	if _, err := w.sync.Search(ctx, other, ""); !errors.As(err, &invalid) || invalid.Field != FieldGroupStrategy {
		t.Fatalf("invalid strategy: %v", err)
	}
	w.dir.groupErr = &DirectoryError{Op: "search", Reason: "down"}
	var failed *DirectoryError
	if _, err := w.sync.Search(ctx, g, ""); !errors.As(err, &failed) {
		t.Fatalf("directory failure: %v", err)
	}
	if _, err := w.sync.Search(ctx, g, "alice"); !errors.As(err, &failed) {
		t.Fatalf("directory failure for a user: %v", err)
	}
}

func TestUnitSyncImport(t *testing.T) {
	w := newSyncWorld(t)
	ctx := t.Context()
	w.group(t, model.Group{Name: "ops", Description: "local"})
	w.user(t, "erin", model.RealmInternal, "ops")
	imp := func(groups ...ImportGroup) (int, error) {
		var n int
		err := w.db.Tx(ctx, func(ctx context.Context) (err error) {
			n, err = w.sync.ImportIn(ctx, accessapi.LDAPDynamic, groups)
			return err
		})
		return n, err
	}
	n, err := imp(
		ImportGroup{Name: "Devs", DN: "cn=Devs,ou=groups", Description: "developers"},
		ImportGroup{Name: "ops", DN: "cn=ops,ou=groups", Description: "operators", Status: StatusStale},
		ImportGroup{Name: "qa", DN: "cn=qa,ou=groups", Status: StatusCurrent},
	)
	if err != nil || n != 2 {
		t.Fatalf("import wrote %d, %v", n, err)
	}
	devs, err := w.db.Groups().Get(ctx, "devs")
	if err != nil || devs.Realm != model.RealmLDAP || devs.Description != "developers" ||
		devs.RealmAttributes != "ldapGroupName=Devs;groupsStrategy=DYNAMIC;groupDn=cn=Devs,ou=groups" {
		t.Fatalf("imported %+v, %v", devs, err)
	}
	// Taking over keeps the group's members.
	ops, err := w.db.Groups().Get(ctx, "ops")
	if err != nil || ops.Realm != model.RealmLDAP || GroupDN(ops.RealmAttributes) != "cn=ops,ou=groups" ||
		ops.Description != "operators" || !slices.Equal(w.groupsOf(t, "erin"), []string{"ops"}) {
		t.Fatalf("taken over %+v, %v", ops, err)
	}
	if _, err := w.db.Groups().Get(ctx, "qa"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("IN_ARTIFACTORY created qa: %v", err)
	}
	// Each refusal refuses the whole import.
	for _, tc := range []struct {
		name  string
		group ImportGroup
		field string
	}{
		{"a taken name", ImportGroup{Name: "devs", DN: "cn=devs"}, "import_groups"},
		{"a missing group", ImportGroup{Name: "gone", DN: "cn=gone", Status: StatusStale}, "import_groups"},
		{"no name", ImportGroup{DN: "cn=x"}, "import_groups.group_name"},
		{"a path name", ImportGroup{Name: "a/b", DN: "cn=x"}, "import_groups.group_name"},
		{"no DN", ImportGroup{Name: "x"}, "import_groups.group_dn"},
		{"a bad DN", ImportGroup{Name: "x", DN: "not a dn"}, "import_groups.group_dn"},
		{"a bad status", ImportGroup{Name: "x", DN: "cn=x", Status: "MAYBE"}, "import_groups.required_update"},
	} {
		var invalid *accessapi.LDAPGroupInvalid
		_, err := imp(ImportGroup{Name: "fresh", DN: "cn=fresh"}, tc.group)
		if !errors.As(err, &invalid) || invalid.Field != tc.field {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if _, err := w.db.Groups().Get(ctx, "fresh"); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("%s: a refused import created a group: %v", tc.name, err)
		}
	}
}

func TestUnitSyncRefresh(t *testing.T) {
	w := newSyncWorld(t)
	ctx := t.Context()
	w.setting(t, "corp-groups", nil)
	w.group(t, model.Group{Name: "admins"})
	w.group(t, model.Group{Name: "old", Realm: model.RealmLDAP, RealmAttributes: "groupDn=cn=old"})
	w.group(t, model.Group{
		Name: "devs", Realm: model.RealmLDAP,
		RealmAttributes: RealmAttributes("devs", accessapi.LDAPStatic, "cn=devs,ou=old,dc=example,dc=com"),
	})
	w.user(t, "alice", model.RealmLDAP, "admins", "old")
	w.user(t, "bob", model.RealmLDAP, "devs")
	w.user(t, "erin", model.RealmInternal)
	w.dir.users["alice"] = "uid=alice"
	w.dir.groups["corp-groups"] = []DirectoryGroup{dg("devs"), dg("QA"), dg("qa"), dg("ops/x")}
	w.dir.members["corp-groups"] = map[string][]DirectoryGroup{"alice": {dg("devs"), dg("QA")}}
	sum, err := w.sync.Refresh(ctx, "corp-groups", OpUpdateAndImport, "")
	if err != nil {
		t.Fatal(err)
	}
	// qa is created once, ops/x is a name no local group can take, devs's
	// DN is refreshed, alice joins devs and qa and leaves old but keeps
	// admins, and bob, whom no connection finds, loses devs.
	if sum != (Summary{Imported: 1, Updated: 1, Users: 2}) ||
		sum.String() != "Imported 1 LDAP groups, updated 1 and changed the memberships of 2 users" {
		t.Fatalf("summary %+v", sum)
	}
	if got := w.groupsOf(t, "alice"); !slices.Equal(got, []string{"admins", "devs", "qa"}) {
		t.Fatalf("alice's groups %v", got)
	}
	if got := w.groupsOf(t, "bob"); len(got) != 0 {
		t.Fatalf("bob's groups %v", got)
	}
	devs, _ := w.db.Groups().Get(ctx, "devs")
	if GroupDN(devs.RealmAttributes) != dg("devs").DN || devs.Description != "devs team" {
		t.Fatalf("devs %+v", devs)
	}
	// Again changes nothing.
	if sum, err := w.sync.Refresh(ctx, "corp-groups", OpUpdateAndImport, ""); err != nil || sum != (Summary{}) {
		t.Fatalf("second refresh %+v, %v", sum, err)
	}
	// A user's refresh reads that user alone.
	w.dir.members["corp-groups"]["alice"] = []DirectoryGroup{dg("devs")}
	if sum, err := w.sync.Refresh(ctx, "corp-groups", OpUpdate, "alice"); err != nil || sum.Users != 1 {
		t.Fatalf("alice's refresh %+v, %v", sum, err)
	}
	if got := w.groupsOf(t, "alice"); !slices.Equal(got, []string{"admins", "devs"}) {
		t.Fatalf("alice's groups after her refresh %v", got)
	}
	// IMPORT alone touches no membership and needs a user it can find.
	w.dir.groups["corp-groups"] = append(w.dir.groups["corp-groups"], dg("new"))
	if sum, err := w.sync.Refresh(ctx, "corp-groups", OpImport, ""); err != nil || sum.Imported != 1 || sum.Users != 0 {
		t.Fatalf("import-only %+v, %v", sum, err)
	}
	var unknown *UnknownUserError
	if _, err := w.sync.Refresh(ctx, "corp-groups", OpImport, "nobody"); !errors.As(err, &unknown) {
		t.Fatalf("import for an unknown user: %v", err)
	}
	var invalid *accessapi.LDAPGroupInvalid
	if _, err := w.sync.Refresh(ctx, "corp-groups", "SOMETIMES", ""); !errors.As(err, &invalid) {
		t.Fatalf("bad operation: %v", err)
	}
	if _, err := w.sync.Refresh(ctx, "missing", OpUpdate, ""); !errors.Is(err, ErrGroupSettingNotFound) {
		t.Fatalf("missing setting: %v", err)
	}
}

// Nothing a refresh read is published when a setting changes or goes while
// the directory answers, or when the directory fails.
func TestUnitSyncRefreshPublishesNothingPartial(t *testing.T) {
	ctx := t.Context()
	for _, tc := range []struct {
		name   string
		during func(t *testing.T, w syncWorld)
		want   func(error) bool
	}{
		{"a changed setting", func(t *testing.T, w syncWorld) {
			if _, err := w.svc.ReplaceGroupSetting(ctx, accessapi.LDAPGroupChange{
				Name: "corp-groups", EnabledLDAP: ptr("corp"), GroupMemberAttribute: ptr("member"),
			}); err != nil {
				t.Fatal(err)
			}
		}, func(err error) bool { return errors.Is(err, ErrChanged) }},
		{"a deleted setting", func(t *testing.T, w syncWorld) {
			if err := w.svc.DeleteGroupSetting(ctx, "corp-groups"); err != nil {
				t.Fatal(err)
			}
		}, func(err error) bool { return errors.Is(err, ErrGroupSettingNotFound) }},
		{"a changed connection", func(t *testing.T, w syncWorld) {
			if _, err := w.svc.Replace(ctx, searchChange("corp")); err != nil {
				t.Fatal(err)
			}
		}, func(err error) bool { return errors.Is(err, ErrChanged) }},
		{"a failed directory", func(_ *testing.T, w syncWorld) {
			w.dir.groupErr = &DirectoryError{Op: "search", Reason: "down"}
		}, func(err error) bool { var d *DirectoryError; return errors.As(err, &d) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newSyncWorld(t)
			w.setting(t, "corp-groups", nil)
			w.group(t, model.Group{Name: "old", Realm: model.RealmLDAP})
			w.user(t, "alice", model.RealmLDAP, "old")
			w.dir.users["alice"] = "uid=alice"
			w.dir.groups["corp-groups"] = []DirectoryGroup{dg("devs")}
			w.dir.members["corp-groups"] = map[string][]DirectoryGroup{"alice": {dg("devs")}}
			once := false
			w.dir.during = func() {
				if !once {
					once = true
					tc.during(t, w)
				}
			}
			if _, err := w.sync.Refresh(ctx, "corp-groups", OpUpdateAndImport, ""); !tc.want(err) {
				t.Fatalf("refresh: %v", err)
			}
			if _, err := w.db.Groups().Get(ctx, "devs"); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("devs was imported: %v", err)
			}
			if got := w.groupsOf(t, "alice"); !slices.Equal(got, []string{"old"}) {
				t.Fatalf("alice's groups %v", got)
			}
		})
	}
}

func TestUnitSyncConnectionGone(t *testing.T) {
	w := newSyncWorld(t)
	w.setting(t, "corp-groups", func(ch *accessapi.LDAPGroupChange) { ch.EnabledLDAP = ptr("other") })
	if _, err := w.sync.Refresh(t.Context(), "corp-groups", OpUpdate, ""); !errors.Is(err, ErrNoConnection) {
		t.Fatalf("refresh through no connection: %v", err)
	}
}

func TestUnitPublishMemberships(t *testing.T) {
	w := newSyncWorld(t)
	ctx := t.Context()
	w.group(t, model.Group{Name: "devs", Realm: model.RealmLDAP})
	w.group(t, model.Group{Name: "local"})
	w.user(t, "alice", model.RealmLDAP, "local")
	// A group that is not imported, or is not of the ldap realm, is not
	// joined.
	changed, err := PublishMemberships(ctx, w.db.Users(), w.db.Groups(), "Alice", []string{"devs", "local", "absent"})
	if err != nil || !changed || !slices.Equal(w.groupsOf(t, "alice"), []string{"devs", "local"}) {
		t.Fatalf("published %v, %v: %v", changed, err, w.groupsOf(t, "alice"))
	}
	if changed, err := PublishMemberships(ctx, w.db.Users(), w.db.Groups(), "ghost", nil); err != nil || changed {
		t.Fatalf("a user not stored: %v, %v", changed, err)
	}
}

// A fresh login publishes the groups of every group setting of the
// connection that accepted it, and a login read under settings changed
// since publishes none.
func TestUnitLoginsPublishGroups(t *testing.T) {
	w := newSyncWorld(t)
	ctx := t.Context()
	w.setting(t, "corp-groups", nil)
	w.setting(t, "corp-units", func(ch *accessapi.LDAPGroupChange) {
		ch.Strategy, ch.GroupMemberAttribute = ptr("HIERARCHICAL"), ptr("ou")
	})
	w.setting(t, "elsewhere", func(ch *accessapi.LDAPGroupChange) { ch.EnabledLDAP = ptr("other") })
	w.group(t, model.Group{Name: "devs", Realm: model.RealmLDAP})
	w.group(t, model.Group{Name: "uk", Realm: model.RealmLDAP})
	w.group(t, model.Group{Name: "old", Realm: model.RealmLDAP})
	w.user(t, "alice", model.RealmLDAP, "old")
	w.dir.answers["corp"] = accepts("alice", "pw")
	w.dir.members["corp-groups"] = map[string][]DirectoryGroup{"alice": {dg("Devs")}}
	w.dir.members["corp-units"] = map[string][]DirectoryGroup{"alice": {{Name: "uk", DN: "ou=uk"}}}
	w.dir.members["elsewhere"] = map[string][]DirectoryGroup{"alice": {dg("old")}}
	var log bytes.Buffer
	logins := NewLogins(w.svc, w.dir, LoginOptions{
		Users: w.db.Users(), Groups: w.db.Groups(), Log: slog.New(slog.NewTextHandler(&log, nil)),
	})
	login, err := logins.Login(ctx, "alice", "pw")
	if err != nil || login.Groups == nil || !slices.Equal(login.Groups.Names, []string{"devs", "uk"}) {
		t.Fatalf("login %+v, %v", login, err)
	}
	if err := w.db.Tx(ctx, func(ctx context.Context) error {
		return logins.PublishGroups(ctx, "alice", *login.Groups)
	}); err != nil {
		t.Fatal(err)
	}
	if got := w.groupsOf(t, "alice"); !slices.Equal(got, []string{"devs", "uk"}) {
		t.Fatalf("alice's groups %v", got)
	}
	// A remembered login reads no groups.
	if again, err := logins.Login(ctx, "alice", "pw"); err != nil || again.Groups != nil {
		t.Fatalf("remembered login %+v, %v", again, err)
	}
	// Groups read under a setting replaced since are not published.
	stale := authn.DirectoryGroups{Names: []string{"old"}, Revision: login.Groups.Revision}
	if _, err := w.svc.ReplaceGroupSetting(ctx, accessapi.LDAPGroupChange{
		Name: "corp-units", EnabledLDAP: ptr("corp"), GroupMemberAttribute: ptr("o"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Tx(ctx, func(ctx context.Context) error {
		return logins.PublishGroups(ctx, "alice", stale)
	}); err != nil {
		t.Fatal(err)
	}
	if got := w.groupsOf(t, "alice"); !slices.Equal(got, []string{"devs", "uk"}) {
		t.Fatalf("stale groups were published: %v", got)
	}
	// A group lookup that fails leaves the login standing, with no groups.
	logins = NewLogins(w.svc, w.dir, LoginOptions{
		Users: w.db.Users(), Groups: w.db.Groups(), Log: slog.New(slog.NewTextHandler(&log, nil)),
	})
	w.dir.groupErr = &DirectoryError{Op: "search", Reason: "down"}
	if again, err := logins.Login(ctx, "alice", "pw"); err != nil || again.Groups != nil {
		t.Fatalf("login with a failed group lookup %+v, %v", again, err)
	}
	if !strings.Contains(log.String(), "memberships unchanged") || strings.Contains(log.String(), "pw") {
		t.Fatalf("log %q", log.String())
	}
	// Without stores a login reads and publishes no groups.
	bare := NewLogins(w.svc, w.dir, LoginOptions{})
	if err := bare.PublishGroups(ctx, "alice", authn.DirectoryGroups{}); err != nil {
		t.Fatal(err)
	}
}

// TestUnitSyncFindNamed covers Create a Group's import: the one directory
// group a name finds, under its lower-case local name and realm
// attributes, and each refusal; then PublishNamed, which stores nothing
// once the settings it was found through changed or went away.
func TestUnitSyncFindNamed(t *testing.T) {
	w := newSyncWorld(t)
	ctx := t.Context()
	w.setting(t, "corp-groups", nil)
	w.dir.groups["corp-groups"] = []DirectoryGroup{dg("DevOps"), dg("twin"), dg("TWIN"), dg("a/b")}
	n, err := w.sync.FindNamed(ctx, "corp-groups", "devops")
	if err != nil || n.Local != "devops" || n.Name != "DevOps" ||
		n.RealmAttributes != RealmAttributes("DevOps", accessapi.LDAPStatic, dg("DevOps").DN) {
		t.Fatalf("FindNamed = %+v, %v", n, err)
	}
	var invalid *accessapi.LDAPGroupInvalid
	for name, tc := range map[string]struct {
		setting, name string
		want          func(error) bool
	}{
		"no name":       {"corp-groups", " ", func(err error) bool { return errors.As(err, &invalid) }},
		"no setting":    {"absent", "devops", func(err error) bool { return errors.Is(err, ErrGroupSettingNotFound) }},
		"no such group": {"corp-groups", "qa", func(err error) bool { return errors.Is(err, ErrGroupNotInDirectory) }},
		"two groups":    {"corp-groups", "twin", func(err error) bool { return errors.As(err, &invalid) }},
		"illegal name":  {"corp-groups", "a/b", func(err error) bool { return errors.As(err, &invalid) }},
	} {
		if _, err := w.sync.FindNamed(ctx, tc.setting, tc.name); !tc.want(err) {
			t.Errorf("%s: %v", name, err)
		}
	}

	stored := func(ctx context.Context) error {
		return w.db.Groups().Create(ctx, model.Group{Name: n.Local, Realm: model.RealmLDAP})
	}
	w.setting(t, "other", nil)
	if err := w.sync.PublishNamed(ctx, n, stored); !errors.Is(err, ErrChanged) {
		t.Fatalf("a changed snapshot: %v", err)
	}
	n, _ = w.sync.FindNamed(ctx, "corp-groups", "devops")
	if err := w.svc.DeleteGroupSetting(ctx, "corp-groups"); err != nil {
		t.Fatal(err)
	}
	if err := w.sync.PublishNamed(ctx, n, stored); !errors.Is(err, ErrGroupSettingNotFound) {
		t.Fatalf("a deleted setting: %v", err)
	}
	if _, err := w.db.Groups().Get(ctx, "devops"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("a refused publication stored the group: %v", err)
	}
	w.setting(t, "corp-groups", nil)
	n, _ = w.sync.FindNamed(ctx, "corp-groups", "devops")
	if err := w.sync.PublishNamed(ctx, n, stored); err != nil {
		t.Fatal(err)
	}

	// The directory's failure and a disabled connection reach the caller.
	w.dir.groupErr = &DirectoryError{Op: "search", Reason: "down"}
	var failed *DirectoryError
	if _, err := w.sync.FindNamed(ctx, "corp-groups", "devops"); !errors.As(err, &failed) {
		t.Fatalf("a failed directory: %v", err)
	}
	off := false
	if _, err := w.svc.Apply(ctx, []accessapi.LDAPChange{{Key: "corp", Enabled: &off}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.sync.FindNamed(ctx, "corp-groups", "devops"); !errors.Is(err, ErrNoConnection) {
		t.Fatalf("a disabled connection: %v", err)
	}
	broken := w.sync
	broken.Tx = func(context.Context, func(context.Context) error) error { return errStore }
	if _, err := broken.FindNamed(ctx, "corp-groups", "devops"); !errors.Is(err, errStore) {
		t.Fatalf("a failed snapshot: %v", err)
	}
}
