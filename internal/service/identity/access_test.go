package identity_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/service/identity"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestCreateOnly(t *testing.T) {
	ctx := context.Background()
	s, db := newService(t)
	if err := s.CreateGroup(
		ctx,
		types.Group{Name: "devs", AutoJoin: true},
		[]string{"admin", "admin"},
	); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateGroup(
		ctx,
		types.Group{Name: "devs"},
		nil,
	); !errors.Is(
		err,
		identity.ErrExists,
	) {
		t.Fatalf("a taken group name: %v", err)
	}
	if err := s.CreateGroup(
		ctx,
		types.Group{Name: "x"},
		[]string{"nobody"},
	); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("a group with an unknown member: %v", err)
	}
	wantInvalid(t, "a group without a name", s.CreateGroup(ctx, types.Group{}, nil))

	if err := s.CreateUser(ctx, newUser("ann"), "Ann-pass1"); err != nil {
		t.Fatal(err)
	}
	u, err := db.Users().Get(ctx, "ann")
	if err != nil || u.Realm != types.RealmInternal || u.Status != types.UserEnabled ||
		!slices.Equal(u.Groups, []string{"devs", "readers"}) || len(u.PasswordHash) == 0 {
		t.Fatalf("created %+v %v", u, err)
	}
	if err := s.CreateUser(ctx, newUser("ann"), "Ann-pass1"); !errors.Is(err, identity.ErrExists) {
		t.Fatalf("a taken user name: %v", err)
	}
	wantInvalid(t, "a user without an email", s.CreateUser(ctx, types.User{Name: "bo"}, "pw"))
	wantInvalid(t, "a user without a password", s.CreateUser(ctx, newUser("bo"), ""))
	wantInvalid(t, "a user without a name", s.CreateUser(ctx, newUser(""), "pw"))
	wantInvalid(
		t,
		"a user in an unknown group",
		s.CreateUser(ctx, newUser("bo", "nope"), "Bo-pass-1"),
	)
	sso := newUser("sso")
	sso.InternalPasswordDisabled = true
	if err := s.CreateUser(ctx, sso, ""); err != nil {
		t.Fatalf("a user without an internal password: %v", err)
	}
}

func TestMemberships(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	for _, g := range []string{"a", "b"} {
		if err := s.CreateGroup(ctx, types.Group{Name: g}, nil); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.UpdateUserGroups(ctx, "admin", []string{"b", "a", "readers"}, []string{"readers"})
	if err != nil || !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("user groups %v %v", got, err)
	}
	if _, err := s.UpdateUserGroups(
		ctx,
		"admin",
		[]string{"nope"},
		nil,
	); !errors.Is(
		err,
		identity.ErrInvalid,
	) {
		t.Fatalf("an unknown group: %v", err)
	}
	if _, err := s.UpdateUserGroups(ctx, "nobody", nil, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an unknown user: %v", err)
	}
	got, err = s.UpdateGroupMembers(ctx, "a", []string{"anonymous"}, []string{"admin"})
	if err != nil || !slices.Equal(got, []string{"anonymous"}) {
		t.Fatalf("members %v %v", got, err)
	}
	if _, err := s.UpdateGroupMembers(
		ctx,
		"a",
		[]string{"nobody"},
		nil,
	); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("an unknown member: %v", err)
	}
	if _, err := s.UpdateGroupMembers(ctx, "nope", nil, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an unknown group: %v", err)
	}
}

