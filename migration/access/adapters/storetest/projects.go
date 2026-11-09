package storetest

import (
	"context"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// runProjects covers projects (decision 0013).
func runProjects(t *testing.T, newStore Factory) {
	t.Run("Projects", func(t *testing.T) { testProjects(t, newStore(t)) })
	t.Run("Roles", func(t *testing.T) { testRoles(t, newStore(t)) })
	t.Run("Environments", func(t *testing.T) { testEnvironments(t, newStore(t)) })
	t.Run("ProjectMembers", func(t *testing.T) { testProjectMembers(t, newStore(t)) })
	t.Run("Assignments", func(t *testing.T) { testAssignments(t, newStore(t)) })
}

func project(key string) types.Project {
	return types.Project{
		Key: key, DisplayName: "Project " + key, Description: "the " + key + " project",
		AdminPrivileges:   types.AdminPrivileges{ManageMembers: true, CreateLocalRepository: true},
		StorageQuotaBytes: 1 << 30, SoftLimit: true,
	}
}

func createProjects(t *testing.T, s store.Store, keys ...string) {
	t.Helper()
	for _, k := range keys {
		storekit.Must(t, s.Projects().Create(context.Background(), project(k)))
	}
}

func testProjects(t *testing.T, s store.Store) {
	ctx := context.Background()
	ps := s.Projects()
	createProjects(t, s, "charlie", "alpha", "bravo")
	storekit.WantErr(t, ps.Create(ctx, project("alpha")), store.ErrConflict)

	storekit.Same(t, "Get", project("alpha"), storekit.Get[types.Project](t)(ps.Get(ctx, "alpha")))
	_, err := ps.Get(ctx, "nope")
	storekit.WantErr(t, err, store.ErrNotFound)
	storekit.Same(
		t,
		"List",
		[]types.Project{project("alpha"), project("bravo"), project("charlie")},
		storekit.Get[[]types.Project](t)(ps.List(ctx)),
	)

	changed := project("alpha")
	changed.Description, changed.AdminPrivileges, changed.QuotaEmail = "renamed", types.AdminPrivileges{}, true
	storekit.Must(t, ps.Update(ctx, changed))
	storekit.Same(
		t,
		"Get after Update",
		changed,
		storekit.Get[types.Project](t)(ps.Get(ctx, "alpha")),
	)
	storekit.WantErr(t, ps.Update(ctx, project("nope")), store.ErrNotFound)

	// Deleting a project takes its roles, environments, members, owned
	// assignments and its shares of other projects' repositories with it.
	storekit.Must(
		t,
		ps.PutRole(ctx, types.Role{ProjectKey: "alpha", Name: "dev", Type: types.RoleCustom}),
	)
	storekit.Must(
		t,
		ps.CreateEnvironment(ctx, types.Environment{ProjectKey: "alpha", Name: "alpha-QA"}),
	)
	storekit.Must(
		t,
		ps.PutMember(
			ctx,
			types.ProjectMember{ProjectKey: "alpha", Type: types.PrincipalUser, Name: "ann"},
		),
	)
	storekit.Must(
		t,
		ps.PutAssignment(
			ctx,
			types.RepositoryAssignment{RepoKey: "alpha-libs", ProjectKey: "alpha"},
		),
	)
	storekit.Must(t, ps.PutAssignment(ctx, types.RepositoryAssignment{
		RepoKey: "bravo-libs", ProjectKey: "bravo", SharedWith: []string{"alpha", "charlie"},
	}))
	storekit.Must(t, ps.Delete(ctx, "alpha"))
	storekit.WantErr(t, ps.Delete(ctx, "alpha"), store.ErrNotFound)
	_, err = ps.Get(ctx, "alpha")
	storekit.WantErr(t, err, store.ErrNotFound)
	if roles := storekit.Get[[]types.Role](t)(ps.Roles(ctx, "alpha")); len(roles) != 0 {
		t.Fatalf("roles left: %v", roles)
	}
	if envs := storekit.Get[[]types.Environment](t)(ps.Environments(ctx, "alpha")); len(envs) != 0 {
		t.Fatalf("environments left: %v", envs)
	}
	_, err = ps.Member(ctx, "alpha", types.PrincipalUser, "ann")
	storekit.WantErr(t, err, store.ErrNotFound)
	_, err = ps.Assignment(ctx, "alpha-libs")
	storekit.WantErr(t, err, store.ErrNotFound)
	storekit.Same(t, "shares after delete", []string{"charlie"},
		storekit.Get[types.RepositoryAssignment](t)(ps.Assignment(ctx, "bravo-libs")).SharedWith)
}

func testRoles(t *testing.T, s store.Store) {
	ctx := context.Background()
	ps := s.Projects()
	createProjects(t, s, "p1")
	reader := types.Role{
		Name: "Reader", Type: types.RoleCustomGlobal, Description: "reads",
		Environments: []string{"DEV", "PROD"}, Actions: []string{"READ_REPOSITORY"},
	}
	admin := types.Role{Name: "Admin", Type: types.RoleAdmin, Actions: []string{"ADMIN"}}
	dev := types.Role{
		ProjectKey:   "p1",
		Name:         "Developer",
		Type:         types.RoleCustom,
		Environments: []string{"DEV"},
	}
	for _, r := range []types.Role{reader, admin, dev} {
		storekit.Must(t, ps.PutRole(ctx, r))
	}
	storekit.WantErr(
		t,
		ps.PutRole(ctx, types.Role{ProjectKey: "nope", Name: "x"}),
		store.ErrNotFound,
	)

	storekit.Same(t, "Role", reader, storekit.Get[types.Role](t)(ps.Role(ctx, "", "Reader")))
	storekit.Same(
		t,
		"global Roles",
		[]types.Role{admin, reader},
		storekit.Get[[]types.Role](t)(ps.Roles(ctx, "")),
	)
	storekit.Same(
		t,
		"project Roles",
		[]types.Role{dev},
		storekit.Get[[]types.Role](t)(ps.Roles(ctx, "p1")),
	)
	_, err := ps.Role(ctx, "p1", "Reader")
	storekit.WantErr(t, err, store.ErrNotFound)

	got := storekit.Get[types.Role](t)(ps.Role(ctx, "", "Reader"))
	got.Environments[0] = "mutated"
	storekit.Same(
		t,
		"Role is a copy",
		reader,
		storekit.Get[types.Role](t)(ps.Role(ctx, "", "Reader")),
	)

	reader.Description, reader.Environments = "reads everything", nil
	storekit.Must(t, ps.PutRole(ctx, reader))
	storekit.Same(
		t,
		"replaced Role",
		reader,
		storekit.Get[types.Role](t)(ps.Role(ctx, "", "Reader")),
	)

	storekit.Must(t, ps.DeleteRole(ctx, "", "Reader"))
	storekit.WantErr(t, ps.DeleteRole(ctx, "", "Reader"), store.ErrNotFound)
	_, err = ps.Role(ctx, "", "Reader")
	storekit.WantErr(t, err, store.ErrNotFound)
}

func testEnvironments(t *testing.T, s store.Store) {
	ctx := context.Background()
	ps := s.Projects()
	createProjects(t, s, "p1")
	for _, e := range []types.Environment{
		{Name: "PROD"}, {Name: "DEV"}, {ProjectKey: "p1", Name: "p1-STAGE"},
	} {
		storekit.Must(t, ps.CreateEnvironment(ctx, e))
	}
	// Names are unique across scopes.
	storekit.WantErr(
		t,
		ps.CreateEnvironment(ctx, types.Environment{ProjectKey: "p1", Name: "DEV"}),
		store.ErrConflict,
	)
	storekit.WantErr(
		t,
		ps.CreateEnvironment(ctx, types.Environment{ProjectKey: "nope", Name: "X"}),
		store.ErrNotFound,
	)
	storekit.Same(t, "global", []types.Environment{{Name: "DEV"}, {Name: "PROD"}},
		storekit.Get[[]types.Environment](t)(ps.Environments(ctx, "")))
	storekit.Same(t, "project", []types.Environment{{ProjectKey: "p1", Name: "p1-STAGE"}},
		storekit.Get[[]types.Environment](t)(ps.Environments(ctx, "p1")))

	role := types.Role{
		ProjectKey:   "p1",
		Name:         "qa",
		Type:         types.RoleCustom,
		Environments: []string{"DEV", "p1-STAGE"},
	}
	untouched := types.Role{
		Name:         "ops",
		Type:         types.RoleCustomGlobal,
		Environments: []string{"PROD"},
	}
	storekit.Must(t, ps.PutRole(ctx, role))
	storekit.Must(t, ps.PutRole(ctx, untouched))
	assigned := types.RepositoryAssignment{
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
		storekit.Get[types.Role](t)(ps.Role(ctx, "p1", "qa")),
	)
	storekit.Must(t, ps.RenameEnvironment(ctx, "p1", "p1-STAGE", "p1-QA"))
	storekit.Same(t, "renamed", []types.Environment{{ProjectKey: "p1", Name: "p1-QA"}},
		storekit.Get[[]types.Environment](t)(ps.Environments(ctx, "p1")))
	role.Environments = []string{"DEV", "p1-QA"}
	storekit.Same(
		t,
		"role after rename",
		role,
		storekit.Get[types.Role](t)(ps.Role(ctx, "p1", "qa")),
	)
	storekit.Same(
		t,
		"untouched role",
		untouched,
		storekit.Get[types.Role](t)(ps.Role(ctx, "", "ops")),
	)
	assigned.Environments = []string{"p1-QA"}
	storekit.Same(t, "assignment after rename", assigned,
		storekit.Get[types.RepositoryAssignment](t)(ps.Assignment(ctx, "p1-libs")))

	storekit.WantErr(t, ps.RenameEnvironment(ctx, "p1", "p1-QA", "DEV"), store.ErrConflict)
	storekit.WantErr(t, ps.RenameEnvironment(ctx, "p1", "nope", "X"), store.ErrNotFound)
	storekit.WantErr(t, ps.RenameEnvironment(ctx, "", "p1-QA", "X"), store.ErrNotFound)

	storekit.WantErr(t, ps.DeleteEnvironment(ctx, "", "p1-QA"), store.ErrNotFound)
	storekit.Must(t, ps.DeleteEnvironment(ctx, "p1", "p1-QA"))
	storekit.WantErr(t, ps.DeleteEnvironment(ctx, "p1", "p1-QA"), store.ErrNotFound)
	if envs := storekit.Get[[]types.Environment](t)(ps.Environments(ctx, "p1")); len(envs) != 0 {
		t.Fatalf("environments left: %v", envs)
	}
}

func testProjectMembers(t *testing.T, s store.Store) {
	ctx := context.Background()
	ps := s.Projects()
	createProjects(t, s, "p1", "p2")
	bob := types.ProjectMember{
		ProjectKey: "p1",
		Type:       types.PrincipalUser,
		Name:       "bob",
		Roles:      []string{"Developer"},
	}
	ann := types.ProjectMember{
		ProjectKey: "p1",
		Type:       types.PrincipalUser,
		Name:       "ann",
		Roles:      []string{"Viewer", "QA"},
	}
	devs := types.ProjectMember{
		ProjectKey: "p1",
		Type:       types.PrincipalGroup,
		Name:       "devs",
		Roles:      []string{"Developer"},
	}
	other := types.ProjectMember{ProjectKey: "p2", Type: types.PrincipalUser, Name: "bob"}
	for _, m := range []types.ProjectMember{bob, ann, devs, other} {
		storekit.Must(t, ps.PutMember(ctx, m))
	}
	storekit.WantErr(
		t,
		ps.PutMember(
			ctx,
			types.ProjectMember{ProjectKey: "nope", Type: types.PrincipalUser, Name: "x"},
		),
		store.ErrNotFound,
	)

	storekit.Same(
		t,
		"Member",
		ann,
		storekit.Get[types.ProjectMember](t)(ps.Member(ctx, "p1", types.PrincipalUser, "ann")),
	)
	storekit.Same(t, "users", []types.ProjectMember{ann, bob},
		storekit.Get[[]types.ProjectMember](t)(ps.Members(ctx, "p1", types.PrincipalUser)))
	storekit.Same(t, "groups", []types.ProjectMember{devs},
		storekit.Get[[]types.ProjectMember](t)(ps.Members(ctx, "p1", types.PrincipalGroup)))
	_, err := ps.Member(ctx, "p1", types.PrincipalGroup, "bob")
	storekit.WantErr(t, err, store.ErrNotFound)

	got := storekit.Get[types.ProjectMember](t)(ps.Member(ctx, "p1", types.PrincipalUser, "ann"))
	got.Roles[0] = "mutated"
	storekit.Same(
		t,
		"Member is a copy",
		ann,
		storekit.Get[types.ProjectMember](t)(ps.Member(ctx, "p1", types.PrincipalUser, "ann")),
	)

	ann.Roles = []string{"Admin"}
	storekit.Must(t, ps.PutMember(ctx, ann))
	storekit.Same(
		t,
		"replaced",
		ann,
		storekit.Get[types.ProjectMember](t)(ps.Member(ctx, "p1", types.PrincipalUser, "ann")),
	)

	storekit.Must(t, ps.DeleteMember(ctx, "p1", types.PrincipalUser, "ann"))
	storekit.WantErr(t, ps.DeleteMember(ctx, "p1", types.PrincipalUser, "ann"), store.ErrNotFound)
	storekit.Same(t, "after delete", []types.ProjectMember{bob},
		storekit.Get[[]types.ProjectMember](t)(ps.Members(ctx, "p1", types.PrincipalUser)))
}

func testAssignments(t *testing.T, s store.Store) {
	ctx := context.Background()
	ps := s.Projects()
	createProjects(t, s, "p1", "p2", "p3")
	a := types.RepositoryAssignment{
		RepoKey: "repo-a", ProjectKey: "p1", Environments: []string{"DEV"},
		SharedWith: []string{"p3", "p2", "p3"}, Updated: storekit.At(1),
	}
	b := types.RepositoryAssignment{RepoKey: "repo-b", ProjectKey: "p2", Updated: storekit.At(2)}
	c := types.RepositoryAssignment{RepoKey: "repo-c", SharedWithAll: true, SharedReadOnly: true}
	d := types.RepositoryAssignment{
		RepoKey:        "repo-d",
		ProjectKey:     "p3",
		SharedWith:     []string{"p1"},
		SharedReadOnly: true,
	}
	for _, v := range []types.RepositoryAssignment{d, c, b, a} {
		storekit.Must(t, ps.PutAssignment(ctx, v))
	}
	storekit.WantErr(
		t,
		ps.PutAssignment(ctx, types.RepositoryAssignment{RepoKey: "x", ProjectKey: "nope"}),
		store.ErrNotFound,
	)

	// Shares come back sorted and without duplicates.
	a.SharedWith = []string{"p2", "p3"}
	storekit.Same(
		t,
		"Assignment",
		a,
		storekit.Get[types.RepositoryAssignment](t)(ps.Assignment(ctx, "repo-a")),
	)
	_, err := ps.Assignment(ctx, "nope")
	storekit.WantErr(t, err, store.ErrNotFound)

	storekit.Same(t, "Assignments p1", []types.RepositoryAssignment{a, c, d},
		storekit.Get[[]types.RepositoryAssignment](t)(ps.Assignments(ctx, "p1")))
	storekit.Same(t, "Assignments p2", []types.RepositoryAssignment{a, b, c},
		storekit.Get[[]types.RepositoryAssignment](t)(ps.Assignments(ctx, "p2")))

	a.SharedWith, a.ProjectKey = nil, "p2"
	storekit.Must(t, ps.PutAssignment(ctx, a))
	storekit.Same(
		t,
		"replaced",
		a,
		storekit.Get[types.RepositoryAssignment](t)(ps.Assignment(ctx, "repo-a")),
	)
	storekit.Same(t, "Assignments p3 after replace", []types.RepositoryAssignment{c, d},
		storekit.Get[[]types.RepositoryAssignment](t)(ps.Assignments(ctx, "p3")))

	storekit.Must(t, ps.DeleteAssignment(ctx, "repo-d"))
	storekit.WantErr(t, ps.DeleteAssignment(ctx, "repo-d"), store.ErrNotFound)
	_, err = ps.Assignment(ctx, "repo-d")
	storekit.WantErr(t, err, store.ErrNotFound)
}
