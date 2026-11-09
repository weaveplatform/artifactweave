package storetest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/core/model"
	"github.com/weaveplatform/artifactweave/migration/access/core/port"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/testkit/storekit"
)

// runIdentity covers users, groups, memberships, permissions, tokens and key
// material.
func runIdentity(t *testing.T, newStore Factory) {
	t.Run("IdentityPages", func(t *testing.T) { testIdentityPages(t, newStore(t)) })
	t.Run("Users", func(t *testing.T) { testUsers(t, newStore(t)) })
	t.Run("UserLockout", func(t *testing.T) { testUserLockout(t, newStore(t)) })
	t.Run("Groups", func(t *testing.T) { testGroups(t, newStore(t)) })
	t.Run("Memberships", func(t *testing.T) { testMemberships(t, newStore(t)) })
	t.Run("Permissions", func(t *testing.T) { testPermissions(t, newStore(t)) })
	t.Run("Tokens", func(t *testing.T) { testTokens(t, newStore(t)) })
	t.Run("Keypairs", func(t *testing.T) { testKeypairs(t, newStore(t)) })
	t.Run("Certificates", func(t *testing.T) { testCertificates(t, newStore(t)) })
	t.Run("TrustedKeys", func(t *testing.T) { testTrustedKeys(t, newStore(t)) })
	t.Run("SigningKeys", func(t *testing.T) { testSigningKeys(t, newStore(t)) })
}

func user(i int, name string, groups ...string) model.User {
	return model.User{
		Name:             name,
		Email:            name + "@example.com",
		PasswordHash:     fmt.Appendf(nil, "$argon2id$v=19$m=65536,t=3,p=4$%s", name),
		Admin:            i%2 == 0,
		ProfileUpdatable: true,
		Realm:            model.RealmInternal,
		Status:           model.UserEnabled,
		MFA:              model.MFANone,
		Groups:           groups,
		Roles:            model.RoleFlags{WatchManager: true, ManageWebhook: i%2 == 1},
		Created:          storekit.At(i),
	}
}

func group(name string, autoJoin bool, members ...string) model.Group {
	return model.Group{
		Name:            name,
		Description:     "the " + name + " group",
		AutoJoin:        autoJoin,
		AdminPrivileges: name == "admins",
		Realm:           model.RealmInternal,
		RealmAttributes: "dn=" + name,
		ExternalID:      "ext-" + name,
		Members:         members,
		Roles:           model.RoleFlags{PolicyViewer: true},
	}
}

func names[V any](vs []V, name func(V) string) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = name(v)
	}
	return out
}

func userNames(us []model.User) []string {
	return names(us, func(u model.User) string { return u.Name })
}

func groupNames(gs []model.Group) []string {
	return names(gs, func(g model.Group) string { return g.Name })
}

func permissionNames(ps []model.Permission) []string {
	return names(ps, func(p model.Permission) string { return p.Name })
}

