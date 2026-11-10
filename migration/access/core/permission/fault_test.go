package permission_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/access/core/permission"
	"github.com/weaveplatform/artifactweave/migration/access/core/port"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
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

func (p faultyPerms) Get(ctx context.Context, name string) (types.Permission, error) {
	if p.f.fail == "perms.get" {
		return types.Permission{}, errBoom
	}
	return p.PermissionStore.Get(ctx, name) //nolint:wrapcheck // test double
}

func (p faultyPerms) List(ctx context.Context, pg kernel.Page) ([]types.Permission, string, error) {
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
) ([]types.Permission, error) {
	if p.f.fail == "perms.forprincipal" {
		return nil, errBoom
	}
	return p.PermissionStore.ForPrincipal(ctx, user, groups) //nolint:wrapcheck // test double
}

type faultyUsers struct {
	port.UserStore
	f *faulty
}

func (u faultyUsers) Get(ctx context.Context, name string) (types.User, error) {
	if u.f.fail == "users.get" {
		return types.User{}, errBoom
	}
	return u.UserStore.Get(ctx, name) //nolint:wrapcheck // test double
}

func (u faultyUsers) List(ctx context.Context, pg kernel.Page) ([]types.User, string, error) {
	if u.f.fail == "users.list" {
		return nil, "", errBoom
	}
	return u.UserStore.List(ctx, pg) //nolint:wrapcheck // test double
}

type faultyGroups struct {
	port.GroupStore
	f *faulty
}

func (g faultyGroups) Get(ctx context.Context, name string) (types.Group, error) {
	if g.f.fail == "groups.get" {
		return types.Group{}, errBoom
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
	grants := perm("g", map[types.ResourceType]types.Resource{
		types.ResourceArtifact: artifact(everywhere("libs-local"),
			map[string]types.Actions{"bob": r}, map[string]types.Actions{"devs": r}),
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
		"users.list":         func() error { _, err := svc.FoldUsers(ctx, []string{"bob"}); return err },
	} {
		f.fail = op
		if err := call(); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", op, err)
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
