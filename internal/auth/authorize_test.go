package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type failingPermissions struct{ store.PermissionStore }

func (failingPermissions) ForPrincipal(
	context.Context,
	string,
	[]string,
) ([]types.Permission, error) {
	return nil, errors.New("database down")
}

func TestAuthorize(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	if err := db.Groups().
		Create(ctx, types.Group{Name: "admins", AdminPrivileges: true}); err != nil {
		t.Fatal(err)
	}
	src := authz.Sources{Permissions: db.Permissions(), Groups: db.Groups()}
	// A password principal carries no scope, and an admin-privilege group
	// makes its member an administrator.
	p := principalOf(types.User{Name: "ann", Groups: []string{"admins"}})
	if p.Subject().Scope != nil {
		t.Fatalf("a password principal has no scope: %+v", p.Subject())
	}
	got, err := Authorize(ctx, src, p)
	if err != nil || !got.Admin || got.Access() == nil || !got.Access().Admin() {
		t.Fatalf("admin through a group: %+v, %v", got, err)
	}
	if _, err := Authorize(
		ctx,
		authz.Sources{Permissions: failingPermissions{}, Groups: db.Groups()},
		p,
	); err == nil {
		t.Fatal("a failing permission store")
	}
}
