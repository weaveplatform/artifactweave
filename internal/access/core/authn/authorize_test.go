package authn

import (
	"context"
	"errors"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/authz"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
)

type failingPermissions struct{ port.PermissionStore }

func (failingPermissions) ForPrincipal(
	context.Context,
	string,
	[]string,
) ([]model.Permission, error) {
	return nil, errors.New("database down")
}

func TestUnitAuthorize(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	if err := db.Groups().
		Create(ctx, model.Group{Name: "admins", AdminPrivileges: true}); err != nil {
		t.Fatal(err)
	}
	src := authz.Sources{Permissions: db.Permissions(), Groups: db.Groups()}
	// A password principal carries no scope, and an admin-privilege group
	// makes its member an administrator.
	p := principalOf(model.User{Name: "ann", Groups: []string{"admins"}})
	if accessapi.SubjectOf(p).Scope != nil {
		t.Fatalf("a password principal has no scope: %+v", accessapi.SubjectOf(p))
	}
	got, d, err := Authorize(ctx, src, p)
	if err != nil || !got.Admin || d == nil || !d.IsAdmin() {
		t.Fatalf("admin through a group: %+v, %v", got, err)
	}
	if _, d, err := Authorize(
		ctx,
		authz.Sources{Permissions: failingPermissions{}, Groups: db.Groups()},
		p,
	); err == nil || d != nil {
		t.Fatal("a failing permission store")
	}
}
