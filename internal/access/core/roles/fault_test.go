package roles_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/access/core/roles"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

var errBoom = errors.New("boom")

// failing is a store whose project methods fail when named; the
// repositories it is paired with fail Repos.List and Repos.Update the same
// way.
type failing struct {
	roles.Store
	fail string
}

func (f failing) Projects() port.ProjectStore { return failingProjects{f.Store.Projects(), f.fail} }

func (f failing) Tx(ctx context.Context, fn func(context.Context) error) error {
	return f.Store.Tx(ctx, fn) //nolint:wrapcheck // test double
}

type failingProjects struct {
	port.ProjectStore
	fail string
}

func (p failingProjects) err(name string) error {
	if p.fail == name {
		return errBoom
	}
	return nil
}

func (p failingProjects) List(ctx context.Context) ([]model.Project, error) {
	if err := p.err("List"); err != nil {
		return nil, err
	}
	return p.ProjectStore.List(ctx) //nolint:wrapcheck // test double
}

func (p failingProjects) Environments(
	ctx context.Context,
	key string,
) ([]model.Environment, error) {
	if err := p.err("Environments"); err != nil {
		return nil, err
	}
	return p.ProjectStore.Environments(ctx, key) //nolint:wrapcheck // test double
}

func (p failingProjects) CreateEnvironment(ctx context.Context, e model.Environment) error {
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

func (p failingProjects) Roles(ctx context.Context, key string) ([]model.Role, error) {
	if err := p.err("Roles"); err != nil {
		return nil, err
	}
	return p.ProjectStore.Roles(ctx, key) //nolint:wrapcheck // test double
}

func (p failingProjects) Role(ctx context.Context, key, name string) (model.Role, error) {
	if err := p.err("Role"); err != nil {
		return model.Role{}, err
	}
	return p.ProjectStore.Role(ctx, key, name) //nolint:wrapcheck // test double
}

func (p failingProjects) PutRole(ctx context.Context, r model.Role) error {
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
) ([]model.RepositoryAssignment, error) {
	if err := p.err("Assignments"); err != nil {
		return nil, err
	}
	return p.ProjectStore.Assignments(ctx, key) //nolint:wrapcheck // test double
}

func (p failingProjects) Members(
	ctx context.Context, key string, typ model.PrincipalType,
) ([]model.ProjectMember, error) {
	if err := p.err("Members"); err != nil {
		return nil, err
	}
	return p.ProjectStore.Members(ctx, key, typ) //nolint:wrapcheck // test double
}

func (p failingProjects) PutMember(ctx context.Context, m model.ProjectMember) error {
	if err := p.err("PutMember"); err != nil {
		return err
	}
	return p.ProjectStore.PutMember(ctx, m) //nolint:wrapcheck // test double
}

func (p failingProjects) DeleteMember(
	ctx context.Context,
	key string,
	typ model.PrincipalType,
	name string,
) error {
	if err := p.err("DeleteMember"); err != nil {
		return err
	}
	return p.ProjectStore.DeleteMember(ctx, key, typ, name) //nolint:wrapcheck // test double
}

// TestUnitStoreFailures drives every operation into each store failure it can
// meet; none may be mistaken for a client error.
func TestUnitStoreFailures(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T) (*failing, roles.Service, *inmem.Store) {
		t.Helper()
		_, db := world(t)
		s := roles.Service{Store: db, Repositories: repositories{db: db}}
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
				model.Role{Name: "ci", Type: model.RoleCustomGlobal},
			),
		)
		must(t, db.Projects().PutMember(ctx, model.ProjectMember{
			ProjectKey: "p",
			Type:       model.PrincipalUser,
			Name:       "ann",
			Roles:      []string{"ci", "Viewer"},
		}))
		must(t, db.Projects().PutMember(ctx, model.ProjectMember{
			ProjectKey: "p", Type: model.PrincipalGroup, Name: "devs", Roles: []string{"ci"},
		}))
		f := &failing{Store: db}
		return f, roles.Service{Store: f, Repositories: repositories{db: db, fail: &f.fail}}, db
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
				model.Role{Name: "x", Type: model.RoleCustomGlobal, Environments: []string{"DEV"}},
			)
		}},
		{"Roles", func(s roles.Service) error {
			return s.CreateRole(ctx, model.Role{Name: "x", Type: model.RoleCustomGlobal})
		}},
		{"PutRole", func(s roles.Service) error {
			return s.CreateRole(ctx, model.Role{Name: "x", Type: model.RoleCustomGlobal})
		}},
		{"Role", func(s roles.Service) error {
			return s.UpdateRole(ctx, "ci", model.Role{Name: "ci", Type: model.RoleCustomGlobal})
		}},
		{"Role", func(s roles.Service) error { _, err := s.Role(ctx, "Viewer"); return err }},
		{"PutRole", func(s roles.Service) error {
			return s.UpdateRole(
				ctx,
				"Viewer",
				model.Role{Name: "Viewer", Type: model.RolePredefined},
			)
		}},
		{"Environments", func(s roles.Service) error {
			return s.UpdateRole(ctx, "Viewer", model.Role{
				Name: "Viewer", Type: model.RolePredefined, Environments: []string{"DEV"},
			})
		}},
		{"DeleteRole", func(s roles.Service) error { return s.DeleteRole(ctx, "ci") }},
		{"List", func(s roles.Service) error { return s.DeleteRole(ctx, "ci") }},
		{"Members", func(s roles.Service) error { return s.DeleteRole(ctx, "ci") }},
		{"PutMember", func(s roles.Service) error { return s.DeleteRole(ctx, "ci") }},
		{"DeleteMember", func(s roles.Service) error { return s.DeleteRole(ctx, "ci") }},
	}
	for _, c := range cases {
		f, s, _ := setup(t)
		f.fail = c.fail
		if err := c.op(s); !errors.Is(err, errBoom) {
			t.Errorf("failing %s: %v", c.fail, err)
		}
	}

	// Deleting QA: free it first, then fail the steps after the in-use check.
	for _, fail := range []string{"Assignments", "Roles", "DeleteEnvironment"} {
		f, s, db := setup(t)
		c, _ := db.Repos().Get(ctx, "r")
		c.Environments = nil
		must(t, db.Repos().Update(ctx, c))
		f.fail = fail
		if err := s.DeleteEnvironment(ctx, "QA"); !errors.Is(err, errBoom) {
			t.Errorf("delete failing %s: %v", fail, err)
		}
	}
	// The project listing inside the in-use check.
	f, s, db := setup(t)
	c, _ := db.Repos().Get(ctx, "r")
	c.Environments = nil
	must(t, db.Repos().Update(ctx, c))
	f.fail = "List"
	if err := s.DeleteEnvironment(ctx, "QA"); !errors.Is(err, errBoom) {
		t.Errorf("delete failing the project listing: %v", err)
	}
}

