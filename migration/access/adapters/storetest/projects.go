package storetest

import (
	"context"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/core/model"
	"github.com/weaveplatform/artifactweave/migration/access/core/port"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/testkit/storekit"
)

// runProjects covers projects (decision 0013).
func runProjects(t *testing.T, newStore Factory) {
	t.Run("Projects", func(t *testing.T) { testProjects(t, newStore(t)) })
	t.Run("Roles", func(t *testing.T) { testRoles(t, newStore(t)) })
	t.Run("Environments", func(t *testing.T) { testEnvironments(t, newStore(t)) })
	t.Run("ProjectMembers", func(t *testing.T) { testProjectMembers(t, newStore(t)) })
	t.Run("Assignments", func(t *testing.T) { testAssignments(t, newStore(t)) })
}

func project(key string) model.Project {
	return model.Project{
		Key: key, DisplayName: "Project " + key, Description: "the " + key + " project",
		AdminPrivileges:   model.AdminPrivileges{ManageMembers: true, CreateLocalRepository: true},
		StorageQuotaBytes: 1 << 30, SoftLimit: true,
	}
}

func createProjects(t *testing.T, s port.Store, keys ...string) {
	t.Helper()
	for _, k := range keys {
		storekit.Must(t, s.Projects().Create(context.Background(), project(k)))
	}
}

func testProjects(t *testing.T, s port.Store) {
	ctx := context.Background()
	ps := s.Projects()
	createProjects(t, s, "charlie", "alpha", "bravo")
	storekit.WantErr(t, ps.Create(ctx, project("alpha")), kernel.ErrConflict)

	storekit.Same(t, "Get", project("alpha"), storekit.Get[model.Project](t)(ps.Get(ctx, "alpha")))
	_, err := ps.Get(ctx, "nope")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.Same(
		t,
		"List",
		[]model.Project{project("alpha"), project("bravo"), project("charlie")},
		storekit.Get[[]model.Project](t)(ps.List(ctx)),
	)

	changed := project("alpha")
	changed.Description, changed.AdminPrivileges, changed.QuotaEmail = "renamed", model.AdminPrivileges{}, true
	storekit.Must(t, ps.Update(ctx, changed))
	storekit.Same(
		t,
		"Get after Update",
		changed,
		storekit.Get[model.Project](t)(ps.Get(ctx, "alpha")),
	)
	storekit.WantErr(t, ps.Update(ctx, project("nope")), kernel.ErrNotFound)

	// Deleting a project takes its roles, environments, members, owned
	// assignments and its shares of other projects' repositories with it.
	storekit.Must(
		t,
		ps.PutRole(ctx, model.Role{ProjectKey: "alpha", Name: "dev", Type: model.RoleCustom}),
	)
	storekit.Must(
		t,
		ps.CreateEnvironment(ctx, model.Environment{ProjectKey: "alpha", Name: "alpha-QA"}),
	)
	storekit.Must(
		t,
		ps.PutMember(
			ctx,
			model.ProjectMember{ProjectKey: "alpha", Type: model.PrincipalUser, Name: "ann"},
		),
	)
	storekit.Must(
		t,
		ps.PutAssignment(
			ctx,
			model.RepositoryAssignment{RepoKey: "alpha-libs", ProjectKey: "alpha"},
		),
	)
	storekit.Must(t, ps.PutAssignment(ctx, model.RepositoryAssignment{
		RepoKey: "bravo-libs", ProjectKey: "bravo", SharedWith: []string{"alpha", "charlie"},
	}))
	storekit.Must(t, ps.Delete(ctx, "alpha"))
	storekit.WantErr(t, ps.Delete(ctx, "alpha"), kernel.ErrNotFound)
	_, err = ps.Get(ctx, "alpha")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	if roles := storekit.Get[[]model.Role](t)(ps.Roles(ctx, "alpha")); len(roles) != 0 {
		t.Fatalf("roles left: %v", roles)
	}
	if envs := storekit.Get[[]model.Environment](t)(ps.Environments(ctx, "alpha")); len(envs) != 0 {
		t.Fatalf("environments left: %v", envs)
	}
	_, err = ps.Member(ctx, "alpha", model.PrincipalUser, "ann")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	_, err = ps.Assignment(ctx, "alpha-libs")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.Same(t, "shares after delete", []string{"charlie"},
		storekit.Get[model.RepositoryAssignment](t)(ps.Assignment(ctx, "bravo-libs")).SharedWith)
}

