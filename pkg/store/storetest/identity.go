package storetest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// runIdentity covers users, groups, memberships, permissions, tokens and key
// material.
func runIdentity(t *testing.T, newStore Factory) {
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

func user(i int, name string, groups ...string) types.User {
	return types.User{
		Name:             name,
		Email:            name + "@example.com",
		PasswordHash:     fmt.Appendf(nil, "$argon2id$v=19$m=65536,t=3,p=4$%s", name),
		Admin:            i%2 == 0,
		ProfileUpdatable: true,
		Realm:            types.RealmInternal,
		Status:           types.UserEnabled,
		MFA:              types.MFANone,
		Groups:           groups,
		Roles:            types.RoleFlags{WatchManager: true, ManageWebhook: i%2 == 1},
		Created:          at(i),
	}
}

func group(name string, autoJoin bool, members ...string) types.Group {
	return types.Group{
		Name:            name,
		Description:     "the " + name + " group",
		AutoJoin:        autoJoin,
		AdminPrivileges: name == "admins",
		Realm:           types.RealmInternal,
		RealmAttributes: "dn=" + name,
		ExternalID:      "ext-" + name,
		Members:         members,
		Roles:           types.RoleFlags{PolicyViewer: true},
	}
}

func names[V any](vs []V, name func(V) string) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = name(v)
	}
	return out
}

func userNames(us []types.User) []string {
	return names(us, func(u types.User) string { return u.Name })
}

func groupNames(gs []types.Group) []string {
	return names(gs, func(g types.Group) string { return g.Name })
}

func permissionNames(ps []types.Permission) []string {
	return names(ps, func(p types.Permission) string { return p.Name })
}

func testUsers(t *testing.T, s store.Store) {
	ctx := context.Background()
	us := s.Users()
	if got, next, err := us.List(ctx, store.Page{}); err != nil || len(got) != 0 || next != "" {
		t.Fatalf("List on an empty store = %v, %q, %v", got, next, err)
	}
	must(t, s.Groups().Create(ctx, group("g1", false)))
	must(t, s.Groups().Create(ctx, group("g2", false)))

	// A missing group fails the whole create.
	wantErr(t, us.Create(ctx, user(1, "alice", "g1", "nope")), store.ErrNotFound)
	_, err := us.Get(ctx, "alice")
	wantErr(t, err, store.ErrNotFound)

	alice := user(1, "alice", "g2", "g1", "g2")
	must(t, us.Create(ctx, alice))
	wantAlice := alice
	wantAlice.Groups = []string{"g1", "g2"}
	got := get[types.User](t)(us.Get(ctx, "alice"))
	same(t, "Get", wantAlice, got)
	if got.Created.Location() != time.UTC {
		t.Fatalf("Created location = %v", got.Created.Location())
	}
	wantErr(t, us.Create(ctx, user(9, "alice")), store.ErrConflict)

	// Returned values are copies.
	got.Groups[0] = "changed"
	got.PasswordHash[0] = 'X'
	same(t, "Get after mutating a result", wantAlice, get[types.User](t)(us.Get(ctx, "alice")))

	// Update replaces fields and memberships; a missing group changes nothing.
	alice.Email, alice.Groups, alice.Status = "a@example.org", []string{"g2"}, types.UserLocked
	alice.PasswordHash = nil
	alice.FailedLogins, alice.PasswordChanged, alice.PasswordExpired = 2, at(3), true
	must(t, us.Update(ctx, alice))
	same(t, "Update", alice, get[types.User](t)(us.Get(ctx, "alice")))
	bad := alice
	bad.Email, bad.Groups = "bad", []string{"nope"}
	wantErr(t, us.Update(ctx, bad), store.ErrNotFound)
	same(t, "failed Update", alice, get[types.User](t)(us.Get(ctx, "alice")))
	wantErr(t, us.Update(ctx, user(1, "nobody")), store.ErrNotFound)

	must(t, us.RecordLogin(ctx, "alice", at(5)))
	alice.LastLoggedIn, alice.FailedLogins = at(5), 0
	same(t, "RecordLogin", alice, get[types.User](t)(us.Get(ctx, "alice")))
	wantErr(t, us.RecordLogin(ctx, "nobody", at(5)), store.ErrNotFound)

	for i, n := range []string{"erin", "bob", "dave", "carol"} {
		must(t, us.Create(ctx, user(i+2, n, "g1")))
	}
	all := pages(t, 2, func(p store.Page) ([]types.User, string, error) { return us.List(ctx, p) })
	same(t, "List order", []string{"alice", "bob", "carol", "dave", "erin"}, userNames(all))
	same(t, "List memberships", []string{"g2"}, all[0].Groups)
	same(t, "List memberships", []string{"g1"}, all[1].Groups)
	same(t, "List record", alice, all[0])
	for _, bad := range []string{"%%%", store.Cursor("a", "b")} {
		_, _, err = us.List(ctx, store.Page{After: bad})
		wantErr(t, err, store.ErrInvalidCursor)
	}

	must(t, us.Delete(ctx, "bob"))
	_, err = us.Get(ctx, "bob")
	wantErr(t, err, store.ErrNotFound)
	wantErr(t, us.Delete(ctx, "bob"), store.ErrNotFound)
	if m := get[types.Group](t)(s.Groups().Get(ctx, "g1")).Members; slices.Contains(m, "bob") {
		t.Fatalf("deleted user still a member: %v", m)
	}
}