func TestPasswordPolicyRules(t *testing.T) {
	ctx := context.Background()
	s, db := newService(t)
	p, err := s.PasswordPolicy(ctx)
	if err != nil || p != types.DefaultPasswordPolicy() {
		t.Fatalf("an unset policy is the default: %+v %v", p, err)
	}
	// The default: four characters, not the old password, not the username.
	wantInvalid(t, "too short by default", s.SetPassword(ctx, "admin", "abc"))
	wantInvalid(t, "the old password by default", s.SetPassword(ctx, "admin", "pw"))
	wantInvalid(t, "the username in any case", s.SetPassword(ctx, "admin", "ADMIN"))
	if err := s.SetPassword(ctx, "admin", "abcd"); err != nil {
		t.Fatalf("a default-policy password: %v", err)
	}
	// A record update sending the current password back changes nothing;
	// the password change endpoint calls the same password a reuse.
	same := "abcd"
	if err := s.PatchUser(ctx, "admin", identity.UserPatch{Password: &same}); err != nil {
		t.Fatalf("an update resending the password: %v", err)
	}
	wantInvalid(t, "a change to the same password", s.SetPassword(ctx, "admin", "abcd"))
	wantInvalid(t, "a negative rule", s.SetPasswordPolicy(ctx, types.PasswordPolicy{Digit: -1}))
	if err := s.SetPasswordPolicy(ctx, types.PasswordPolicy{
		Length: 6, Uppercase: 1, Lowercase: 1, Digit: 1, SpecialChar: 1, NotMatchOld: true,
	}); err != nil {
		t.Fatal(err)
	}
	for pw, why := range map[string]string{
		"Aa1!":    "too short",
		"aaaa1!":  "no upper case",
		"AAAA1!":  "no lower case",
		"Aaaaa!":  "no digit",
		"Aaaaa1":  "no special character",
		"Ääää1!x": "", // letters outside ASCII count by case, length in runes
	} {
		err := s.SetPassword(ctx, "admin", pw)
		if why == "" {
			if err != nil {
				t.Errorf("%q: %v", pw, err)
			}
			continue
		}
		wantInvalid(t, why, err)
	}
	wantInvalid(t, "the current password", s.SetPassword(ctx, "admin", "Ääää1!x"))
	wantInvalid(t, "an empty password", s.SetPassword(ctx, "admin", ""))
	if err := s.SetPassword(ctx, "nobody", "Bbbb2?"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an unknown user: %v", err)
	}
	// A stored hash in a format the comparison cannot read is no match.
	u, err := db.Users().Get(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	u.PasswordHash = []byte("not a hash")
	if err := db.Users().Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPassword(ctx, "admin", "Bbbb2?"); err != nil {
		t.Fatalf("over an unreadable hash: %v", err)
	}
}

func TestBulkPasswordExpiry(t *testing.T) {
	ctx := context.Background()
	s, db := newService(t)
	sso := newUser("sso")
	sso.InternalPasswordDisabled = true
	if err := s.CreateUser(ctx, sso, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.ExpireAllPasswords(ctx); err != nil {
		t.Fatal(err)
	}
	// Twice: users already expired are left as they are.
	if err := s.ExpireAllPasswords(ctx); err != nil {
		t.Fatal(err)
	}
	expired := func(name string) bool {
		t.Helper()
		u, err := db.Users().Get(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		return u.PasswordExpired
	}
	if !expired("admin") || expired("sso") {
		t.Fatal("expire all: admin should expire and sso should not")
	}
	if err := s.UnexpireAllPasswords(ctx); err != nil {
		t.Fatal(err)
	}
	u, err := db.Users().Get(ctx, "admin")
	if err != nil || u.PasswordExpired || !u.PasswordChanged.Equal(clock) {
		t.Fatalf("unexpired %+v %v", u, err)
	}
	if _, err := auth.CheckPassword(u.PasswordHash, "pw"); err != nil {
		t.Fatalf("the password survives: %v", err)
	}
}

// TestBulkExpiryRevokesEncryptedPasswords: expiring every password revokes
// the expired users' encrypted passwords, so unexpiring them all cannot
// bring an old one back. A user whose internal password is disabled is not
// expired and keeps its own, which authentication refuses anyway.
func TestBulkExpiryRevokesEncryptedPasswords(t *testing.T) {
	ctx := context.Background()
	s, db := newService(t)
	sso := newUser("sso")
	sso.InternalPasswordDisabled = true
	if err := s.CreateUser(ctx, sso, ""); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"admin", "sso"} {
		if err := db.Tokens().Create(ctx, types.Token{
			ID:      "ep-" + n,
			Subject: "jfac@svc/users/" + n,
			Purpose: types.TokenPurposeEncryptedPassword,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ExpireAllPasswords(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.UnexpireAllPasswords(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Tokens().Get(ctx, "ep-admin"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("admin's encrypted password after expiring every password: %v", err)
	}
	if _, err := db.Tokens().Get(ctx, "ep-sso"); err != nil {
		t.Fatalf("sso's encrypted password: %v", err)
	}

	// A revocation that fails fails the expiry with it.
	_, db2 := newService(t)
	f := &faulty{Store: db2, fail: "tokens.list"}
	if err := (&identity.Service{Store: f, Hash: cheap}).ExpireAllPasswords(
		ctx,
	); !errors.Is(
		err,
		errBoom,
	) {
		t.Fatalf("expire all with the revocation failing: %v", err)
	}
}

func TestAccessStoreFailures(t *testing.T) {
	ctx := context.Background()
	_, db := newService(t)
	f := &faulty{Store: db}
	s := &identity.Service{Store: f, Hash: cheap}
	for op, call := range map[string]func() error{
		"users.list":      func() error { return s.ExpireAllPasswords(ctx) },
		"users.update":    func() error { return s.UnexpireAllPasswords(ctx) },
		"groups.autojoin": func() error { return s.CreateUser(ctx, newUser("x"), "pw") },
		"settings.put":    func() error { return s.SetPasswordPolicy(ctx, types.PasswordPolicy{}) },
		"groups.get": func() error {
			_, err := s.UpdateGroupMembers(ctx, "readers", nil, nil)
			return err
		},
		"users.get": func() error { return s.SetPassword(ctx, "admin", "x") },
	} {
		if op == "users.update" {
			if err := s.ExpireAllPasswords(ctx); err != nil {
				t.Fatal(err)
			}
		}
		f.fail = op
		if err := call(); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v, want the store's error", op, err)
		}
		f.fail = ""
	}
}