func testRoles(t *testing.T, s port.Store) {
	ctx := context.Background()
	ps := s.Projects()
	createProjects(t, s, "p1")
	reader := model.Role{
		Name: "Reader", Type: model.RoleCustomGlobal, Description: "reads",
		Environments: []string{"DEV", "PROD"}, Actions: []string{"READ_REPOSITORY"},
	}
	admin := model.Role{Name: "Admin", Type: model.RoleAdmin, Actions: []string{"ADMIN"}}
	dev := model.Role{
		ProjectKey:   "p1",
		Name:         "Developer",
		Type:         model.RoleCustom,
		Environments: []string{"DEV"},
	}
	for _, r := range []model.Role{reader, admin, dev} {
		storekit.Must(t, ps.PutRole(ctx, r))
	}
	storekit.WantErr(
		t,
		ps.PutRole(ctx, model.Role{ProjectKey: "nope", Name: "x"}),
		kernel.ErrNotFound,
	)

	storekit.Same(t, "Role", reader, storekit.Get[model.Role](t)(ps.Role(ctx, "", "Reader")))
	storekit.Same(
		t,
		"global Roles",
		[]model.Role{admin, reader},
		storekit.Get[[]model.Role](t)(ps.Roles(ctx, "")),
	)
	storekit.Same(
		t,
		"project Roles",
		[]model.Role{dev},
		storekit.Get[[]model.Role](t)(ps.Roles(ctx, "p1")),
	)
	_, err := ps.Role(ctx, "p1", "Reader")
	storekit.WantErr(t, err, kernel.ErrNotFound)

	got := storekit.Get[model.Role](t)(ps.Role(ctx, "", "Reader"))
	got.Environments[0] = "mutated"
	storekit.Same(
		t,
		"Role is a copy",
		reader,
		storekit.Get[model.Role](t)(ps.Role(ctx, "", "Reader")),
	)

	reader.Description, reader.Environments = "reads everything", nil
	storekit.Must(t, ps.PutRole(ctx, reader))
	storekit.Same(
		t,
		"replaced Role",
		reader,
		storekit.Get[model.Role](t)(ps.Role(ctx, "", "Reader")),
	)

	storekit.Must(t, ps.DeleteRole(ctx, "", "Reader"))
	storekit.WantErr(t, ps.DeleteRole(ctx, "", "Reader"), kernel.ErrNotFound)
	_, err = ps.Role(ctx, "", "Reader")
	storekit.WantErr(t, err, kernel.ErrNotFound)
}

