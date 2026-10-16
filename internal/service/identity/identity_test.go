package identity_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/service/identity"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var cheap = auth.HashParams{MemoryKiB: 64, Time: 1, Threads: 1}

var clock = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func newService(t *testing.T) (*identity.Service, *inmem.Store) {
	t.Helper()
	db := inmem.New()
	hash, err := auth.HashPasswordWith(cheap, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.SeedHashed(context.Background(), db, hash, clock); err != nil {
		t.Fatal(err)
	}
	return &identity.Service{Store: db, Hash: cheap, Now: func() time.Time { return clock }}, db
}

func wantInvalid(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, identity.ErrInvalid) {
		t.Fatalf("%s: %v, want ErrInvalid", what, err)
	}
}

func newUser(name string, groups ...string) types.User {
	return types.User{
		Name:             name,
		Email:            name + "@example.com",
		ProfileUpdatable: true,
		Groups:           groups,
	}
}

func TestPutUser(t *testing.T) {
	ctx := context.Background()
	s, db := newService(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := s.PutGroup(ctx, types.Group{Name: "joiners", AutoJoin: true}, nil)
	must(err)

	created, err := s.PutUser(ctx, newUser("ann", auth.ReadersGroup), "Ann-pass1")
	if err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	ann, err := s.User(ctx, "ann")
	must(err)
	if !slices.Equal(ann.Groups, []string{"joiners", auth.ReadersGroup}) ||
		ann.Status != types.UserEnabled || ann.Realm != types.RealmInternal ||
		!ann.PasswordChanged.Equal(clock) || len(ann.PasswordHash) == 0 {
		t.Fatalf("created user: %+v", ann)
	}
	if ok, err := auth.CheckPassword(ann.PasswordHash, "Ann-pass1"); err != nil || !ok {
		t.Fatalf("the stored hash does not verify: %v %v", ok, err)
	}

	// A replacement keeps server-owned state and the old hash when the
	// user's password is disabled.
	_, err = db.Users().RecordFailedLogin(ctx, "ann", 0)
	must(err)
	repl := newUser("ann")
	repl.Email, repl.InternalPasswordDisabled = "new@example.com", true
	created, err = s.PutUser(ctx, repl, "")
	if err != nil || created {
		t.Fatalf("replace: %v %v", created, err)
	}
	got, err := s.User(ctx, "ann")
	must(err)
	if got.Email != "new@example.com" || len(got.Groups) != 0 || got.FailedLogins != 1 ||
		!got.Created.Equal(ann.Created) || !slices.Equal(got.PasswordHash, ann.PasswordHash) {
		t.Fatalf("replaced user: %+v", got)
	}

	for what, put := range map[string]func() error{
		"no email":      func() error { _, err := s.PutUser(ctx, types.User{Name: "x"}, "pw"); return err },
		"no password":   func() error { _, err := s.PutUser(ctx, newUser("x"), ""); return err },
		"bad name":      func() error { _, err := s.PutUser(ctx, newUser("a/b"), "pw"); return err },
		"unknown group": func() error { _, err := s.PutUser(ctx, newUser("x", "nope"), "pw"); return err },
	} {
		wantInvalid(t, what, put())
	}
}

func TestPatchUser(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	if _, err := s.PutUser(ctx, newUser("ben"), "Ben-pass1"); err != nil {
		t.Fatal(err)
	}
	// A new password also clears an administrator's expiry.
	if _, err := s.ExpirePasswords(ctx, []string{"ben"}); err != nil {
		t.Fatal(err)
	}
	email, pw, admin := "b@example.org", "Ben-pass2", true
	groups := []string{auth.ReadersGroup}
	if err := s.PatchUser(ctx, "ben", identity.UserPatch{
		Email: &email, Password: &pw, Admin: &admin, Groups: &groups,
	}); err != nil {
		t.Fatal(err)
	}
	ben, err := s.User(ctx, "ben")
	if err != nil || ben.Email != email || !ben.Admin || ben.PasswordExpired ||
		!slices.Equal(ben.Groups, groups) {
		t.Fatalf("patched: %+v %v", ben, err)
	}
	empty := ""
	wantInvalid(t, "empty email", s.PatchUser(ctx, "ben", identity.UserPatch{Email: &empty}))
	wantInvalid(t, "empty password", s.PatchUser(ctx, "ben", identity.UserPatch{Password: &empty}))
	nope := []string{"nope"}
	wantInvalid(t, "unknown group", s.PatchUser(ctx, "ben", identity.UserPatch{Groups: &nope}))
	if err := s.PatchUser(ctx, "nobody", identity.UserPatch{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
}

func TestLastAdminAndDeletion(t *testing.T) {
	ctx := context.Background()
	s, db := newService(t)
	no := false
	wantInvalid(
		t,
		"demote the last admin",
		s.PatchUser(ctx, auth.AdminUser, identity.UserPatch{Admin: &no}),
	)
	wantInvalid(t, "delete admin", s.DeleteUser(ctx, auth.AdminUser))
	wantInvalid(t, "delete anonymous", s.DeleteUser(ctx, auth.AnonymousUser))

	boss := newUser("boss")
	boss.Admin = true
	if _, err := s.PutUser(ctx, boss, "Boss-pass1"); err != nil {
		t.Fatal(err)
	}
	// With two admins, either may be demoted, but not both.
	boss.Admin = false
	if _, err := s.PutUser(ctx, boss, "Boss-pass1"); err != nil {
		t.Fatalf("demoting one of two admins: %v", err)
	}
	boss.Admin = true
	if _, err := s.PutUser(ctx, boss, "Boss-pass1"); err != nil {
		t.Fatal(err)
	}

	// Deleting a user revokes its tokens and nobody else's.
	for _, tok := range []types.Token{
		{ID: "t1", Subject: "jfac@svc/users/boss"},
		{ID: "t2", Subject: "jfac@svc/users/admin"},
	} {
		if err := db.Tokens().Create(ctx, tok); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteUser(ctx, "boss"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Tokens().Get(ctx, "t1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("boss's token survived: %v", err)
	}
	if _, err := db.Tokens().Get(ctx, "t2"); err != nil {
		t.Fatalf("admin's token was revoked: %v", err)
	}
	if err := s.DeleteUser(ctx, "boss"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleting twice: %v", err)
	}
	users, err := s.Users(ctx)
	if err != nil || len(users) != 2 {
		t.Fatalf("users: %v %v", users, err)
	}
}

func TestGroups(t *testing.T) {
	ctx := context.Background()
	s, db := newService(t)
	if _, err := s.PutUser(ctx, newUser("cat"), "Cat-pass1"); err != nil {
		t.Fatal(err)
	}
	members := []string{"cat", "cat"}
	created, err := s.PutGroup(ctx, types.Group{Name: "devs", Description: "d"}, &members)
	if err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	// A replacement without members keeps them.
	created, err = s.PutGroup(
		ctx,
		types.Group{Name: "devs", Description: "e", AdminPrivileges: true},
		nil,
	)
	if err != nil || created {
		t.Fatalf("replace: %v %v", created, err)
	}
	g, err := s.Group(ctx, "devs")
	if err != nil || g.Description != "e" || !g.AdminPrivileges ||
		!slices.Equal(g.Members, []string{"cat"}) {
		t.Fatalf("replaced: %+v %v", g, err)
	}
	desc, none := "f", []string{}
	if err := s.PatchGroup(
		ctx,
		"devs",
		identity.GroupPatch{Description: &desc, Members: &none},
	); err != nil {
		t.Fatal(err)
	}
	if g, err := s.Group(ctx, "devs"); err != nil || g.Description != "f" || len(g.Members) != 0 {
		t.Fatalf("patched: %+v %v", g, err)
	}
	ghost := []string{"ghost"}
	wantInvalid(
		t,
		"unknown member",
		s.PatchGroup(ctx, "devs", identity.GroupPatch{Members: &ghost}),
	)
	_, err = s.PutGroup(ctx, types.Group{Name: "devs"}, &ghost)
	wantInvalid(t, "unknown member on put", err)
	_, err = s.PutGroup(ctx, types.Group{Name: ""}, nil)
	wantInvalid(t, "no name", err)
	if err := s.PatchGroup(
		ctx,
		"nobody",
		identity.GroupPatch{},
	); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("unknown group: %v", err)
	}
	if err := s.DeleteGroup(ctx, "devs"); err != nil {
		t.Fatal(err)
	}
	// The seeded readers group goes like any other, and its grant with it.
	if err := s.DeleteGroup(ctx, auth.ReadersGroup); err != nil {
		t.Fatalf("delete readers: %v", err)
	}
	groups, err := s.Groups(ctx)
	if err != nil || len(groups) != 0 {
		t.Fatalf("groups: %v %v", groups, err)
	}
	anything, err := db.Permissions().Get(ctx, auth.AnythingPermission)
	if err != nil || len(anything.Resources[types.ResourceArtifact].Actions.Groups) != 0 {
		t.Fatalf("Anything after deleting readers: %+v %v", anything, err)
	}
	if err := s.DeleteGroup(ctx, "devs"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete an unknown group: %v", err)
	}
}

func TestLocksAndExpiry(t *testing.T) {
	ctx := context.Background()
	s, db := newService(t)
	if _, err := s.PutUser(ctx, newUser("dan"), "Dan-pass1"); err != nil {
		t.Fatal(err)
	}
	unknown, err := s.ExpirePasswords(ctx, []string{"dan", "ghost", "dan"})
	if err != nil || !slices.Equal(unknown, []string{"ghost"}) {
		t.Fatalf("expire: %v %v", unknown, err)
	}
	if dan, _ := s.User(ctx, "dan"); !dan.PasswordExpired {
		t.Fatal("dan's password was not expired")
	}
	if err := s.UnexpirePassword(ctx, "dan"); err != nil {
		t.Fatal(err)
	}
	if dan, _ := s.User(ctx, "dan"); dan.PasswordExpired || !dan.PasswordChanged.Equal(clock) {
		t.Fatalf("unexpired: %+v", dan)
	}
	if err := s.UnexpirePassword(ctx, "ghost"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unexpire an unknown user: %v", err)
	}

	if _, err := db.Users().RecordFailedLogin(ctx, "dan", 1); err != nil {
		t.Fatal(err)
	}
	if locked, err := s.Locked(ctx); err != nil || !slices.Equal(locked, []string{"dan"}) {
		t.Fatalf("locked: %v %v", locked, err)
	}
	wantInvalid(t, "unlock nobody", s.Unlock(ctx, nil))
	if err := s.Unlock(ctx, []string{"dan"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Users().RecordFailedLogin(ctx, "dan", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.UnlockAll(ctx); err != nil {
		t.Fatal(err)
	}
	if locked, _ := s.Locked(ctx); len(locked) != 0 {
		t.Fatalf("still locked: %v", locked)
	}
}

func TestPolicies(t *testing.T) {
	ctx := context.Background()
	s, db := newService(t)
	lock, err := s.LockPolicy(ctx)
	if err != nil || lock.Enabled || lock.LoginAttempts != auth.DefaultLoginAttempts {
		t.Fatalf("default lock policy: %+v %v", lock, err)
	}
	if err := s.SetLockPolicy(ctx, types.UserLockPolicy{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if lock, _ := s.LockPolicy(
		ctx,
	); !lock.Enabled ||
		lock.LoginAttempts != auth.DefaultLoginAttempts {
		t.Fatalf("a zero limit takes the default: %+v", lock)
	}
	wantInvalid(
		t,
		"a negative limit",
		s.SetLockPolicy(ctx, types.UserLockPolicy{LoginAttempts: -1}),
	)
	// Disabling the policy unlocks everyone.
	if _, err := db.Users().RecordFailedLogin(ctx, auth.AdminUser, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLockPolicy(ctx, types.UserLockPolicy{}); err != nil {
		t.Fatal(err)
	}
	if locked, _ := s.Locked(ctx); len(locked) != 0 {
		t.Fatalf("disabling the policy kept locks: %v", locked)
	}

	exp, err := s.ExpirationPolicy(ctx)
	if err != nil || exp.Enabled || exp.MaxAgeDays != auth.DefaultPasswordMaxAgeDays ||
		!exp.NotifyByEmail {
		t.Fatalf("default expiration policy: %+v %v", exp, err)
	}
	stored, err := s.SetExpirationPolicy(
		ctx,
		types.PasswordExpirationPolicy{Enabled: true, MaxAgeDays: 30},
	)
	if err != nil || stored.MaxAgeDays != 30 {
		t.Fatalf("set: %+v %v", stored, err)
	}
	_, err = s.SetExpirationPolicy(ctx, types.PasswordExpirationPolicy{})
	wantInvalid(t, "a zero age", err)
}

// TestAccessLockPolicy: the Access policy reads and writes the lock and
// expiration policies, keeps their limits while it turns them off, and
// stores the rest beside them.
func TestAccessLockPolicy(t *testing.T) {
	ctx := context.Background()
	s, db := newService(t)
	got, err := s.AccessLockPolicy(ctx)
	if err != nil ||
		got != (identity.AccessLockPolicy{AccessLockSettings: types.DefaultAccessLockSettings()}) {
		t.Fatalf("defaults: %+v %v", got, err)
	}
	want := identity.AccessLockPolicy{
		Attempts:           3,
		PasswordExpiryDays: 30,
		AccessLockSettings: types.AccessLockSettings{
			SecondsToUnlock:        60,
			AdminPasswordExpirable: true,
		},
	}
	if stored, err := s.SetAccessLockPolicy(ctx, want); err != nil || stored != want {
		t.Fatalf("set: %+v %v", stored, err)
	}
	if got, _ := s.AccessLockPolicy(ctx); got != want {
		t.Fatalf("read back: %+v", got)
	}
	lock, _ := s.LockPolicy(ctx)
	expiry, _ := s.ExpirationPolicy(ctx)
	if !lock.Enabled || lock.LoginAttempts != 3 || !expiry.Enabled || expiry.MaxAgeDays != 30 {
		t.Fatalf("the Artifactory policies: %+v %+v", lock, expiry)
	}
	// 0 turns both off, keeps their limits, and unlocks everyone.
	if _, err := db.Users().RecordFailedLogin(ctx, auth.AdminUser, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAccessLockPolicy(ctx, identity.AccessLockPolicy{}); err != nil {
		t.Fatal(err)
	}
	lock, _ = s.LockPolicy(ctx)
	expiry, _ = s.ExpirationPolicy(ctx)
	if lock.Enabled || lock.LoginAttempts != 3 || expiry.Enabled || expiry.MaxAgeDays != 30 {
		t.Fatalf("turned off: %+v %+v", lock, expiry)
	}
	if locked, _ := s.Locked(ctx); len(locked) != 0 {
		t.Fatalf("turning locking off kept locks: %v", locked)
	}
	for name, p := range map[string]identity.AccessLockPolicy{
		"negative attempts": {Attempts: -1},
		"too many attempts": {Attempts: 101},
		"negative unlock":   {AccessLockSettings: types.AccessLockSettings{SecondsToUnlock: -1}},
		"negative expiry":   {PasswordExpiryDays: -1},
	} {
		_, err := s.SetAccessLockPolicy(ctx, p)
		wantInvalid(t, name, err)
	}
}

// TestEncryptedPasswordFollowsThePassword: setting or expiring a password
// revokes that user's encrypted password and nothing else.
func TestEncryptedPasswordFollowsThePassword(t *testing.T) {
	ctx := context.Background()
	s, db := newService(t)
	for _, n := range []string{"dan", "erin"} {
		if _, err := s.PutUser(ctx, newUser(n), "Pass-"+n+"1"); err != nil {
			t.Fatal(err)
		}
	}
	encrypted := func(id, sub string) {
		t.Helper()
		if err := db.Tokens().Create(ctx, types.Token{
			ID: id, Subject: sub, Purpose: types.TokenPurposeEncryptedPassword, Secret: id,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// More than a page of other users' encrypted passwords, so the
	// revocation reads every page.
	for i := range store.DefaultLimit {
		encrypted(fmt.Sprintf("ep-other-%03d", i), fmt.Sprintf("jfac@svc/users/other%03d", i))
	}
	encrypted("ep-bad", "nonsense")
	encrypted("ep-erin", "jfac@svc/users/erin")
	if err := db.Tokens().
		Create(ctx, types.Token{ID: "t-dan", Subject: "jfac@svc/users/dan"}); err != nil {
		t.Fatal(err)
	}
	exists := func(id string) bool {
		_, err := db.Tokens().Get(ctx, id)
		return err == nil
	}
	email := "dan@example.org"
	pw := "Pass-dan2"
	for what, change := range map[string]func() error{
		"patch": func() error { return s.PatchUser(ctx, "dan", identity.UserPatch{Password: &pw}) },
		"put":   func() error { _, err := s.PutUser(ctx, newUser("dan"), "Pass-dan3"); return err },
		"expire": func() error {
			_, err := s.ExpirePasswords(ctx, []string{"dan"})
			return err
		},
	} {
		encrypted("ep-dan", "jfac@svc/users/dan")
		// A change that leaves the password alone keeps it.
		if err := s.PatchUser(
			ctx,
			"dan",
			identity.UserPatch{Email: &email},
		); err != nil ||
			!exists("ep-dan") {
			t.Fatalf("%s: an email change revoked the encrypted password (%v)", what, err)
		}
		if err := change(); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if exists("ep-dan") {
			t.Errorf("%s: dan's encrypted password survived", what)
		}
		for _, id := range []string{"ep-erin", "ep-bad", "t-dan", "ep-other-000", "ep-other-099"} {
			if !exists(id) {
				t.Errorf("%s: %s was revoked", what, id)
			}
		}
		if _, err := db.Tokens().Get(ctx, "ep-dan"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%s: %v", what, err)
		}
	}
	if _, err := s.ExpirePasswords(ctx, []string{"ghost"}); err != nil || !exists("ep-erin") {
		t.Fatalf("expiring nobody: %v", err)
	}
}