func testUsers(t *testing.T, s port.Store) {
	ctx := context.Background()
	us := s.Users()
	if got, next, err := us.List(ctx, kernel.Page{}); err != nil || len(got) != 0 || next != "" {
		t.Fatalf("List on an empty store = %v, %q, %v", got, next, err)
	}
	storekit.Must(t, s.Groups().Create(ctx, group("g1", false)))
	storekit.Must(t, s.Groups().Create(ctx, group("g2", false)))

	// A missing group fails the whole create.
	storekit.WantErr(t, us.Create(ctx, user(1, "alice", "g1", "nope")), kernel.ErrNotFound)
	_, err := us.Get(ctx, "alice")
	storekit.WantErr(t, err, kernel.ErrNotFound)

	alice := user(1, "alice", "g2", "g1", "g2")
	storekit.Must(t, us.Create(ctx, alice))
	wantAlice := alice
	wantAlice.Groups = []string{"g1", "g2"}
	got := storekit.Get[model.User](t)(us.Get(ctx, "alice"))
	storekit.Same(t, "Get", wantAlice, got)
	if got.Created.Location() != time.UTC {
		t.Fatalf("Created location = %v", got.Created.Location())
	}
	storekit.WantErr(t, us.Create(ctx, user(9, "alice")), kernel.ErrConflict)

	// Returned values are copies.
	got.Groups[0] = "changed"
	got.PasswordHash[0] = 'X'
	storekit.Same(
		t,
		"Get after mutating a result",
		wantAlice,
		storekit.Get[model.User](t)(us.Get(ctx, "alice")),
	)

	// Update replaces fields and memberships; a missing group changes nothing.
	alice.Email, alice.Groups, alice.Status = "a@example.org", []string{"g2"}, model.UserLocked
	alice.PasswordHash = nil
	alice.FailedLogins, alice.PasswordChanged, alice.PasswordExpired = 2, storekit.At(3), true
	storekit.Must(t, us.Update(ctx, alice))
	storekit.Same(t, "Update", alice, storekit.Get[model.User](t)(us.Get(ctx, "alice")))
	bad := alice
	bad.Email, bad.Groups = "bad", []string{"nope"}
	storekit.WantErr(t, us.Update(ctx, bad), kernel.ErrNotFound)
	storekit.Same(t, "failed Update", alice, storekit.Get[model.User](t)(us.Get(ctx, "alice")))
	storekit.WantErr(t, us.Update(ctx, user(1, "nobody")), kernel.ErrNotFound)

	storekit.Must(t, us.RecordLogin(ctx, "alice", storekit.At(5)))
	alice.LastLoggedIn, alice.FailedLogins = storekit.At(5), 0
	storekit.Same(t, "RecordLogin", alice, storekit.Get[model.User](t)(us.Get(ctx, "alice")))
	storekit.WantErr(t, us.RecordLogin(ctx, "nobody", storekit.At(5)), kernel.ErrNotFound)

	for i, n := range []string{"erin", "bob", "dave", "carol"} {
		storekit.Must(t, us.Create(ctx, user(i+2, n, "g1")))
	}
	all := storekit.Pages(
		t,
		2,
		func(p kernel.Page) ([]model.User, string, error) { return us.List(ctx, p) },
	)
	storekit.Same(
		t,
		"List order",
		[]string{"alice", "bob", "carol", "dave", "erin"},
		userNames(all),
	)
	storekit.Same(t, "List memberships", []string{"g2"}, all[0].Groups)
	storekit.Same(t, "List memberships", []string{"g1"}, all[1].Groups)
	storekit.Same(t, "List record", alice, all[0])
	for _, bad := range []string{"%%%", kernel.Cursor("a", "b")} {
		_, _, err = us.List(ctx, kernel.Page{After: bad})
		storekit.WantErr(t, err, kernel.ErrInvalidCursor)
	}

	storekit.Must(t, us.Delete(ctx, "bob"))
	_, err = us.Get(ctx, "bob")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.WantErr(t, us.Delete(ctx, "bob"), kernel.ErrNotFound)
	if m := storekit.Get[model.Group](
		t,
	)(
		s.Groups().Get(ctx, "g1"),
	).Members; slices.Contains(
		m,
		"bob",
	) {
		t.Fatalf("deleted user still a member: %v", m)
	}
}

func testGroups(t *testing.T, s port.Store) {
	ctx := context.Background()
	gs := s.Groups()
	if got, next, err := gs.List(ctx, kernel.Page{}); err != nil || len(got) != 0 || next != "" {
		t.Fatalf("List on an empty store = %v, %q, %v", got, next, err)
	}
	for i, n := range []string{"alice", "bob"} {
		storekit.Must(t, s.Users().Create(ctx, user(i, n)))
	}
	storekit.WantErr(t, gs.Create(ctx, group("devs", false, "alice", "nobody")), kernel.ErrNotFound)
	_, err := gs.Get(ctx, "devs")
	storekit.WantErr(t, err, kernel.ErrNotFound)

	devs := group("devs", true, "bob", "alice", "bob")
	storekit.Must(t, gs.Create(ctx, devs))
	wantDevs := devs
	wantDevs.Members = []string{"alice", "bob"}
	got := storekit.Get[model.Group](t)(gs.Get(ctx, "devs"))
	storekit.Same(t, "Get", wantDevs, got)
	got.Members[0] = "changed"
	storekit.Same(
		t,
		"Get after mutating a result",
		wantDevs,
		storekit.Get[model.Group](t)(gs.Get(ctx, "devs")),
	)
	storekit.WantErr(t, gs.Create(ctx, group("devs", false)), kernel.ErrConflict)
	storekit.Same(
		t,
		"user sees the group",
		[]string{"devs"},
		storekit.Get[model.User](t)(s.Users().Get(ctx, "alice")).Groups,
	)

	devs.Description, devs.Members = "changed", []string{"bob"}
	storekit.Must(t, gs.Update(ctx, devs))
	storekit.Same(t, "Update", devs, storekit.Get[model.Group](t)(gs.Get(ctx, "devs")))
	bad := devs
	bad.Members = []string{"nobody"}
	storekit.WantErr(t, gs.Update(ctx, bad), kernel.ErrNotFound)
	storekit.Same(t, "failed Update", devs, storekit.Get[model.Group](t)(gs.Get(ctx, "devs")))
	storekit.WantErr(t, gs.Update(ctx, group("nope", false)), kernel.ErrNotFound)

	for _, g := range []model.Group{group("readers", true), group("admins", false), group("ops", false), group("zeta", true)} {
		storekit.Must(t, gs.Create(ctx, g))
	}
	all := storekit.Pages(
		t,
		2,
		func(p kernel.Page) ([]model.Group, string, error) { return gs.List(ctx, p) },
	)
	storekit.Same(
		t,
		"List order",
		[]string{"admins", "devs", "ops", "readers", "zeta"},
		groupNames(all),
	)
	storekit.Same(t, "List members", []string{"bob"}, all[1].Members)
	storekit.Same(
		t,
		"AutoJoin",
		[]string{"devs", "readers", "zeta"},
		storekit.Get[[]string](t)(gs.AutoJoin(ctx)),
	)
	for _, bad := range []string{"%%%", kernel.Cursor("a", "b")} {
		_, _, err = gs.List(ctx, kernel.Page{After: bad})
		storekit.WantErr(t, err, kernel.ErrInvalidCursor)
	}

	storekit.Must(t, gs.Delete(ctx, "devs"))
	_, err = gs.Get(ctx, "devs")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.WantErr(t, gs.Delete(ctx, "devs"), kernel.ErrNotFound)
	storekit.Same(
		t,
		"memberships removed",
		[]string(nil),
		storekit.Get[model.User](t)(s.Users().Get(ctx, "bob")).Groups,
	)
}