func testEnvironments(t *testing.T, s port.Store) {
	ctx := context.Background()
	ps := s.Projects()
	createProjects(t, s, "p1")
	for _, e := range []model.Environment{
		{Name: "PROD"}, {Name: "DEV"}, {ProjectKey: "p1", Name: "p1-STAGE"},
	} {
		storekit.Must(t, ps.CreateEnvironment(ctx, e))
	}
	// Names are unique across scopes.
	storekit.WantErr(
		t,
		ps.CreateEnvironment(ctx, model.Environment{ProjectKey: "p1", Name: "DEV"}),
		kernel.ErrConflict,
	)
	storekit.WantErr(
		t,
		ps.CreateEnvironment(ctx, model.Environment{ProjectKey: "nope", Name: "X"}),
		kernel.ErrNotFound,
	)
	storekit.Same(t, "global", []model.Environment{{Name: "DEV"}, {Name: "PROD"}},
		storekit.Get[[]model.Environment](t)(ps.Environments(ctx, "")))
	storekit.Same(t, "project", []model.Environment{{ProjectKey: "p1", Name: "p1-STAGE"}},
		storekit.Get[[]model.Environment](t)(ps.Environments(ctx, "p1")))

	role := model.Role{
		ProjectKey:   "p1",
		Name:         "qa",
		Type:         model.RoleCustom,
		Environments: []string{"DEV", "p1-STAGE"},
	}
	untouched := model.Role{
		Name:         "ops",
		Type:         model.RoleCustomGlobal,
		Environments: []string{"PROD"},
	}
	storekit.Must(t, ps.PutRole(ctx, role))
	storekit.Must(t, ps.PutRole(ctx, untouched))
	assigned := model.RepositoryAssignment{
		RepoKey:      "p1-libs",
		ProjectKey:   "p1",
		Environments: []string{"p1-STAGE"},
	}
	storekit.Must(t, ps.PutAssignment(ctx, assigned))

	// Renaming to the same name changes nothing.
	storekit.Must(t, ps.RenameEnvironment(ctx, "p1", "p1-STAGE", "p1-STAGE"))
	storekit.Same(
		t,
		"role after no-op rename",
		role,
		storekit.Get[model.Role](t)(ps.Role(ctx, "p1", "qa")),
	)
	storekit.Must(t, ps.RenameEnvironment(ctx, "p1", "p1-STAGE", "p1-QA"))
	storekit.Same(t, "renamed", []model.Environment{{ProjectKey: "p1", Name: "p1-QA"}},
		storekit.Get[[]model.Environment](t)(ps.Environments(ctx, "p1")))
	role.Environments = []string{"DEV", "p1-QA"}
	storekit.Same(
		t,
		"role after rename",
		role,
		storekit.Get[model.Role](t)(ps.Role(ctx, "p1", "qa")),
	)
	storekit.Same(
		t,
		"untouched role",
		untouched,
		storekit.Get[model.Role](t)(ps.Role(ctx, "", "ops")),
	)
	assigned.Environments = []string{"p1-QA"}
	storekit.Same(t, "assignment after rename", assigned,
		storekit.Get[model.RepositoryAssignment](t)(ps.Assignment(ctx, "p1-libs")))

	storekit.WantErr(t, ps.RenameEnvironment(ctx, "p1", "p1-QA", "DEV"), kernel.ErrConflict)
	storekit.WantErr(t, ps.RenameEnvironment(ctx, "p1", "nope", "X"), kernel.ErrNotFound)
	storekit.WantErr(t, ps.RenameEnvironment(ctx, "", "p1-QA", "X"), kernel.ErrNotFound)

	storekit.WantErr(t, ps.DeleteEnvironment(ctx, "", "p1-QA"), kernel.ErrNotFound)
	storekit.Must(t, ps.DeleteEnvironment(ctx, "p1", "p1-QA"))
	storekit.WantErr(t, ps.DeleteEnvironment(ctx, "p1", "p1-QA"), kernel.ErrNotFound)
	if envs := storekit.Get[[]model.Environment](t)(ps.Environments(ctx, "p1")); len(envs) != 0 {
		t.Fatalf("environments left: %v", envs)
	}
}

func testProjectMembers(t *testing.T, s port.Store) {
	ctx := context.Background()
	ps := s.Projects()
	createProjects(t, s, "p1", "p2")
	bob := model.ProjectMember{
		ProjectKey: "p1",
		Type:       model.PrincipalUser,
		Name:       "bob",
		Roles:      []string{"Developer"},
	}
	ann := model.ProjectMember{
		ProjectKey: "p1",
		Type:       model.PrincipalUser,
		Name:       "ann",
		Roles:      []string{"Viewer", "QA"},
	}
	devs := model.ProjectMember{
		ProjectKey: "p1",
		Type:       model.PrincipalGroup,
		Name:       "devs",
		Roles:      []string{"Developer"},
	}
	other := model.ProjectMember{ProjectKey: "p2", Type: model.PrincipalUser, Name: "bob"}
	for _, m := range []model.ProjectMember{bob, ann, devs, other} {
		storekit.Must(t, ps.PutMember(ctx, m))
	}
	storekit.WantErr(
		t,
		ps.PutMember(
			ctx,
			model.ProjectMember{ProjectKey: "nope", Type: model.PrincipalUser, Name: "x"},
		),
		kernel.ErrNotFound,
	)

	storekit.Same(
		t,
		"Member",
		ann,
		storekit.Get[model.ProjectMember](t)(ps.Member(ctx, "p1", model.PrincipalUser, "ann")),
	)
	storekit.Same(t, "users", []model.ProjectMember{ann, bob},
		storekit.Get[[]model.ProjectMember](t)(ps.Members(ctx, "p1", model.PrincipalUser)))
	storekit.Same(t, "groups", []model.ProjectMember{devs},
		storekit.Get[[]model.ProjectMember](t)(ps.Members(ctx, "p1", model.PrincipalGroup)))
	_, err := ps.Member(ctx, "p1", model.PrincipalGroup, "bob")
	storekit.WantErr(t, err, kernel.ErrNotFound)

	got := storekit.Get[model.ProjectMember](t)(ps.Member(ctx, "p1", model.PrincipalUser, "ann"))
	got.Roles[0] = "mutated"
	storekit.Same(
		t,
		"Member is a copy",
		ann,
		storekit.Get[model.ProjectMember](t)(ps.Member(ctx, "p1", model.PrincipalUser, "ann")),
	)

	ann.Roles = []string{"Admin"}
	storekit.Must(t, ps.PutMember(ctx, ann))
	storekit.Same(
		t,
		"replaced",
		ann,
		storekit.Get[model.ProjectMember](t)(ps.Member(ctx, "p1", model.PrincipalUser, "ann")),
	)

	storekit.Must(t, ps.DeleteMember(ctx, "p1", model.PrincipalUser, "ann"))
	storekit.WantErr(t, ps.DeleteMember(ctx, "p1", model.PrincipalUser, "ann"), kernel.ErrNotFound)
	storekit.Same(t, "after delete", []model.ProjectMember{bob},
		storekit.Get[[]model.ProjectMember](t)(ps.Members(ctx, "p1", model.PrincipalUser)))
}

