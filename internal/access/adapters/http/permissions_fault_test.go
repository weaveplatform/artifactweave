package accesshttp

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	accessgen "github.com/weaveplatform/artifactweave/internal/platform/gen/access"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// permFault fails one PermissionStore method after letting skip calls of it
// through, so a handler's second read can fail after its first succeeded.
type permFault struct {
	op   string
	skip int
}

func (f *permFault) hit(op string) error {
	if f.op != op {
		return nil
	}
	if f.skip > 0 {
		f.skip--
		return nil
	}
	return errBoom
}

type permFaultStore struct {
	*inmem.Store
	f *permFault
}

func (s permFaultStore) Permissions() port.PermissionStore {
	return permFaultPerms{s.Store.Permissions(), s.f}
}

type permFaultPerms struct {
	port.PermissionStore
	f *permFault
}

func (p permFaultPerms) Create(ctx context.Context, perm model.Permission) error {
	if err := p.f.hit("create"); err != nil {
		return err
	}
	return p.PermissionStore.Create(ctx, perm) //nolint:wrapcheck // test double
}

func (p permFaultPerms) Get(ctx context.Context, name string) (model.Permission, error) {
	if err := p.f.hit("get"); err != nil {
		return model.Permission{}, err
	}
	return p.PermissionStore.Get(ctx, name) //nolint:wrapcheck // test double
}

func (p permFaultPerms) Update(ctx context.Context, perm model.Permission) error {
	if err := p.f.hit("update"); err != nil {
		return err
	}
	return p.PermissionStore.Update(ctx, perm) //nolint:wrapcheck // test double
}

func (p permFaultPerms) Delete(ctx context.Context, name string) error {
	if err := p.f.hit("delete"); err != nil {
		return err
	}
	return p.PermissionStore.Delete(ctx, name) //nolint:wrapcheck // test double
}

func (p permFaultPerms) List(
	ctx context.Context,
	pg kernel.Page,
) ([]model.Permission, string, error) {
	if err := p.f.hit("list"); err != nil {
		return nil, "", err
	}
	return p.PermissionStore.List(ctx, pg) //nolint:wrapcheck // test double
}

func (p permFaultPerms) ForPrincipal(
	ctx context.Context,
	user string,
	groups []string,
) ([]model.Permission, error) {
	if err := p.f.hit("forprincipal"); err != nil {
		return nil, err
	}
	return p.PermissionStore.ForPrincipal(ctx, user, groups) //nolint:wrapcheck // test double
}

// TestAccReadAccessWithoutRawBody decodes the generated body when no raw body
// was kept, as for a request that bypassed the middleware.
func TestAccReadAccessWithoutRawBody(t *testing.T) {
	name := "direct"
	w, err := readAccess(
		context.Background(),
		&accessgen.CreatePermissionJSONRequestBody{Name: name},
	)
	if err != nil || w.Name != name {
		t.Fatalf("readAccess = %+v, %v", w, err)
	}
	if got := after(errBoom, errBoom); got != "boom" {
		t.Errorf("after with a bare sentinel = %q", got)
	}
}

// TestAccPermissionCallerWithoutMiddleware evaluates permissions itself for a
// principal the middleware did not authorise, and answers 500 when that
// evaluation fails.
func TestAccPermissionCallerWithoutMiddleware(t *testing.T) {
	db := inmem.New()
	cheapSeed(t, db)
	f := &permFault{op: "forprincipal"}
	st := permFaultStore{Store: db, f: f}
	deps := Deps{Store: st, API: accesstest.Over(t, st).API()}
	ctx := kernel.WithPrincipal(context.Background(), kernel.Principal{Name: "admin", Admin: true})
	_, err := permissionCaller(ctx, accessState{deps: deps})
	var ae *apierror.Error
	if !errors.As(err, &ae) || ae.Status != http.StatusInternalServerError {
		t.Fatalf("failing evaluation: %v", err)
	}
	*f = permFault{}
	c, err := permissionCaller(ctx, accessState{deps: deps})
	if err != nil || !c.dec.IsAdmin() {
		t.Fatalf("caller = %+v, %v", c, err)
	}
}