func testMemberships(t *testing.T, s port.Store) {
	ctx := context.Background()
	gs := s.Groups()
	for i, n := range []string{"alice", "bob", "carol"} {
		storekit.Must(t, s.Users().Create(ctx, user(i, n)))
	}
	storekit.Must(t, gs.Create(ctx, group("devs", false)))
	storekit.Must(t, gs.Create(ctx, group("ops", false)))

	storekit.Must(t, gs.AddMembers(ctx, "devs", "carol", "alice"))
	storekit.Must(t, gs.AddMembers(ctx, "devs", "alice")) // again: not an error
	storekit.Must(t, gs.AddMembers(ctx, "ops", "alice"))
	storekit.Same(
		t,
		"members",
		[]string{"alice", "carol"},
		storekit.Get[model.Group](t)(gs.Get(ctx, "devs")).Members,
	)
	storekit.Same(
		t,
		"groups",
		[]string{"devs", "ops"},
		storekit.Get[model.User](t)(s.Users().Get(ctx, "alice")).Groups,
	)

	storekit.WantErr(t, gs.AddMembers(ctx, "nope", "alice"), kernel.ErrNotFound)
	storekit.WantErr(t, gs.AddMembers(ctx, "devs", "bob", "nobody"), kernel.ErrNotFound)
	storekit.Same(
		t,
		"failed AddMembers",
		[]string{"alice", "carol"},
		storekit.Get[model.Group](t)(gs.Get(ctx, "devs")).Members,
	)

	storekit.Must(t, gs.RemoveMembers(ctx, "devs", "alice", "bob", "nobody"))
	storekit.Same(
		t,
		"after RemoveMembers",
		[]string{"carol"},
		storekit.Get[model.Group](t)(gs.Get(ctx, "devs")).Members,
	)
	storekit.WantErr(t, gs.RemoveMembers(ctx, "nope", "alice"), kernel.ErrNotFound)

	// Replacing a user's groups through Update leaves other members alone.
	carol := storekit.Get[model.User](t)(s.Users().Get(ctx, "carol"))
	carol.Groups = []string{"ops"}
	storekit.Must(t, s.Users().Update(ctx, carol))
	storekit.Same(
		t,
		"devs after carol left",
		[]string(nil),
		storekit.Get[model.Group](t)(gs.Get(ctx, "devs")).Members,
	)
	storekit.Same(
		t,
		"ops",
		[]string{"alice", "carol"},
		storekit.Get[model.Group](t)(gs.Get(ctx, "ops")).Members,
	)
}

func permission(
	i int,
	name string,
	resources map[api.ResourceType]model.Resource,
) model.Permission {
	return model.Permission{
		Name:        name,
		Description: "permission " + name,
		Resources:   resources,
		Created:     storekit.At(i),
		Updated:     storekit.At(i + 1),
	}
}