func testAssignments(t *testing.T, s port.Store) {
	ctx := context.Background()
	ps := s.Projects()
	createProjects(t, s, "p1", "p2", "p3")
	a := model.RepositoryAssignment{
		RepoKey: "repo-a", ProjectKey: "p1", Environments: []string{"DEV"},
		SharedWith: []string{"p3", "p2", "p3"}, Updated: storekit.At(1),
	}
	b := model.RepositoryAssignment{RepoKey: "repo-b", ProjectKey: "p2", Updated: storekit.At(2)}
	c := model.RepositoryAssignment{RepoKey: "repo-c", SharedWithAll: true, SharedReadOnly: true}
	d := model.RepositoryAssignment{
		RepoKey:        "repo-d",
		ProjectKey:     "p3",
		SharedWith:     []string{"p1"},
		SharedReadOnly: true,
	}
	for _, v := range []model.RepositoryAssignment{d, c, b, a} {
		storekit.Must(t, ps.PutAssignment(ctx, v))
	}
	storekit.WantErr(
		t,
		ps.PutAssignment(ctx, model.RepositoryAssignment{RepoKey: "x", ProjectKey: "nope"}),
		kernel.ErrNotFound,
	)

	// Shares come back sorted and without duplicates.
	a.SharedWith = []string{"p2", "p3"}
	storekit.Same(
		t,
		"Assignment",
		a,
		storekit.Get[model.RepositoryAssignment](t)(ps.Assignment(ctx, "repo-a")),
	)
	_, err := ps.Assignment(ctx, "nope")
	storekit.WantErr(t, err, kernel.ErrNotFound)

	storekit.Same(t, "Assignments p1", []model.RepositoryAssignment{a, c, d},
		storekit.Get[[]model.RepositoryAssignment](t)(ps.Assignments(ctx, "p1")))
	storekit.Same(t, "Assignments p2", []model.RepositoryAssignment{a, b, c},
		storekit.Get[[]model.RepositoryAssignment](t)(ps.Assignments(ctx, "p2")))

	a.SharedWith, a.ProjectKey = nil, "p2"
	storekit.Must(t, ps.PutAssignment(ctx, a))
	storekit.Same(
		t,
		"replaced",
		a,
		storekit.Get[model.RepositoryAssignment](t)(ps.Assignment(ctx, "repo-a")),
	)
	storekit.Same(t, "Assignments p3 after replace", []model.RepositoryAssignment{c, d},
		storekit.Get[[]model.RepositoryAssignment](t)(ps.Assignments(ctx, "p3")))

	storekit.Must(t, ps.DeleteAssignment(ctx, "repo-d"))
	storekit.WantErr(t, ps.DeleteAssignment(ctx, "repo-d"), kernel.ErrNotFound)
	_, err = ps.Assignment(ctx, "repo-d")
	storekit.WantErr(t, err, kernel.ErrNotFound)
}