// secondUpdateFails passes the first repository update through and fails
// the second with errBoom.
type secondUpdateFails struct {
	repositories
	updates int
}

func (r *secondUpdateFails) Update(ctx context.Context, c repo.Config) error {
	r.updates++
	if r.updates == 2 {
		return errBoom
	}
	return r.repositories.Update(ctx, c)
}

// TestUnitRenameEnvironmentRollsBackRepositoryUpdates fails the second of
// two repository updates in a rename. The artifacts module made the first
// one in the rename's transaction, so the rollback takes it back with the
// environment, the role and the project assignment the rename changed
// (decision 0013).
func TestUnitRenameEnvironmentRollsBackRepositoryUpdates(t *testing.T) {
	ctx := context.Background()
	s, db := world(t)
	must(t, s.CreateEnvironment(ctx, "QA"))
	for _, key := range []string{"a", "b"} {
		must(t, db.Repos().Create(ctx, repo.Config{
			Key: key, Class: repo.Local, Environments: []string{"QA"},
		}))
	}
	must(t, s.CreateRole(ctx, model.Role{
		Name: "ci", Type: model.RoleCustomGlobal, Environments: []string{"QA"},
	}))
	must(t, db.Projects().PutAssignment(ctx, model.RepositoryAssignment{
		RepoKey: "a", ProjectKey: "p", Environments: []string{"QA"},
	}))
	updates := &secondUpdateFails{repositories: repositories{db: db}}
	s.Repositories = updates
	if err := s.RenameEnvironment(ctx, "QA", "STAGE"); !errors.Is(err, errBoom) {
		t.Fatalf("rename with the second repository update failing: %v", err)
	}
	if updates.updates != 2 {
		t.Fatalf("%d repository updates, want 2", updates.updates)
	}

	envs, err := s.Environments(ctx)
	must(t, err)
	if !slices.Contains(envs, "QA") || slices.Contains(envs, "STAGE") {
		t.Errorf("environments %v, want QA and no STAGE", envs)
	}
	role, err := s.Role(ctx, "ci")
	must(t, err)
	if !slices.Equal(role.Environments, []string{"QA"}) {
		t.Errorf("role environments %v, want [QA]", role.Environments)
	}
	a, err := db.Projects().Assignment(ctx, "a")
	must(t, err)
	if !slices.Equal(a.Environments, []string{"QA"}) {
		t.Errorf("assignment environments %v, want [QA]", a.Environments)
	}
	for _, key := range []string{"a", "b"} {
		c, err := db.Repos().Get(ctx, key)
		must(t, err)
		if !slices.Equal(c.Environments, []string{"QA"}) {
			t.Errorf("repository %s environments %v, want [QA]", key, c.Environments)
		}
	}
}