func testPermissions(t *testing.T, s port.Store) {
	ctx := context.Background()
	ps := s.Permissions()
	deploy := permission(1, "deploy", map[api.ResourceType]model.Resource{
		api.ResourceArtifact: {
			Actions: model.Grants{
				Users:  map[string]api.Actions{"alice": api.ActionRead | api.ActionWrite},
				Groups: map[string]api.Actions{"devs": api.ActionRead},
			},
			Targets: map[string]model.Target{
				"libs-local":        {Include: []string{"**"}, Exclude: []string{"secret/**"}},
				api.TargetAnyRemote: {Include: []string{"**"}},
			},
		},
		api.ResourceBuild: {
			Actions: model.Grants{Users: map[string]api.Actions{"bob": api.ActionRead}},
			Targets: map[string]model.Target{
				api.BuildInfoRepository: {Include: []string{"app/**"}},
			},
		},
	})
	// A grant of no actions grants nothing.
	empty := permission(2, "empty", map[api.ResourceType]model.Resource{
		api.ResourceArtifact: {
			Actions: model.Grants{Users: map[string]api.Actions{"carol": 0}},
			Targets: map[string]model.Target{"libs-local": {}},
		},
	})
	storekit.Must(t, ps.Create(ctx, deploy))
	storekit.Must(t, ps.Create(ctx, empty))
	got := storekit.Get[model.Permission](t)(ps.Get(ctx, "deploy"))
	storekit.Same(t, "Get", deploy, got)
	got.Resources[api.ResourceArtifact].Actions.Users["alice"] = api.ActionManage
	storekit.Same(
		t,
		"Get after mutating a result",
		deploy,
		storekit.Get[model.Permission](t)(ps.Get(ctx, "deploy")),
	)
	storekit.WantErr(t, ps.Create(ctx, permission(3, "deploy", nil)), kernel.ErrConflict)
	_, err := ps.Get(ctx, "nope")
	storekit.WantErr(t, err, kernel.ErrNotFound)

	for _, c := range []struct {
		user   string
		groups []string
		want   []string
	}{
		{"alice", nil, []string{"deploy"}},
		{"bob", nil, []string{"deploy"}},
		{"carol", nil, nil},
		{"zed", []string{"ops", "devs"}, []string{"deploy"}},
		{"zed", []string{"ops"}, nil},
	} {
		storekit.Same(
			t,
			fmt.Sprintf("ForPrincipal(%s, %v)", c.user, c.groups),
			c.want,
			permissionNames(
				storekit.Get[[]model.Permission](t)(ps.ForPrincipal(ctx, c.user, c.groups)),
			),
		)
	}
	storekit.Same(t, "WithTarget libs-local", []string{"deploy", "empty"},
		permissionNames(storekit.Get[[]model.Permission](t)(ps.WithTarget(ctx, "libs-local"))))
	storekit.Same(
		t,
		"WithTarget build",
		[]string{"deploy"},
		permissionNames(
			storekit.Get[[]model.Permission](t)(ps.WithTarget(ctx, api.BuildInfoRepository)),
		),
	)
	storekit.Same(t, "WithTarget none", []string(nil),
		permissionNames(storekit.Get[[]model.Permission](t)(ps.WithTarget(ctx, "other"))))

	// Update replaces the document and what the lookups see.
	deploy.Resources = map[api.ResourceType]model.Resource{
		api.ResourceArtifact: {
			Actions: model.Grants{Groups: map[string]api.Actions{"ops": api.ActionRead}},
			Targets: map[string]model.Target{"other": {Include: []string{"**"}}},
		},
	}
	deploy.Updated = storekit.At(9)
	storekit.Must(t, ps.Update(ctx, deploy))
	storekit.Same(t, "Update", deploy, storekit.Get[model.Permission](t)(ps.Get(ctx, "deploy")))
	storekit.Same(t, "ForPrincipal after Update", []string(nil),
		permissionNames(storekit.Get[[]model.Permission](t)(ps.ForPrincipal(ctx, "alice", nil))))
	storekit.Same(
		t,
		"ForPrincipal ops after Update",
		[]string{"deploy"},
		permissionNames(
			storekit.Get[[]model.Permission](t)(ps.ForPrincipal(ctx, "x", []string{"ops"})),
		),
	)
	storekit.Same(t, "WithTarget after Update", []string{"empty"},
		permissionNames(storekit.Get[[]model.Permission](t)(ps.WithTarget(ctx, "libs-local"))))
	storekit.WantErr(t, ps.Update(ctx, permission(1, "nope", nil)), kernel.ErrNotFound)

	for i, n := range []string{"c", "a", "b"} {
		storekit.Must(t, ps.Create(ctx, permission(i, n, nil)))
	}
	all := storekit.Pages(
		t,
		2,
		func(p kernel.Page) ([]model.Permission, string, error) { return ps.List(ctx, p) },
	)
	storekit.Same(t, "List order", []string{"a", "b", "c", "deploy", "empty"}, permissionNames(all))
	storekit.Same(t, "List record", deploy, all[3])
	for _, bad := range []string{"%%%", kernel.Cursor("a", "b")} {
		_, _, err = ps.List(ctx, kernel.Page{After: bad})
		storekit.WantErr(t, err, kernel.ErrInvalidCursor)
	}

	storekit.Must(t, ps.Delete(ctx, "deploy"))
	_, err = ps.Get(ctx, "deploy")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.WantErr(t, ps.Delete(ctx, "deploy"), kernel.ErrNotFound)
	storekit.Same(
		t,
		"ForPrincipal after Delete",
		[]string(nil),
		permissionNames(
			storekit.Get[[]model.Permission](t)(ps.ForPrincipal(ctx, "x", []string{"ops"})),
		),
	)
}

func scope(t *testing.T, s string) kernel.Scope {
	t.Helper()
	sc, err := kernel.ParseScope(s)
	storekit.Must(t, err)
	return sc
}

func token(t *testing.T, id, subject string, issued time.Time) model.Token {
	return model.Token{
		ID:      id,
		Subject: subject,
		Scope: scope(
			t,
			`applied-permissions/groups:readers,"Project Admin" repo:libs-local:r,w`,
		),
		Audience:        "jfrt@*",
		Description:     "token " + id,
		ProjectKey:      "proj",
		IssuedAt:        issued,
		ExpiresAt:       issued.Add(time.Hour),
		Refreshable:     true,
		Revocable:       true,
		AccessDigest:    []byte("access-" + id),
		RefreshDigest:   []byte("refresh-" + id),
		ReferenceDigest: []byte("reference-" + id),
	}
}

