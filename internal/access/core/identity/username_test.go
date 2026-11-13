package identity_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/identity"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// TestUnitUsernamesFold: every method that takes a user name folds it
// (#1432), so the store holds lower-case names only and any spelling finds
// its user.
func TestUnitUsernamesFold(t *testing.T) {
	ctx := context.Background()
	s, db := newService(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	members := func(name string) []string {
		t.Helper()
		g, err := db.Groups().Get(ctx, name)
		must(err)
		return g.Members
	}
	created, err := s.PutUser(ctx, newUser("Dan"), "Dan-pass1")
	must(err)
	if !created {
		t.Fatal("Dan was not created")
	}
	if created, err := s.PutUser(ctx, newUser("DAN"), "Dan-pass2"); err != nil || created {
		t.Fatalf("DAN replaces dan: created %v, %v", created, err)
	}
	if _, err := db.Users().Get(ctx, "Dan"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("the store holds Dan as written: %v", err)
	}
	must(s.CreateUser(ctx, newUser("Bob"), "Bob-pass1"))
	if err := s.CreateUser(ctx, newUser("BOB"), "Bob-pass1"); !errors.Is(err, identity.ErrExists) {
		t.Fatalf("BOB beside bob: %v", err)
	}
	u, err := s.User(ctx, "BOB")
	must(err)
	if u.Name != "bob" {
		t.Fatalf("BOB is %q", u.Name)
	}
	email := "b2@example.com"
	must(s.PatchUser(ctx, "BOB", identity.UserPatch{Email: &email}))

	_, err = s.PutGroup(ctx, model.Group{Name: "crew"}, &[]string{"BOB", "Dan", "bob"})
	must(err)
	if got := members("crew"); !slices.Equal(got, []string{"bob", "dan"}) {
		t.Fatalf("crew %v", got)
	}
	must(s.PatchGroup(ctx, "crew", identity.GroupPatch{Members: &[]string{"DAN"}}))
	if got := members("crew"); !slices.Equal(got, []string{"dan"}) {
		t.Fatalf("crew after patch %v", got)
	}
	must(s.CreateGroup(ctx, model.Group{Name: "team"}, []string{"BOB"}))
	_, err = s.ReplaceGroup(ctx, model.Group{Name: "team"}, []string{"Dan"})
	must(err)
	team, err := s.UpdateGroupMembers(ctx, "team", []string{"BOB"}, []string{"DAN"})
	must(err)
	if !slices.Equal(team, []string{"bob"}) {
		t.Fatalf("team members %v", team)
	}
	if got, err := s.UpdateUserGroups(ctx, "DAN", []string{"crew"}, nil); err != nil ||
		!slices.Contains(got, "crew") {
		t.Fatalf("dan's groups %v, %v", got, err)
	}

	must(s.SetPassword(ctx, "BOB", "Bob-pass2"))
	must(s.ChangePassword(ctx, "Bob", "Bob-pass2", "Bob-pass3", "Bob-pass3"))
	unknown, err := s.ExpirePasswords(ctx, []string{"BOB", "Ghost"})
	must(err)
	if !slices.Equal(unknown, []string{"Ghost"}) {
		t.Fatalf("unknown %v, want the request's spelling", unknown)
	}
	if u, _ := db.Users().Get(ctx, "bob"); !u.PasswordExpired {
		t.Fatal("expiring BOB left bob's password")
	}
	must(s.UnexpirePassword(ctx, "BOB"))
	if u, _ := db.Users().Get(ctx, "bob"); u.PasswordExpired {
		t.Fatal("unexpiring BOB left bob's password expired")
	}
	must(s.Unlock(ctx, []string{"BOB"}))

	for _, name := range []string{"ADMIN", "Anonymous"} {
		if err := s.DeleteUser(ctx, name); !errors.Is(err, identity.ErrProtected) {
			t.Errorf("delete %s: %v", name, err)
		}
	}
	must(s.DeleteUser(ctx, "DAN"))
	if _, err := s.User(ctx, "dan"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("dan after deleting DAN: %v", err)
	}
}

// TestUnitUsernameTokensRevoked: deleting a user revokes the tokens of its
// subject in any case, as a token an administrator minted for BOB before
// bob existed acts as bob.
func TestUnitUsernameTokensRevoked(t *testing.T) {
	ctx := context.Background()
	s, db := newService(t)
	if _, err := s.PutUser(ctx, newUser("bob"), "Bob-pass1"); err != nil {
		t.Fatal(err)
	}
	for id, subject := range map[string]string{
		"upper": "jfac@svc/users/BOB", "lower": "jfac@svc/users/bob", "other": "jfac@svc/users/bobby",
		"group": "jfac@svc/groups/bob",
	} {
		if err := db.Tokens().Create(ctx, model.Token{ID: id, Subject: subject}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteUser(ctx, "Bob"); err != nil {
		t.Fatal(err)
	}
	for id, kept := range map[string]bool{"upper": false, "lower": false, "other": true, "group": true} {
		if _, err := db.Tokens().Get(ctx, id); (err == nil) != kept {
			t.Errorf("token %s: %v, kept %v", id, err, kept)
		}
	}
}

// TestUnitUnknownGroupOrMember: the service words an unknown group in a user
// write, and an unknown member in a legacy group write, itself (#1433), and
// writes nothing. The first unknown name, in name order, is named as the
// request spells it.
func TestUnitUnknownGroupOrMember(t *testing.T) {
	ctx := context.Background()
	s, db := newService(t)
	want := func(err error, msg string) {
		t.Helper()
		// The handlers answer the text after the sentinel.
		if !errors.Is(err, identity.ErrInvalid) ||
			!strings.HasSuffix(err.Error(), identity.ErrInvalid.Error()+": "+msg) {
			t.Errorf("%v, want ErrInvalid %q", err, msg)
		}
	}
	noGroup := "Group 'nope' does not exist"
	_, err := s.PutUser(ctx, newUser("eve", "zeta", "nope"), "Eve-pass1")
	want(err, noGroup)
	want(s.CreateUser(ctx, newUser("eve", "nope"), "Eve-pass1"), noGroup)
	if _, err := db.Users().Get(ctx, "eve"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("eve was created: %v", err)
	}
	if _, err := s.PutUser(ctx, newUser("eve"), "Eve-pass1"); err != nil {
		t.Fatal(err)
	}
	want(s.PatchUser(ctx, "eve", identity.UserPatch{Groups: &[]string{"nope"}}), noGroup)
	_, err = s.UpdateUserGroups(ctx, "eve", []string{"readers", "nope"}, nil)
	want(err, noGroup)
	_, err = s.PutGroup(ctx, model.Group{Name: "gang"}, &[]string{"eve", "Ghost"})
	want(err, "User 'Ghost' does not exist")
	want(s.PatchGroup(ctx, "readers", identity.GroupPatch{Members: &[]string{"Ghost"}}),
		"User 'Ghost' does not exist")
	if _, err := db.Groups().Get(ctx, "gang"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("gang was created: %v", err)
	}
}

// TestUnitUnknownGroupStoreFailures: a store that fails the existence check
// fails the write with its error, not the 400.
func TestUnitUnknownGroupStoreFailures(t *testing.T) {
	ctx := context.Background()
	_, db := newService(t)
	f := &faulty{Store: db}
	s := &identity.Service{Store: f, Hash: cheap}
	if _, err := s.PutUser(ctx, newUser("eve"), "Eve-pass1"); err != nil {
		t.Fatal(err)
	}
	for op, call := range map[string]func() error{
		"groups.get": func() error {
			return s.PatchUser(ctx, "eve", identity.UserPatch{Groups: &[]string{"readers"}})
		},
		"users.get": func() error {
			return s.PatchGroup(ctx, "readers", identity.GroupPatch{Members: &[]string{"eve"}})
		},
		"users.update": func() error {
			_, err := s.UpdateUserGroups(ctx, "eve", []string{"readers"}, nil)
			return err
		},
	} {
		f.fail = op
		if err := call(); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v, want the store's error", op, err)
		}
	}
}
