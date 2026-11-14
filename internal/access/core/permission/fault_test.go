package permission_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/permission"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

var errBoom = errors.New("boom")

// faulty fails the store operation named in fail.
type faulty struct {
	*inmem.Store
	fail string
}

func (f *faulty) Permissions() port.PermissionStore {
	return faultyPerms{f.Store.Permissions(), f}
}
func (f *faulty) Users() port.UserStore   { return faultyUsers{f.Store.Users(), f} }
func (f *faulty) Groups() port.GroupStore { return faultyGroups{f.Store.Groups(), f} }

type faultyPerms struct {
	port.PermissionStore
	f *faulty
}

func (p faultyPerms) Get(ctx context.Context, name string) (model.Permission, error) {
	if p.f.fail == "perms.get" {
		return model.Permission{}, errBoom
	}
	return p.PermissionStore.Get(ctx, name) //nolint:wrapcheck // test double
}

func (p faultyPerms) List(ctx context.Context, pg kernel.Page) ([]model.Permission, string, error) {
	if p.f.fail == "perms.list" {
		return nil, "", errBoom
	}
	return p.PermissionStore.List(ctx, pg) //nolint:wrapcheck // test double
}

func (p faultyPerms) Delete(ctx context.Context, name string) error {
	if p.f.fail == "perms.delete" {
		return errBoom
	}
	return p.PermissionStore.Delete(ctx, name) //nolint:wrapcheck // test double
}

func (p faultyPerms) ForPrincipal(
	ctx context.Context,
	user string,
	groups []string,
) ([]model.Permission, error) {
	if p.f.fail == "perms.forprincipal" {
		return nil, errBoom
	}
	return p.PermissionStore.ForPrincipal(ctx, user, groups) //nolint:wrapcheck // test double
}

func (p faultyPerms) Page(
	ctx context.Context,
	q port.PermissionQuery,
) ([]model.Permission, bool, error) {
	if p.f.fail == "perms.page" {
		return nil, false, errBoom
	}
	return p.PermissionStore.Page(ctx, q) //nolint:wrapcheck // test double
}

type faultyUsers struct {
	port.UserStore
	f *faulty
}

func (u faultyUsers) Get(ctx context.Context, name string) (model.User, error) {
	if u.f.fail == "users.get" {
		return model.User{}, errBoom
	}
	return u.UserStore.Get(ctx, name) //nolint:wrapcheck // test double
}

func (u faultyUsers) List(ctx context.Context, pg kernel.Page) ([]model.User, string, error) {
	if u.f.fail == "users.list" {
		return nil, "", errBoom
	}
	return u.UserStore.List(ctx, pg) //nolint:wrapcheck // test double
}

type faultyGroups struct {
	port.GroupStore
	f *faulty
}

func (g faultyGroups) Get(ctx context.Context, name string) (model.Group, error) {
	if g.f.fail == "groups.get" {
		return model.Group{}, errBoom
	}
	return g.GroupStore.Get(ctx, name) //nolint:wrapcheck // test double
}

func TestUnitStoreFailures(t *testing.T) {
	ctx := context.Background()
	f := &faulty{Store: seeded(t)}
	svc := permission.Service{
		Store:        f,
		Repositories: repositories{db: f.Store, fail: &f.fail},
		Now:          func() time.Time { return clock },
	}
	grants := perm("g", map[api.ResourceType]model.Resource{
		api.ResourceArtifact: artifact(everywhere("libs-local"),
			map[string]api.Actions{"bob": r}, map[string]api.Actions{"devs": r}),
	})
	if err := svc.Create(ctx, grants); err != nil {
		t.Fatal(err)
	}
	for op, call := range map[string]func() error{
		"perms.list": func() error { _, err := svc.List(ctx); return err },
		"perms.get":  func() error { _, err := svc.Get(ctx, "g"); return err },
		"perms.delete": func() error {
			return svc.Delete(ctx, "g")
		},
		"perms.forprincipal": func() error { _, _, err := svc.Granting(ctx, []string{"bob"}, nil, false); return err },
		"users.get":          func() error { _, _, err := svc.Granting(ctx, []string{"bob"}, nil, false); return err },
		"groups.get":         func() error { _, _, err := svc.Granting(ctx, nil, []string{"devs"}, false); return err },
		"perms.page":         func() error { _, _, err := svc.Page(ctx, "", 10, nil); return err },
	} {
		f.fail = op
		if err := call(); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", op, err)
		}
	}
	f.fail = "users.get"
	if _, err := svc.FoldUsers(ctx, []string{"bob"}); !errors.Is(err, errBoom) {
		t.Errorf("folding users with a failing read: %v", err)
	}
	for op, call := range map[string]func() error{
		"users.get":  func() error { _, err := svc.PrincipalsOf(ctx, []string{"bob"}, nil, true); return err },
		"groups.get": func() error { _, err := svc.PrincipalsOf(ctx, nil, []string{"devs"}, false); return err },
	} {
		f.fail = op
		if err := call(); !errors.Is(err, errBoom) {
			t.Errorf("principals of with %s failing: %v", op, err)
		}
	}
	f.fail = "perms.forprincipal"
	if _, _, err := svc.Granting(ctx, nil, []string{"devs"}, false); !errors.Is(err, errBoom) {
		t.Errorf("group lookup with failing permissions: %v", err)
	}
	// Writes: the read inside the transaction and validation's lookups.
	for _, op := range []string{"perms.get", "users.get", "groups.get", "repos.get"} {
		f.fail = op
		if _, err := svc.Put(ctx, grants); !errors.Is(err, errBoom) {
			t.Errorf("put with %s failing: %v", op, err)
		}
	}
	f.fail = "perms.get"
	if err := svc.Create(ctx, perm("other", nil)); !errors.Is(err, errBoom) {
		t.Errorf("create with a failing read: %v", err)
	}
	f.fail = "users.get"
	if err := svc.Create(ctx, grants); !errors.Is(err, errBoom) {
		t.Errorf("create with a failing validation: %v", err)
	}
}