func tokenIDs(ts []model.Token) []string {
	return names(ts, func(t model.Token) string { return t.ID })
}

func testTokens(t *testing.T, s port.Store) {
	ctx := context.Background()
	ts := s.Tokens()
	sub := "jfac@01abc/users/alice"
	t1 := token(t, "t1", sub, storekit.At(1))
	storekit.Must(t, ts.Create(ctx, t1))
	got := storekit.Get[model.Token](t)(ts.Get(ctx, "t1"))
	storekit.Same(t, "Get", t1, got)
	got.AccessDigest[0] = 'X'
	storekit.Same(
		t,
		"Get after mutating a result",
		t1,
		storekit.Get[model.Token](t)(ts.Get(ctx, "t1")),
	)
	storekit.WantErr(t, ts.Create(ctx, token(t, "t1", sub, storekit.At(2))), kernel.ErrConflict)
	_, err := ts.Get(ctx, "nope")
	storekit.WantErr(t, err, kernel.ErrNotFound)

	// Every digest is unique; empty digests never collide.
	dup := token(t, "t9", sub, storekit.At(2))
	dup.RefreshDigest = t1.RefreshDigest
	storekit.WantErr(t, ts.Create(ctx, dup), kernel.ErrConflict)
	_, err = ts.Get(ctx, "t9")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	// The identity of an OIDC exchange's token round-trips (research 21).
	bare := []model.Token{
		{
			ID: "t0", Subject: "jfac@01abc/users/bob", Scope: kernel.UserScope(),
			Identity: kernel.IdentityTransient,
		},
		{ID: "t00", Subject: "jfac@01abc/users/bob", Identity: kernel.IdentitySynthetic},
	}
	for _, b := range bare {
		storekit.Must(t, ts.Create(ctx, b))
		storekit.Same(t, "Get bare "+b.ID, b, storekit.Get[model.Token](t)(ts.Get(ctx, b.ID)))
	}

	for _, kind := range []port.DigestKind{port.DigestAccess, port.DigestRefresh, port.DigestReference} {
		var d []byte
		switch kind {
		case port.DigestAccess:
			d = t1.AccessDigest
		case port.DigestRefresh:
			d = t1.RefreshDigest
		default:
			d = t1.ReferenceDigest
		}
		if got := storekit.Get[model.Token](t)(ts.ByDigest(ctx, kind, d)); got.ID != "t1" {
			t.Fatalf("ByDigest(%d) = %s", kind, got.ID)
		}
		_, err := ts.ByDigest(ctx, kind, []byte("unknown"))
		storekit.WantErr(t, err, kernel.ErrNotFound)
		_, err = ts.ByDigest(ctx, kind, nil)
		storekit.WantErr(t, err, kernel.ErrNotFound)
	}
	// An access digest is not a refresh digest.
	_, err = ts.ByDigest(ctx, port.DigestRefresh, t1.AccessDigest)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	_, err = ts.ByDigest(ctx, port.DigestKind(0), t1.AccessDigest)
	storekit.WantErr(t, err, kernel.ErrInvalid)

	// Ties on IssuedAt order by ID; unset IssuedAt sorts first.
	for _, tk := range []model.Token{
		token(t, "t4", sub, storekit.At(3)), token(t, "t3", sub, storekit.At(3)), token(t, "t2", "jfac@01abc/users/carol", storekit.At(2)),
	} {
		storekit.Must(t, ts.Create(ctx, tk))
	}
	all := storekit.Pages(
		t,
		2,
		func(p kernel.Page) ([]model.Token, string, error) { return ts.List(ctx, port.TokenFilter{}, p) },
	)
	storekit.Same(t, "List order", []string{"t0", "t00", "t1", "t2", "t3", "t4"}, tokenIDs(all))
	mine := storekit.Pages(t, 1, func(p kernel.Page) ([]model.Token, string, error) {
		return ts.List(ctx, port.TokenFilter{Subject: sub}, p)
	})
	storekit.Same(t, "List by subject", []string{"t1", "t3", "t4"}, tokenIDs(mine))
	for _, bad := range []string{"%%%", kernel.Cursor("x", "t1"), kernel.Cursor("t1")} {
		_, _, err = ts.List(ctx, port.TokenFilter{}, kernel.Page{After: bad})
		storekit.WantErr(t, err, kernel.ErrInvalidCursor)
	}

	// A subject holds at most one token of a purpose, and its sealed secret
	// round-trips. Unpurposed tokens never collide on the purpose.
	pw := model.Token{
		ID:      "tp1",
		Subject: "jfac@01abc/users/dave",
		Purpose: model.TokenPurposeEncryptedPassword,
		Secret:  "ref-tp1",
	}
	storekit.Must(t, ts.Create(ctx, pw))
	storekit.Same(t, "Get purposed", pw, storekit.Get[model.Token](t)(ts.Get(ctx, "tp1")))
	again := pw
	again.ID, again.Secret = "tp2", "ref-tp2"
	storekit.WantErr(t, ts.Create(ctx, again), kernel.ErrConflict)
	other := again
	other.Subject = "jfac@01abc/users/erin"
	storekit.Must(t, ts.Create(ctx, other))
	byPurpose := storekit.Pages(t, 1, func(p kernel.Page) ([]model.Token, string, error) {
		return ts.List(ctx, port.TokenFilter{Purpose: model.TokenPurposeEncryptedPassword}, p)
	})
	storekit.Same(t, "List by purpose", []string{"tp1", "tp2"}, tokenIDs(byPurpose))
	davePW, _, err := ts.List(
		ctx,
		port.TokenFilter{Subject: pw.Subject, Purpose: model.TokenPurposeEncryptedPassword},
		kernel.Page{},
	)
	storekit.Must(t, err)
	storekit.Same(t, "List by subject and purpose", []model.Token{pw}, davePW)
	for _, id := range []string{"tp1", "tp2"} {
		storekit.Must(t, ts.Delete(ctx, id))
	}

	storekit.Must(t, ts.Touch(ctx, "t1", storekit.At(30)))
	t1.LastUsed = storekit.At(30)
	storekit.Same(t, "Touch", t1, storekit.Get[model.Token](t)(ts.Get(ctx, "t1")))
	storekit.WantErr(t, ts.Touch(ctx, "nope", storekit.At(30)), kernel.ErrNotFound)

	// t1, t2 and t3/t4 expire an hour after issue; t0 and t00 never expire.
	n, err := ts.DeleteExpired(ctx, storekit.At(62).Add(time.Second))
	storekit.Must(t, err)
	if n != 2 {
		t.Fatalf("DeleteExpired removed %d, want 2", n)
	}
	rest := storekit.Pages(
		t,
		10,
		func(p kernel.Page) ([]model.Token, string, error) { return ts.List(ctx, port.TokenFilter{}, p) },
	)
	storekit.Same(t, "after DeleteExpired", []string{"t0", "t00", "t3", "t4"}, tokenIDs(rest))

	storekit.Must(t, ts.Delete(ctx, "t3"))
	_, err = ts.Get(ctx, "t3")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.WantErr(t, ts.Delete(ctx, "t3"), kernel.ErrNotFound)
	_, err = ts.ByDigest(ctx, port.DigestAccess, []byte("access-t3"))
	storekit.WantErr(t, err, kernel.ErrNotFound)
}