func testGroups(t *testing.T, s store.Store) {
	ctx := context.Background()
	gs := s.Groups()
	if got, next, err := gs.List(ctx, store.Page{}); err != nil || len(got) != 0 || next != "" {
		t.Fatalf("List on an empty store = %v, %q, %v", got, next, err)
	}
	for i, n := range []string{"alice", "bob"} {
		must(t, s.Users().Create(ctx, user(i, n)))
	}
	wantErr(t, gs.Create(ctx, group("devs", false, "alice", "nobody")), store.ErrNotFound)
	_, err := gs.Get(ctx, "devs")
	wantErr(t, err, store.ErrNotFound)

	devs := group("devs", true, "bob", "alice", "bob")
	must(t, gs.Create(ctx, devs))
	wantDevs := devs
	wantDevs.Members = []string{"alice", "bob"}
	got := get[types.Group](t)(gs.Get(ctx, "devs"))
	same(t, "Get", wantDevs, got)
	got.Members[0] = "changed"
	same(t, "Get after mutating a result", wantDevs, get[types.Group](t)(gs.Get(ctx, "devs")))
	wantErr(t, gs.Create(ctx, group("devs", false)), store.ErrConflict)
	same(
		t,
		"user sees the group",
		[]string{"devs"},
		get[types.User](t)(s.Users().Get(ctx, "alice")).Groups,
	)

	devs.Description, devs.Members = "changed", []string{"bob"}
	must(t, gs.Update(ctx, devs))
	same(t, "Update", devs, get[types.Group](t)(gs.Get(ctx, "devs")))
	bad := devs
	bad.Members = []string{"nobody"}
	wantErr(t, gs.Update(ctx, bad), store.ErrNotFound)
	same(t, "failed Update", devs, get[types.Group](t)(gs.Get(ctx, "devs")))
	wantErr(t, gs.Update(ctx, group("nope", false)), store.ErrNotFound)

	for _, g := range []types.Group{group("readers", true), group("admins", false), group("ops", false), group("zeta", true)} {
		must(t, gs.Create(ctx, g))
	}
	all := pages(t, 2, func(p store.Page) ([]types.Group, string, error) { return gs.List(ctx, p) })
	same(t, "List order", []string{"admins", "devs", "ops", "readers", "zeta"}, groupNames(all))
	same(t, "List members", []string{"bob"}, all[1].Members)
	same(t, "AutoJoin", []string{"devs", "readers", "zeta"}, get[[]string](t)(gs.AutoJoin(ctx)))
	for _, bad := range []string{"%%%", store.Cursor("a", "b")} {
		_, _, err = gs.List(ctx, store.Page{After: bad})
		wantErr(t, err, store.ErrInvalidCursor)
	}

	must(t, gs.Delete(ctx, "devs"))
	_, err = gs.Get(ctx, "devs")
	wantErr(t, err, store.ErrNotFound)
	wantErr(t, gs.Delete(ctx, "devs"), store.ErrNotFound)
	same(
		t,
		"memberships removed",
		[]string(nil),
		get[types.User](t)(s.Users().Get(ctx, "bob")).Groups,
	)
}

