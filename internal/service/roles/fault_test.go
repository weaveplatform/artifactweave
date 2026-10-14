package roles_test

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/service/roles"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var errBoom = errors.New("boom")

// failing is a store whose project and repository methods fail when named.
type failing struct {
	store.Store
	fail string
}

func (f failing) Projects() store.ProjectStore { return failingProjects{f.Store.Projects(), f.fail} }
func (f failing) Repos() store.RepoStore       { return failingRepos{f.Store.Repos(), f.fail} }

func (f failing) Tx(ctx context.Context, fn func(context.Context) error) error {
	return f.Store.Tx(ctx, fn) //nolint:wrapcheck // test double
}

type failingProjects struct {
	store.ProjectStore
	fail string
}

func (p failingProjects) err(name string) error {
	if p.fail == name {
		return errBoom
	}
	return nil
}

func (p failingProjects) List(ctx context.Context) ([]types.Project, error) {
	if err := p.err("List"); err != nil {
		return nil, err
	}
	return p.ProjectStore.List(ctx) //nolint:wrapcheck // test double
}

func (p failingProjects) Environments(
	ctx context.Context,
	key string,
) ([]types.Environment, error) {
	if err := p.err("Environments"); err != nil {
		return nil, err
	}
	return p.ProjectStore.Environments(ctx, key) //nolint:wrapcheck // test double
}

func (p failingProjects) CreateEnvironment(ctx context.Context, e types.Environment) error {
	if err := p.err("CreateEnvironment"); err != nil {
		return err
	}
	return p.ProjectStore.CreateEnvironment(ctx, e) //nolint:wrapcheck // test double
}

func (p failingProjects) RenameEnvironment(ctx context.Context, key, from, to string) error {
	if err := p.err("RenameEnvironment"); err != nil {
		return err
	}
	return p.ProjectStore.RenameEnvironment(ctx, key, from, to) //nolint:wrapcheck // test double
}

func (p failingProjects) DeleteEnvironment(ctx context.Context, key, name string) error {
	if err := p.err("DeleteEnvironment"); err != nil {
		return err
	}
	return p.ProjectStore.DeleteEnvironment(ctx, key, name) //nolint:wrapcheck // test double
}

func (p failingProjects) Roles(ctx context.Context, key string) ([]types.Role, error) {
	if err := p.err("Roles"); err != nil {
		return nil, err
	}
	return p.ProjectStore.Roles(ctx, key) //nolint:wrapcheck // test double
}

func (p failingProjects) Role(ctx context.Context, key, name string) (types.Role, error) {
	if err := p.err("Role"); err != nil {
		return types.Role{}, err
	}
	return p.ProjectStore.Role(ctx, key, name) //nolint:wrapcheck // test double
}

func (p failingProjects) PutRole(ctx context.Context, r types.Role) error {
	if err := p.err("PutRole"); err != nil {
		return err
	}
	return p.ProjectStore.PutRole(ctx, r) //nolint:wrapcheck // test double
}

func (p failingProjects) DeleteRole(ctx context.Context, key, name string) error {
	if err := p.err("DeleteRole"); err != nil {
		return err
	}
	return p.ProjectStore.DeleteRole(ctx, key, name) //nolint:wrapcheck // test double
}

func (p failingProjects) Assignments(
	ctx context.Context,
	key string,
) ([]types.RepositoryAssignment, error) {
	if err := p.err("Assignments"); err != nil {
		return nil, err
	}
	return p.ProjectStore.Assignments(ctx, key) //nolint:wrapcheck // test double
}

func (p failingProjects) Members(
	ctx context.Context, key string, typ types.PrincipalType,
) ([]types.ProjectMember, error) {
	if err := p.err("Members"); err != nil {
		return nil, err
	}
	return p.ProjectStore.Members(ctx, key, typ) //nolint:wrapcheck // test double
}

func (p failingProjects) PutMember(ctx context.Context, m types.ProjectMember) error {
	if err := p.err("PutMember"); err != nil {
		return err
	}
	return p.ProjectStore.PutMember(ctx, m) //nolint:wrapcheck // test double
}

func (p failingProjects) DeleteMember(
	ctx context.Context,
	key string,
	typ types.PrincipalType,
	name string,
) error {
	if err := p.err("DeleteMember"); err != nil {
		return err
	}
	return p.ProjectStore.DeleteMember(ctx, key, typ, name) //nolint:wrapcheck // test double
}

type failingRepos struct {
	store.RepoStore
	fail string
}

func (r failingRepos) List(ctx context.Context, f store.RepoFilter) ([]repo.Config, error) {
	if r.fail == "Repos.List" {
		return nil, errBoom
	}
	return r.RepoStore.List(ctx, f) //nolint:wrapcheck // test double
}

func (r failingRepos) Update(ctx context.Context, c repo.Config) error {
	if r.fail == "Repos.Update" {
		return errBoom
	}
	return r.RepoStore.Update(ctx, c) //nolint:wrapcheck // test double
}