func keypair(name string) api.Keypair {
	return api.Keypair{
		Name:       name,
		Type:       api.KeypairGPG,
		Alias:      name + "-alias",
		PublicKey:  "-----BEGIN PGP PUBLIC KEY BLOCK-----\n" + name,
		PrivateKey: "-----BEGIN PGP PRIVATE KEY BLOCK-----\n" + name,
		Passphrase: "pass-" + name,
	}
}

func testKeypairs(t *testing.T, s port.Store) {
	ctx := context.Background()
	ks := s.Keys()
	for _, n := range []string{"rsa", "gpg"} {
		storekit.Must(t, ks.CreateKeypair(ctx, keypair(n)))
	}
	storekit.Same(
		t,
		"Keypair",
		keypair("gpg"),
		storekit.Get[api.Keypair](t)(ks.Keypair(ctx, "gpg")),
	)
	storekit.WantErr(t, ks.CreateKeypair(ctx, keypair("gpg")), kernel.ErrConflict)
	_, err := ks.Keypair(ctx, "nope")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	// No passphrase is stored as none.
	plain := keypair("plain")
	plain.Type, plain.Passphrase = api.KeypairRSA, ""
	storekit.Must(t, ks.CreateKeypair(ctx, plain))
	storekit.Same(
		t,
		"Keypair without passphrase",
		plain,
		storekit.Get[api.Keypair](t)(ks.Keypair(ctx, "plain")),
	)

	storekit.Same(
		t,
		"Keypairs",
		[]api.Keypair{keypair("gpg"), plain, keypair("rsa")},
		storekit.Get[[]api.Keypair](t)(ks.Keypairs(ctx)),
	)

	upd := keypair("rsa")
	upd.PrivateKey, upd.Passphrase, upd.Alias = "rotated", "new-pass", "new-alias"
	storekit.Must(t, ks.UpdateKeypair(ctx, upd))
	storekit.Same(t, "UpdateKeypair", upd, storekit.Get[api.Keypair](t)(ks.Keypair(ctx, "rsa")))
	storekit.WantErr(t, ks.UpdateKeypair(ctx, keypair("nope")), kernel.ErrNotFound)

	storekit.Must(t, ks.DeleteKeypair(ctx, "rsa"))
	_, err = ks.Keypair(ctx, "rsa")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.WantErr(t, ks.DeleteKeypair(ctx, "rsa"), kernel.ErrNotFound)
}