func testMemberships(t *testing.T, s store.Store) {
	ctx := context.Background()
	gs := s.Groups()
	for i, n := range []string{"alice", "bob", "carol"} {
		must(t, s.Users().Create(ctx, user(i, n)))
	}
	must(t, gs.Create(ctx, group("devs", false)))
	must(t, gs.Create(ctx, group("ops", false)))

	must(t, gs.AddMembers(ctx, "devs", "carol", "alice"))
	must(t, gs.AddMembers(ctx, "devs", "alice")) // again: not an error
	must(t, gs.AddMembers(ctx, "ops", "alice"))
	same(t, "members", []string{"alice", "carol"}, get[types.Group](t)(gs.Get(ctx, "devs")).Members)
	same(
		t,
		"groups",
		[]string{"devs", "ops"},
		get[types.User](t)(s.Users().Get(ctx, "alice")).Groups,
	)

	wantErr(t, gs.AddMembers(ctx, "nope", "alice"), store.ErrNotFound)
	wantErr(t, gs.AddMembers(ctx, "devs", "bob", "nobody"), store.ErrNotFound)
	same(
		t,
		"failed AddMembers",
		[]string{"alice", "carol"},
		get[types.Group](t)(gs.Get(ctx, "devs")).Members,
	)

	must(t, gs.RemoveMembers(ctx, "devs", "alice", "bob", "nobody"))
	same(
		t,
		"after RemoveMembers",
		[]string{"carol"},
		get[types.Group](t)(gs.Get(ctx, "devs")).Members,
	)
	wantErr(t, gs.RemoveMembers(ctx, "nope", "alice"), store.ErrNotFound)

	// Replacing a user's groups through Update leaves other members alone.
	carol := get[types.User](t)(s.Users().Get(ctx, "carol"))
	carol.Groups = []string{"ops"}
	must(t, s.Users().Update(ctx, carol))
	same(
		t,
		"devs after carol left",
		[]string(nil),
		get[types.Group](t)(gs.Get(ctx, "devs")).Members,
	)
	same(t, "ops", []string{"alice", "carol"}, get[types.Group](t)(gs.Get(ctx, "ops")).Members)
}

func permission(
	i int,
	name string,
	resources map[types.ResourceType]types.Resource,
) types.Permission {
	return types.Permission{
		Name:        name,
		Description: "permission " + name,
		Resources:   resources,
		Created:     at(i),
		Updated:     at(i + 1),
	}
}