// TestStoreFailures drives every operation into each store failure it can
// meet; none may be mistaken for a client error.
func TestStoreFailures(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T) (*failing, roles.Service) {
		t.Helper()
		_, db := world(t)
		s := roles.Service{Store: db}
		must(t, s.CreateEnvironment(ctx, "QA"))
		must(
			t,
			db.Repos().
				Create(ctx, repo.Config{Key: "r", Class: repo.Local, Environments: []string{"QA"}}),
		)
		must(
			t,
			s.CreateRole(
				ctx,
				types.Role{Name: "ci", Type: types.RoleCustomGlobal},
			),
		)
		must(t, db.Projects().PutMember(ctx, types.ProjectMember{
			ProjectKey: "p",
			Type:       types.PrincipalUser,
			Name:       "ann",
			Roles:      []string{"ci", "Viewer"},
		}))
		must(t, db.Projects().PutMember(ctx, types.ProjectMember{
			ProjectKey: "p", Type: types.PrincipalGroup, Name: "devs", Roles: []string{"ci"},
		}))
		f := &failing{Store: db}
		return f, roles.Service{Store: f}
	}
	// Each case: the method to fail and the operation that must meet it.
	cases := []struct {
		fail string
		op   func(s roles.Service) error
	}{
		{"Environments", func(s roles.Service) error { _, err := s.Environments(ctx); return err }},
		{"List", func(s roles.Service) error { return s.CreateEnvironment(ctx, "NEW") }},
		{
			"CreateEnvironment",
			func(s roles.Service) error { return s.CreateEnvironment(ctx, "NEW") },
		},
		{
			"Environments",
			func(s roles.Service) error { return s.RenameEnvironment(ctx, "QA", "NEW") },
		},
		{"List", func(s roles.Service) error { return s.RenameEnvironment(ctx, "QA", "NEW") }},
		{
			"RenameEnvironment",
			func(s roles.Service) error { return s.RenameEnvironment(ctx, "QA", "NEW") },
		},
		{
			"Repos.List",
			func(s roles.Service) error { return s.RenameEnvironment(ctx, "QA", "NEW") },
		},
		{
			"Repos.Update",
			func(s roles.Service) error { return s.RenameEnvironment(ctx, "QA", "NEW") },
		},
		{"Environments", func(s roles.Service) error { return s.DeleteEnvironment(ctx, "QA") }},
		{"Repos.List", func(s roles.Service) error { return s.DeleteEnvironment(ctx, "QA") }},
		{"Roles", func(s roles.Service) error { _, err := s.Roles(ctx); return err }},
		{"Role", func(s roles.Service) error { _, err := s.Role(ctx, "ci"); return err }},
		{"Environments", func(s roles.Service) error {
			return s.CreateRole(
				ctx,
				types.Role{Name: "x", Type: types.RoleCustomGlobal, Environments: []string{"DEV"}},
			)
		}},
		{"Roles", func(s roles.Service) error {
			return s.CreateRole(ctx, types.Role{Name: "x", Type: types.RoleCustomGlobal})
		}},
		{"PutRole", func(s roles.Service) error {
			return s.CreateRole(ctx, types.Role{Name: "x", Type: types.RoleCustomGlobal})
		}},
		{"Role", func(s roles.Service) error {
			return s.UpdateRole(ctx, "ci", types.Role{Name: "ci", Type: types.RoleCustomGlobal})
		}},
		{"Role", func(s roles.Service) error { _, err := s.Role(ctx, "Viewer"); return err }},
		{"PutRole", func(s roles.Service) error {
			return s.UpdateRole(
				ctx,
				"Viewer",
				types.Role{Name: "Viewer", Type: types.RolePredefined},
			)
		}},
		{"Environments", func(s roles.Service) error {
			return s.UpdateRole(ctx, "Viewer", types.Role{
				Name: "Viewer", Type: types.RolePredefined, Environments: []string{"DEV"},
			})
		}},
		{"DeleteRole", func(s roles.Service) error { return s.DeleteRole(ctx, "ci") }},
		{"List", func(s roles.Service) error { return s.DeleteRole(ctx, "ci") }},
		{"Members", func(s roles.Service) error { return s.DeleteRole(ctx, "ci") }},
		{"PutMember", func(s roles.Service) error { return s.DeleteRole(ctx, "ci") }},
		{"DeleteMember", func(s roles.Service) error { return s.DeleteRole(ctx, "ci") }},
	}
	for _, c := range cases {
		f, s := setup(t)
		f.fail = c.fail
		if err := c.op(s); !errors.Is(err, errBoom) {
			t.Errorf("failing %s: %v", c.fail, err)
		}
	}

	// Deleting QA: free it first, then fail the steps after the in-use check.
	for _, fail := range []string{"Assignments", "Roles", "DeleteEnvironment"} {
		f, s := setup(t)
		c, _ := f.Store.Repos().Get(ctx, "r")
		c.Environments = nil
		must(t, f.Store.Repos().Update(ctx, c))
		f.fail = fail
		if err := s.DeleteEnvironment(ctx, "QA"); !errors.Is(err, errBoom) {
			t.Errorf("delete failing %s: %v", fail, err)
		}
	}
	// The project listing inside the in-use check.
	f, s := setup(t)
	c, _ := f.Store.Repos().Get(ctx, "r")
	c.Environments = nil
	must(t, f.Store.Repos().Update(ctx, c))
	f.fail = "List"
	if err := s.DeleteEnvironment(ctx, "QA"); !errors.Is(err, errBoom) {
		t.Errorf("delete failing the project listing: %v", err)
	}
}