func certificate(i int, alias string) api.Certificate {
	return api.Certificate{
		Alias:       alias,
		PEM:         "-----BEGIN CERTIFICATE-----\n" + alias,
		PrivateKey:  "-----BEGIN PRIVATE KEY-----\n" + alias,
		IssuedTo:    "CN=" + alias,
		IssuedBy:    "CN=Example CA",
		IssuedOn:    storekit.At(i),
		ValidUntil:  storekit.At(i).AddDate(1, 0, 0),
		Fingerprint: fmt.Sprintf("%064x", i),
	}
}

func testCertificates(t *testing.T, s port.Store) {
	ctx := context.Background()
	ks := s.Keys()
	b, a := certificate(1, "b"), certificate(2, "a")
	storekit.Must(t, ks.PutCertificate(ctx, b))
	storekit.Must(t, ks.PutCertificate(ctx, a))
	storekit.Same(t, "Certificate", b, storekit.Get[api.Certificate](t)(ks.Certificate(ctx, "b")))
	b.PEM, b.IssuedOn = "replaced", storekit.At(7)
	storekit.Must(t, ks.PutCertificate(ctx, b))
	storekit.Same(
		t,
		"replaced Certificate",
		b,
		storekit.Get[api.Certificate](t)(ks.Certificate(ctx, "b")),
	)
	storekit.Same(
		t,
		"Certificates",
		[]api.Certificate{a, b},
		storekit.Get[[]api.Certificate](t)(ks.Certificates(ctx)),
	)
	_, err := ks.Certificate(ctx, "nope")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.Must(t, ks.DeleteCertificate(ctx, "a"))
	storekit.WantErr(t, ks.DeleteCertificate(ctx, "a"), kernel.ErrNotFound)
	storekit.Same(
		t,
		"Certificates after delete",
		[]api.Certificate{b},
		storekit.Get[[]api.Certificate](t)(ks.Certificates(ctx)),
	)
}

func trustedKey(i int, kid, alias string) model.TrustedKey {
	return model.TrustedKey{
		Kid:         kid,
		Alias:       alias,
		PublicKey:   "-----BEGIN PGP PUBLIC KEY BLOCK-----\n" + kid,
		Fingerprint: fmt.Sprintf("%040x", i),
		IssuedOn:    storekit.At(i),
		IssuedBy:    "release@example.com",
		ValidUntil:  storekit.At(i).AddDate(2, 0, 0),
	}
}

func testTrustedKeys(t *testing.T, s port.Store) {
	ctx := context.Background()
	ks := s.Keys()
	k2, k1 := trustedKey(2, "k2", "edge"), trustedKey(1, "k1", "")
	storekit.Must(t, ks.CreateTrustedKey(ctx, k2))
	storekit.Must(t, ks.CreateTrustedKey(ctx, k1))
	// Keys without an alias do not collide with each other.
	k3 := trustedKey(3, "k3", "")
	storekit.Must(t, ks.CreateTrustedKey(ctx, k3))
	storekit.Same(t, "TrustedKey", k2, storekit.Get[model.TrustedKey](t)(ks.TrustedKey(ctx, "k2")))
	storekit.WantErr(t, ks.CreateTrustedKey(ctx, trustedKey(4, "k2", "other")), kernel.ErrConflict)
	storekit.WantErr(t, ks.CreateTrustedKey(ctx, trustedKey(4, "k4", "edge")), kernel.ErrConflict)
	_, err := ks.TrustedKey(ctx, "k4")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.Same(
		t,
		"TrustedKeys",
		[]model.TrustedKey{k1, k2, k3},
		storekit.Get[[]model.TrustedKey](t)(ks.TrustedKeys(ctx)),
	)
	storekit.Must(t, ks.DeleteTrustedKey(ctx, "k2"))
	storekit.WantErr(t, ks.DeleteTrustedKey(ctx, "k2"), kernel.ErrNotFound)
	// The alias is free again once its key is gone.
	storekit.Must(t, ks.CreateTrustedKey(ctx, trustedKey(5, "k5", "edge")))
}

func signingKey(alias string, isDefault bool) model.SigningKey {
	return model.SigningKey{
		Alias:      alias,
		PublicKey:  "-----BEGIN PGP PUBLIC KEY BLOCK-----\n" + alias,
		PrivateKey: "-----BEGIN PGP PRIVATE KEY BLOCK-----\n" + alias,
		Passphrase: "pass-" + alias,
		IsDefault:  isDefault,
	}
}