func testPermissions(t *testing.T, s store.Store) {
	ctx := context.Background()
	ps := s.Permissions()
	deploy := permission(1, "deploy", map[types.ResourceType]types.Resource{
		types.ResourceArtifact: {
			Actions: types.Grants{
				Users:  map[string]types.Actions{"alice": types.ActionRead | types.ActionWrite},
				Groups: map[string]types.Actions{"devs": types.ActionRead},
			},
			Targets: map[string]types.Target{
				"libs-local":          {Include: []string{"**"}, Exclude: []string{"secret/**"}},
				types.TargetAnyRemote: {Include: []string{"**"}},
			},
		},
		types.ResourceBuild: {
			Actions: types.Grants{Users: map[string]types.Actions{"bob": types.ActionRead}},
			Targets: map[string]types.Target{
				types.BuildInfoRepository: {Include: []string{"app/**"}},
			},
		},
	})
	// A grant of no actions grants nothing.
	empty := permission(2, "empty", map[types.ResourceType]types.Resource{
		types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"carol": 0}},
			Targets: map[string]types.Target{"libs-local": {}},
		},
	})
	must(t, ps.Create(ctx, deploy))
	must(t, ps.Create(ctx, empty))
	got := get[types.Permission](t)(ps.Get(ctx, "deploy"))
	same(t, "Get", deploy, got)
	got.Resources[types.ResourceArtifact].Actions.Users["alice"] = types.ActionManage
	same(t, "Get after mutating a result", deploy, get[types.Permission](t)(ps.Get(ctx, "deploy")))
	wantErr(t, ps.Create(ctx, permission(3, "deploy", nil)), store.ErrConflict)
	_, err := ps.Get(ctx, "nope")
	wantErr(t, err, store.ErrNotFound)

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
		same(t, fmt.Sprintf("ForPrincipal(%s, %v)", c.user, c.groups), c.want,
			permissionNames(get[[]types.Permission](t)(ps.ForPrincipal(ctx, c.user, c.groups))))
	}
	same(t, "WithTarget libs-local", []string{"deploy", "empty"},
		permissionNames(get[[]types.Permission](t)(ps.WithTarget(ctx, "libs-local"))))
	same(t, "WithTarget build", []string{"deploy"},
		permissionNames(get[[]types.Permission](t)(ps.WithTarget(ctx, types.BuildInfoRepository))))
	same(t, "WithTarget none", []string(nil),
		permissionNames(get[[]types.Permission](t)(ps.WithTarget(ctx, "other"))))

	// Update replaces the document and what the lookups see.
	deploy.Resources = map[types.ResourceType]types.Resource{
		types.ResourceArtifact: {
			Actions: types.Grants{Groups: map[string]types.Actions{"ops": types.ActionRead}},
			Targets: map[string]types.Target{"other": {Include: []string{"**"}}},
		},
	}
	deploy.Updated = at(9)
	must(t, ps.Update(ctx, deploy))
	same(t, "Update", deploy, get[types.Permission](t)(ps.Get(ctx, "deploy")))
	same(t, "ForPrincipal after Update", []string(nil),
		permissionNames(get[[]types.Permission](t)(ps.ForPrincipal(ctx, "alice", nil))))
	same(t, "ForPrincipal ops after Update", []string{"deploy"},
		permissionNames(get[[]types.Permission](t)(ps.ForPrincipal(ctx, "x", []string{"ops"}))))
	same(t, "WithTarget after Update", []string{"empty"},
		permissionNames(get[[]types.Permission](t)(ps.WithTarget(ctx, "libs-local"))))
	wantErr(t, ps.Update(ctx, permission(1, "nope", nil)), store.ErrNotFound)

	for i, n := range []string{"c", "a", "b"} {
		must(t, ps.Create(ctx, permission(i, n, nil)))
	}
	all := pages(
		t,
		2,
		func(p store.Page) ([]types.Permission, string, error) { return ps.List(ctx, p) },
	)
	same(t, "List order", []string{"a", "b", "c", "deploy", "empty"}, permissionNames(all))
	same(t, "List record", deploy, all[3])
	for _, bad := range []string{"%%%", store.Cursor("a", "b")} {
		_, _, err = ps.List(ctx, store.Page{After: bad})
		wantErr(t, err, store.ErrInvalidCursor)
	}

	must(t, ps.Delete(ctx, "deploy"))
	_, err = ps.Get(ctx, "deploy")
	wantErr(t, err, store.ErrNotFound)
	wantErr(t, ps.Delete(ctx, "deploy"), store.ErrNotFound)
	same(t, "ForPrincipal after Delete", []string(nil),
		permissionNames(get[[]types.Permission](t)(ps.ForPrincipal(ctx, "x", []string{"ops"}))))
}

func scope(t *testing.T, s string) types.Scope {
	t.Helper()
	sc, err := types.ParseScope(s)
	must(t, err)
	return sc
}

