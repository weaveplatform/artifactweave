package identity_test

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/service/identity"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var errBoom = errors.New("boom")

// faulty fails the store operation named in fail.
type faulty struct {
	*inmem.Store
	fail string
}

func (f *faulty) Users() store.UserStore        { return faultyUsers{f.Store.Users(), f} }
func (f *faulty) Groups() store.GroupStore      { return faultyGroups{f.Store.Groups(), f} }
func (f *faulty) Tokens() store.TokenStore      { return faultyTokens{f.Store.Tokens(), f} }
func (f *faulty) Settings() store.SettingsStore { return faultySettings{f.Store.Settings(), f} }
func (f *faulty) failing(op string) bool        { return f.fail == op }

type faultyUsers struct {
	store.UserStore
	f *faulty
}

func (u faultyUsers) Get(ctx context.Context, name string) (types.User, error) {
	if u.f.failing("users.get") {
		return types.User{}, errBoom
	}
	return u.UserStore.Get(ctx, name) //nolint:wrapcheck // test double
}

func (u faultyUsers) List(ctx context.Context, p store.Page) ([]types.User, string, error) {
	if u.f.failing("users.list") {
		return nil, "", errBoom
	}
	return u.UserStore.List(ctx, p) //nolint:wrapcheck // test double
}

func (u faultyUsers) Update(ctx context.Context, x types.User) error {
	if u.f.failing("users.update") {
		return errBoom
	}
	return u.UserStore.Update(ctx, x) //nolint:wrapcheck // test double
}

func (u faultyUsers) Unlock(ctx context.Context, names ...string) error {
	if u.f.failing("users.unlock") {
		return errBoom
	}
	return u.UserStore.Unlock(ctx, names...) //nolint:wrapcheck // test double
}

type faultyGroups struct {
	store.GroupStore
	f *faulty
}

func (g faultyGroups) Get(ctx context.Context, name string) (types.Group, error) {
	if g.f.failing("groups.get") {
		return types.Group{}, errBoom
	}
	return g.GroupStore.Get(ctx, name) //nolint:wrapcheck // test double
}

func (g faultyGroups) AutoJoin(ctx context.Context) ([]string, error) {
	if g.f.failing("groups.autojoin") {
		return nil, errBoom
	}
	return g.GroupStore.AutoJoin(ctx) //nolint:wrapcheck // test double
}

type faultyTokens struct {
	store.TokenStore
	f *faulty
}

func (t faultyTokens) List(
	ctx context.Context,
	f store.TokenFilter,
	p store.Page,
) ([]types.Token, string, error) {
	if t.f.failing("tokens.list") {
		return nil, "", errBoom
	}
	return t.TokenStore.List(ctx, f, p) //nolint:wrapcheck // test double
}

func (t faultyTokens) Delete(ctx context.Context, id string) error {
	if t.f.failing("tokens.delete") {
		return errBoom
	}
	return t.TokenStore.Delete(ctx, id) //nolint:wrapcheck // test double
}

type faultySettings struct {
	store.SettingsStore
	f *faulty
}

func (s faultySettings) Put(ctx context.Context, key string, v any) error {
	if s.f.failing("settings.put") {
		return errBoom
	}
	return s.SettingsStore.Put(ctx, key, v) //nolint:wrapcheck // test double
}

func TestStoreFailures(t *testing.T) {
	ctx := context.Background()
	_, db := newService(t)
	f := &faulty{Store: db}
	s := &identity.Service{Store: f, Hash: cheap}
	if _, err := s.PutUser(ctx, newUser("eve"), "Eve-pass1"); err != nil {
		t.Fatal(err)
	}
	if err := db.Tokens().
		Create(ctx, types.Token{ID: "t", Subject: "jfac@svc/users/eve"}); err != nil {
		t.Fatal(err)
	}
	boss := newUser("boss")
	boss.Admin = true
	no := false
	for op, call := range map[string]func() error{
		"users.get": func() error { _, err := s.PutUser(ctx, newUser("x"), "pw"); return err },
		"groups.autojoin": func() error {
			_, err := s.PutUser(ctx, newUser("x"), "pw")
			return err
		},
		"users.list": func() error {
			return s.PatchUser(ctx, auth.AdminUser, identity.UserPatch{Admin: &no})
		},
		"tokens.list":   func() error { return s.DeleteUser(ctx, "eve") },
		"tokens.delete": func() error { return s.DeleteUser(ctx, "eve") },
		"groups.get":    func() error { _, err := s.PutGroup(ctx, types.Group{Name: "g"}, nil); return err },
		"users.update":  func() error { _, err := s.ExpirePasswords(ctx, []string{"eve"}); return err },
		"users.unlock":  func() error { return s.SetLockPolicy(ctx, types.UserLockPolicy{}) },
		"settings.put": func() error {
			_, err := s.SetExpirationPolicy(ctx, types.PasswordExpirationPolicy{MaxAgeDays: 1})
			return err
		},
	} {
		f.fail = op
		if err := call(); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v, want the store's error", op, err)
		}
	}
	f.fail = "users.get"
	if _, err := s.ExpirePasswords(ctx, []string{"eve"}); !errors.Is(err, errBoom) {
		t.Errorf("expire with a failing read: %v", err)
	}
	f.fail = "users.list"
	if _, err := s.Users(ctx); !errors.Is(err, errBoom) {
		t.Errorf("list users: %v", err)
	}
	// keepAnAdmin reads every user: a replacement that demotes an admin
	// fails with the store.
	f.fail = ""
	if _, err := s.PutUser(ctx, boss, "Boss-pass1"); err != nil {
		t.Fatal(err)
	}
	boss.Admin = false
	f.fail = "users.list"
	if _, err := s.PutUser(ctx, boss, "Boss-pass1"); !errors.Is(err, errBoom) {
		t.Errorf("demote with a failing list: %v", err)
	}
	f.fail = "users.get"
	if err := s.DeleteUser(ctx, "boss"); !errors.Is(err, errBoom) {
		t.Errorf("delete with a failing read: %v", err)
	}
}