func testSigningKeys(t *testing.T, s port.Store) {
	ctx := context.Background()
	ks := s.Keys()
	_, err := ks.DefaultSigningKey(ctx)
	storekit.WantErr(t, err, kernel.ErrNotFound)

	storekit.Must(t, ks.PutSigningKey(ctx, signingKey("a", true)))
	storekit.Must(t, ks.PutSigningKey(ctx, signingKey("c", false)))
	storekit.Same(
		t,
		"SigningKey",
		signingKey("a", true),
		storekit.Get[model.SigningKey](t)(ks.SigningKey(ctx, "a")),
	)
	storekit.Same(
		t,
		"DefaultSigningKey",
		signingKey("a", true),
		storekit.Get[model.SigningKey](t)(ks.DefaultSigningKey(ctx)),
	)

	// A new default clears the old one.
	storekit.Must(t, ks.PutSigningKey(ctx, signingKey("b", true)))
	storekit.Same(
		t,
		"SigningKeys",
		[]model.SigningKey{signingKey("a", false), signingKey("b", true), signingKey("c", false)},
		storekit.Get[[]model.SigningKey](t)(ks.SigningKeys(ctx)),
	)
	storekit.Same(
		t,
		"new default",
		signingKey("b", true),
		storekit.Get[model.SigningKey](t)(ks.DefaultSigningKey(ctx)),
	)

	// Replacing the default key keeps it the default.
	b := signingKey("b", true)
	b.PrivateKey, b.Passphrase = "rotated", ""
	storekit.Must(t, ks.PutSigningKey(ctx, b))
	storekit.Same(t, "replaced", b, storekit.Get[model.SigningKey](t)(ks.SigningKey(ctx, "b")))
	storekit.Same(
		t,
		"still default",
		b,
		storekit.Get[model.SigningKey](t)(ks.DefaultSigningKey(ctx)),
	)

	_, err = ks.SigningKey(ctx, "nope")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.Must(t, ks.DeleteSigningKey(ctx, "b"))
	storekit.WantErr(t, ks.DeleteSigningKey(ctx, "b"), kernel.ErrNotFound)
	_, err = ks.DefaultSigningKey(ctx)
	storekit.WantErr(t, err, kernel.ErrNotFound)
}

func testUserLockout(t *testing.T, s port.Store) {
	ctx := context.Background()
	us := s.Users()
	for i, n := range []string{"ann", "ben", "cat", "dan"} {
		storekit.Must(t, us.Create(ctx, user(i, n)))
	}
	disabled := storekit.Get[model.User](t)(us.Get(ctx, "dan"))
	disabled.Status = model.UserDisabled
	storekit.Must(t, us.Update(ctx, disabled))

	// Failed logins count up and lock at the limit; zero never locks.
	for i, want := range []bool{false, false, true} {
		locked, err := us.RecordFailedLogin(ctx, "ann", 3)
		if err != nil || locked != want {
			t.Fatalf("ann's failure %d: locked %v, %v", i+1, locked, err)
		}
	}
	if locked, err := us.RecordFailedLogin(ctx, "ben", 0); err != nil || locked {
		t.Fatalf("no limit locked ben: %v, %v", locked, err)
	}
	// A disabled account stays disabled rather than becoming locked.
	if locked, err := us.RecordFailedLogin(ctx, "dan", 1); err != nil || locked {
		t.Fatalf("a disabled user was locked: %v, %v", locked, err)
	}
	if _, err := us.RecordFailedLogin(ctx, "nobody", 1); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("an unknown user: %v", err)
	}
	ann := storekit.Get[model.User](t)(us.Get(ctx, "ann"))
	if ann.Status != model.UserLocked || ann.FailedLogins != 3 {
		t.Fatalf("ann = %s with %d failures", ann.Status, ann.FailedLogins)
	}
	if _, err := us.RecordFailedLogin(ctx, "cat", 1); err != nil {
		t.Fatal(err)
	}
	storekit.Same(t, "Locked", []string{"ann", "cat"}, storekit.Get[[]string](t)(us.Locked(ctx)))

	// Unlocking named users ignores unknown names and resets counters.
	storekit.Must(t, us.Unlock(ctx, "ann", "nobody"))
	ann = storekit.Get[model.User](t)(us.Get(ctx, "ann"))
	if ann.Status != model.UserEnabled || ann.FailedLogins != 0 {
		t.Fatalf("unlocked ann = %s with %d failures", ann.Status, ann.FailedLogins)
	}
	storekit.Same(
		t,
		"Locked after one unlock",
		[]string{"cat"},
		storekit.Get[[]string](t)(us.Locked(ctx)),
	)

	// Unlocking everyone also clears counters of users below the limit and
	// leaves disabled users disabled.
	storekit.Must(t, us.Unlock(ctx))
	storekit.Same(
		t,
		"Locked after unlocking all",
		[]string{},
		storekit.Get[[]string](t)(us.Locked(ctx)),
	)
	if ben := storekit.Get[model.User](t)(us.Get(ctx, "ben")); ben.FailedLogins != 0 {
		t.Fatalf("ben's failures were not cleared: %d", ben.FailedLogins)
	}
	if dan := storekit.Get[model.User](t)(us.Get(ctx, "dan")); dan.Status != model.UserDisabled {
		t.Fatalf("unlock changed a disabled user: %s", dan.Status)
	}
	// A successful login resets the counter.
	if _, err := us.RecordFailedLogin(ctx, "ben", 5); err != nil {
		t.Fatal(err)
	}
	storekit.Must(t, us.RecordLogin(ctx, "ben", storekit.At(9)))
	if ben := storekit.Get[model.User](t)(us.Get(ctx, "ben")); ben.FailedLogins != 0 {
		t.Fatalf("a login did not reset ben's failures: %d", ben.FailedLogins)
	}
}