func token(t *testing.T, id, subject string, issued time.Time) types.Token {
	return types.Token{
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

func tokenIDs(ts []types.Token) []string {
	return names(ts, func(t types.Token) string { return t.ID })
}

func testTokens(t *testing.T, s store.Store) {
	ctx := context.Background()
	ts := s.Tokens()
	sub := "jfac@01abc/users/alice"
	t1 := token(t, "t1", sub, at(1))
	must(t, ts.Create(ctx, t1))
	got := get[types.Token](t)(ts.Get(ctx, "t1"))
	same(t, "Get", t1, got)
	got.AccessDigest[0] = 'X'
	same(t, "Get after mutating a result", t1, get[types.Token](t)(ts.Get(ctx, "t1")))
	wantErr(t, ts.Create(ctx, token(t, "t1", sub, at(2))), store.ErrConflict)
	_, err := ts.Get(ctx, "nope")
	wantErr(t, err, store.ErrNotFound)

	// Every digest is unique; empty digests never collide.
	dup := token(t, "t9", sub, at(2))
	dup.RefreshDigest = t1.RefreshDigest
	wantErr(t, ts.Create(ctx, dup), store.ErrConflict)
	_, err = ts.Get(ctx, "t9")
	wantErr(t, err, store.ErrNotFound)
	bare := []types.Token{
		{ID: "t0", Subject: "jfac@01abc/users/bob", Scope: types.UserScope()},
		{ID: "t00", Subject: "jfac@01abc/users/bob"},
	}
	for _, b := range bare {
		must(t, ts.Create(ctx, b))
		same(t, "Get bare "+b.ID, b, get[types.Token](t)(ts.Get(ctx, b.ID)))
	}

	for _, kind := range []store.DigestKind{store.DigestAccess, store.DigestRefresh, store.DigestReference} {
		var d []byte
		switch kind {
		case store.DigestAccess:
			d = t1.AccessDigest
		case store.DigestRefresh:
			d = t1.RefreshDigest
		default:
			d = t1.ReferenceDigest
		}
		if got := get[types.Token](t)(ts.ByDigest(ctx, kind, d)); got.ID != "t1" {
			t.Fatalf("ByDigest(%d) = %s", kind, got.ID)
		}
		_, err := ts.ByDigest(ctx, kind, []byte("unknown"))
		wantErr(t, err, store.ErrNotFound)
		_, err = ts.ByDigest(ctx, kind, nil)
		wantErr(t, err, store.ErrNotFound)
	}
	// An access digest is not a refresh digest.
	_, err = ts.ByDigest(ctx, store.DigestRefresh, t1.AccessDigest)
	wantErr(t, err, store.ErrNotFound)
	_, err = ts.ByDigest(ctx, store.DigestKind(0), t1.AccessDigest)
	wantErr(t, err, store.ErrInvalid)

	// Ties on IssuedAt order by ID; unset IssuedAt sorts first.
	for _, tk := range []types.Token{
		token(t, "t4", sub, at(3)), token(t, "t3", sub, at(3)), token(t, "t2", "jfac@01abc/users/carol", at(2)),
	} {
		must(t, ts.Create(ctx, tk))
	}
	all := pages(
		t,
		2,
		func(p store.Page) ([]types.Token, string, error) { return ts.List(ctx, store.TokenFilter{}, p) },
	)
	same(t, "List order", []string{"t0", "t00", "t1", "t2", "t3", "t4"}, tokenIDs(all))
	mine := pages(t, 1, func(p store.Page) ([]types.Token, string, error) {
		return ts.List(ctx, store.TokenFilter{Subject: sub}, p)
	})
	same(t, "List by subject", []string{"t1", "t3", "t4"}, tokenIDs(mine))
	for _, bad := range []string{"%%%", store.Cursor("x", "t1"), store.Cursor("t1")} {
		_, _, err = ts.List(ctx, store.TokenFilter{}, store.Page{After: bad})
		wantErr(t, err, store.ErrInvalidCursor)
	}

	// A subject holds at most one token of a purpose, and its sealed secret
	// round-trips. Unpurposed tokens never collide on the purpose.
	pw := types.Token{
		ID:      "tp1",
		Subject: "jfac@01abc/users/dave",
		Purpose: types.TokenPurposeEncryptedPassword,
		Secret:  "ref-tp1",
	}
	must(t, ts.Create(ctx, pw))
	same(t, "Get purposed", pw, get[types.Token](t)(ts.Get(ctx, "tp1")))
	again := pw
	again.ID, again.Secret = "tp2", "ref-tp2"
	wantErr(t, ts.Create(ctx, again), store.ErrConflict)
	other := again
	other.Subject = "jfac@01abc/users/erin"
	must(t, ts.Create(ctx, other))
	byPurpose := pages(t, 1, func(p store.Page) ([]types.Token, string, error) {
		return ts.List(ctx, store.TokenFilter{Purpose: types.TokenPurposeEncryptedPassword}, p)
	})
	same(t, "List by purpose", []string{"tp1", "tp2"}, tokenIDs(byPurpose))
	davePW, _, err := ts.List(
		ctx,
		store.TokenFilter{Subject: pw.Subject, Purpose: types.TokenPurposeEncryptedPassword},
		store.Page{},
	)
	must(t, err)
	same(t, "List by subject and purpose", []types.Token{pw}, davePW)
	for _, id := range []string{"tp1", "tp2"} {
		must(t, ts.Delete(ctx, id))
	}

	must(t, ts.Touch(ctx, "t1", at(30)))
	t1.LastUsed = at(30)
	same(t, "Touch", t1, get[types.Token](t)(ts.Get(ctx, "t1")))
	wantErr(t, ts.Touch(ctx, "nope", at(30)), store.ErrNotFound)

	// t1, t2 and t3/t4 expire an hour after issue; t0 and t00 never expire.
	n, err := ts.DeleteExpired(ctx, at(62).Add(time.Second))
	must(t, err)
	if n != 2 {
		t.Fatalf("DeleteExpired removed %d, want 2", n)
	}
	rest := pages(
		t,
		10,
		func(p store.Page) ([]types.Token, string, error) { return ts.List(ctx, store.TokenFilter{}, p) },
	)
	same(t, "after DeleteExpired", []string{"t0", "t00", "t3", "t4"}, tokenIDs(rest))

	must(t, ts.Delete(ctx, "t3"))
	_, err = ts.Get(ctx, "t3")
	wantErr(t, err, store.ErrNotFound)
	wantErr(t, ts.Delete(ctx, "t3"), store.ErrNotFound)
	_, err = ts.ByDigest(ctx, store.DigestAccess, []byte("access-t3"))
	wantErr(t, err, store.ErrNotFound)
}

func keypair(name string) types.Keypair {
	return types.Keypair{
		Name:       name,
		Type:       types.KeypairGPG,
		Alias:      name + "-alias",
		PublicKey:  "-----BEGIN PGP PUBLIC KEY BLOCK-----\n" + name,
		PrivateKey: "-----BEGIN PGP PRIVATE KEY BLOCK-----\n" + name,
		Passphrase: "pass-" + name,
	}
}

func testKeypairs(t *testing.T, s store.Store) {
	ctx := context.Background()
	ks := s.Keys()
	for _, n := range []string{"rsa", "gpg"} {
		must(t, ks.CreateKeypair(ctx, keypair(n)))
	}
	same(t, "Keypair", keypair("gpg"), get[types.Keypair](t)(ks.Keypair(ctx, "gpg")))
	wantErr(t, ks.CreateKeypair(ctx, keypair("gpg")), store.ErrConflict)
	_, err := ks.Keypair(ctx, "nope")
	wantErr(t, err, store.ErrNotFound)
	// No passphrase is stored as none.
	plain := keypair("plain")
	plain.Type, plain.Passphrase = types.KeypairRSA, ""
	must(t, ks.CreateKeypair(ctx, plain))
	same(t, "Keypair without passphrase", plain, get[types.Keypair](t)(ks.Keypair(ctx, "plain")))

	same(
		t,
		"Keypairs",
		[]types.Keypair{keypair("gpg"), plain, keypair("rsa")},
		get[[]types.Keypair](t)(ks.Keypairs(ctx)),
	)

	upd := keypair("rsa")
	upd.PrivateKey, upd.Passphrase, upd.Alias = "rotated", "new-pass", "new-alias"
	must(t, ks.UpdateKeypair(ctx, upd))
	same(t, "UpdateKeypair", upd, get[types.Keypair](t)(ks.Keypair(ctx, "rsa")))
	wantErr(t, ks.UpdateKeypair(ctx, keypair("nope")), store.ErrNotFound)

	must(t, ks.DeleteKeypair(ctx, "rsa"))
	_, err = ks.Keypair(ctx, "rsa")
	wantErr(t, err, store.ErrNotFound)
	wantErr(t, ks.DeleteKeypair(ctx, "rsa"), store.ErrNotFound)
}

func certificate(i int, alias string) types.Certificate {
	return types.Certificate{
		Alias:       alias,
		PEM:         "-----BEGIN CERTIFICATE-----\n" + alias,
		PrivateKey:  "-----BEGIN PRIVATE KEY-----\n" + alias,
		IssuedTo:    "CN=" + alias,
		IssuedBy:    "CN=Example CA",
		IssuedOn:    at(i),
		ValidUntil:  at(i).AddDate(1, 0, 0),
		Fingerprint: fmt.Sprintf("%064x", i),
	}
}

func testCertificates(t *testing.T, s store.Store) {
	ctx := context.Background()
	ks := s.Keys()
	b, a := certificate(1, "b"), certificate(2, "a")
	must(t, ks.PutCertificate(ctx, b))
	must(t, ks.PutCertificate(ctx, a))
	same(t, "Certificate", b, get[types.Certificate](t)(ks.Certificate(ctx, "b")))
	b.PEM, b.IssuedOn = "replaced", at(7)
	must(t, ks.PutCertificate(ctx, b))
	same(t, "replaced Certificate", b, get[types.Certificate](t)(ks.Certificate(ctx, "b")))
	same(
		t,
		"Certificates",
		[]types.Certificate{a, b},
		get[[]types.Certificate](t)(ks.Certificates(ctx)),
	)
	_, err := ks.Certificate(ctx, "nope")
	wantErr(t, err, store.ErrNotFound)
	must(t, ks.DeleteCertificate(ctx, "a"))
	wantErr(t, ks.DeleteCertificate(ctx, "a"), store.ErrNotFound)
	same(
		t,
		"Certificates after delete",
		[]types.Certificate{b},
		get[[]types.Certificate](t)(ks.Certificates(ctx)),
	)
}

func trustedKey(i int, kid, alias string) types.TrustedKey {
	return types.TrustedKey{
		Kid:         kid,
		Alias:       alias,
		PublicKey:   "-----BEGIN PGP PUBLIC KEY BLOCK-----\n" + kid,
		Fingerprint: fmt.Sprintf("%040x", i),
		IssuedOn:    at(i),
		IssuedBy:    "release@example.com",
		ValidUntil:  at(i).AddDate(2, 0, 0),
	}
}

func testTrustedKeys(t *testing.T, s store.Store) {
	ctx := context.Background()
	ks := s.Keys()
	k2, k1 := trustedKey(2, "k2", "edge"), trustedKey(1, "k1", "")
	must(t, ks.CreateTrustedKey(ctx, k2))
	must(t, ks.CreateTrustedKey(ctx, k1))
	// Keys without an alias do not collide with each other.
	k3 := trustedKey(3, "k3", "")
	must(t, ks.CreateTrustedKey(ctx, k3))
	same(t, "TrustedKey", k2, get[types.TrustedKey](t)(ks.TrustedKey(ctx, "k2")))
	wantErr(t, ks.CreateTrustedKey(ctx, trustedKey(4, "k2", "other")), store.ErrConflict)
	wantErr(t, ks.CreateTrustedKey(ctx, trustedKey(4, "k4", "edge")), store.ErrConflict)
	_, err := ks.TrustedKey(ctx, "k4")
	wantErr(t, err, store.ErrNotFound)
	same(
		t,
		"TrustedKeys",
		[]types.TrustedKey{k1, k2, k3},
		get[[]types.TrustedKey](t)(ks.TrustedKeys(ctx)),
	)
	must(t, ks.DeleteTrustedKey(ctx, "k2"))
	wantErr(t, ks.DeleteTrustedKey(ctx, "k2"), store.ErrNotFound)
	// The alias is free again once its key is gone.
	must(t, ks.CreateTrustedKey(ctx, trustedKey(5, "k5", "edge")))
}

func signingKey(alias string, isDefault bool) types.SigningKey {
	return types.SigningKey{
		Alias:      alias,
		PublicKey:  "-----BEGIN PGP PUBLIC KEY BLOCK-----\n" + alias,
		PrivateKey: "-----BEGIN PGP PRIVATE KEY BLOCK-----\n" + alias,
		Passphrase: "pass-" + alias,
		IsDefault:  isDefault,
	}
}

func testSigningKeys(t *testing.T, s store.Store) {
	ctx := context.Background()
	ks := s.Keys()
	_, err := ks.DefaultSigningKey(ctx)
	wantErr(t, err, store.ErrNotFound)

	must(t, ks.PutSigningKey(ctx, signingKey("a", true)))
	must(t, ks.PutSigningKey(ctx, signingKey("c", false)))
	same(t, "SigningKey", signingKey("a", true), get[types.SigningKey](t)(ks.SigningKey(ctx, "a")))
	same(
		t,
		"DefaultSigningKey",
		signingKey("a", true),
		get[types.SigningKey](t)(ks.DefaultSigningKey(ctx)),
	)

	// A new default clears the old one.
	must(t, ks.PutSigningKey(ctx, signingKey("b", true)))
	same(
		t,
		"SigningKeys",
		[]types.SigningKey{signingKey("a", false), signingKey("b", true), signingKey("c", false)},
		get[[]types.SigningKey](t)(ks.SigningKeys(ctx)),
	)
	same(
		t,
		"new default",
		signingKey("b", true),
		get[types.SigningKey](t)(ks.DefaultSigningKey(ctx)),
	)

	// Replacing the default key keeps it the default.
	b := signingKey("b", true)
	b.PrivateKey, b.Passphrase = "rotated", ""
	must(t, ks.PutSigningKey(ctx, b))
	same(t, "replaced", b, get[types.SigningKey](t)(ks.SigningKey(ctx, "b")))
	same(t, "still default", b, get[types.SigningKey](t)(ks.DefaultSigningKey(ctx)))

	_, err = ks.SigningKey(ctx, "nope")
	wantErr(t, err, store.ErrNotFound)
	must(t, ks.DeleteSigningKey(ctx, "b"))
	wantErr(t, ks.DeleteSigningKey(ctx, "b"), store.ErrNotFound)
	_, err = ks.DefaultSigningKey(ctx)
	wantErr(t, err, store.ErrNotFound)
}

func testUserLockout(t *testing.T, s store.Store) {
	ctx := context.Background()
	us := s.Users()
	for i, n := range []string{"ann", "ben", "cat", "dan"} {
		must(t, us.Create(ctx, user(i, n)))
	}
	disabled := get[types.User](t)(us.Get(ctx, "dan"))
	disabled.Status = types.UserDisabled
	must(t, us.Update(ctx, disabled))

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
	if _, err := us.RecordFailedLogin(ctx, "nobody", 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an unknown user: %v", err)
	}
	ann := get[types.User](t)(us.Get(ctx, "ann"))
	if ann.Status != types.UserLocked || ann.FailedLogins != 3 {
		t.Fatalf("ann = %s with %d failures", ann.Status, ann.FailedLogins)
	}
	if _, err := us.RecordFailedLogin(ctx, "cat", 1); err != nil {
		t.Fatal(err)
	}
	same(t, "Locked", []string{"ann", "cat"}, get[[]string](t)(us.Locked(ctx)))

	// Unlocking named users ignores unknown names and resets counters.
	must(t, us.Unlock(ctx, "ann", "nobody"))
	ann = get[types.User](t)(us.Get(ctx, "ann"))
	if ann.Status != types.UserEnabled || ann.FailedLogins != 0 {
		t.Fatalf("unlocked ann = %s with %d failures", ann.Status, ann.FailedLogins)
	}
	same(t, "Locked after one unlock", []string{"cat"}, get[[]string](t)(us.Locked(ctx)))

	// Unlocking everyone also clears counters of users below the limit and
	// leaves disabled users disabled.
	must(t, us.Unlock(ctx))
	same(t, "Locked after unlocking all", []string{}, get[[]string](t)(us.Locked(ctx)))
	if ben := get[types.User](t)(us.Get(ctx, "ben")); ben.FailedLogins != 0 {
		t.Fatalf("ben's failures were not cleared: %d", ben.FailedLogins)
	}
	if dan := get[types.User](t)(us.Get(ctx, "dan")); dan.Status != types.UserDisabled {
		t.Fatalf("unlock changed a disabled user: %s", dan.Status)
	}
	// A successful login resets the counter.
	if _, err := us.RecordFailedLogin(ctx, "ben", 5); err != nil {
		t.Fatal(err)
	}
	must(t, us.RecordLogin(ctx, "ben", at(9)))
	if ben := get[types.User](t)(us.Get(ctx, "ben")); ben.FailedLogins != 0 {
		t.Fatalf("a login did not reset ben's failures: %d", ben.FailedLogins)
	}
}
