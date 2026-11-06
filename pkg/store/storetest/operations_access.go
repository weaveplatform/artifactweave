package storetest

import (
	"context"
	"testing"

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
		must(t, s.Projects().Create(context.Background(), project(k)))
	}
}

func testProjects(t *testing.T, s store.Store) {
	ctx := context.Background()
	ps := s.Projects()
	createProjects(t, s, "charlie", "alpha", "bravo")
	wantErr(t, ps.Create(ctx, project("alpha")), store.ErrConflict)

	same(t, "Get", project("alpha"), get[types.Project](t)(ps.Get(ctx, "alpha")))
	_, err := ps.Get(ctx, "nope")
	wantErr(t, err, store.ErrNotFound)
	same(t, "List", []types.Project{project("alpha"), project("bravo"), project("charlie")},
		get[[]types.Project](t)(ps.List(ctx)))

	changed := project("alpha")
	changed.Description, changed.AdminPrivileges, changed.QuotaEmail = "renamed", types.AdminPrivileges{}, true
	must(t, ps.Update(ctx, changed))
	same(t, "Get after Update", changed, get[types.Project](t)(ps.Get(ctx, "alpha")))
	wantErr(t, ps.Update(ctx, project("nope")), store.ErrNotFound)

	// Deleting a project takes its roles, environments, members, owned
	// assignments and its shares of other projects' repositories with it.
	must(t, ps.PutRole(ctx, types.Role{ProjectKey: "alpha", Name: "dev", Type: types.RoleCustom}))
	must(t, ps.CreateEnvironment(ctx, types.Environment{ProjectKey: "alpha", Name: "alpha-QA"}))
	must(
		t,
		ps.PutMember(
			ctx,
			types.ProjectMember{ProjectKey: "alpha", Type: types.PrincipalUser, Name: "ann"},
		),
	)
	must(
		t,
		ps.PutAssignment(
			ctx,
			types.RepositoryAssignment{RepoKey: "alpha-libs", ProjectKey: "alpha"},
		),
	)
	must(t, ps.PutAssignment(ctx, types.RepositoryAssignment{
		RepoKey: "bravo-libs", ProjectKey: "bravo", SharedWith: []string{"alpha", "charlie"},
	}))
	must(t, ps.Delete(ctx, "alpha"))
	wantErr(t, ps.Delete(ctx, "alpha"), store.ErrNotFound)
	_, err = ps.Get(ctx, "alpha")
	wantErr(t, err, store.ErrNotFound)
	if roles := get[[]types.Role](t)(ps.Roles(ctx, "alpha")); len(roles) != 0 {
		t.Fatalf("roles left: %v", roles)
	}
	if envs := get[[]types.Environment](t)(ps.Environments(ctx, "alpha")); len(envs) != 0 {
		t.Fatalf("environments left: %v", envs)
	}
	_, err = ps.Member(ctx, "alpha", types.PrincipalUser, "ann")
	wantErr(t, err, store.ErrNotFound)
	_, err = ps.Assignment(ctx, "alpha-libs")
	wantErr(t, err, store.ErrNotFound)
	same(t, "shares after delete", []string{"charlie"},
		get[types.RepositoryAssignment](t)(ps.Assignment(ctx, "bravo-libs")).SharedWith)
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
		must(t, ps.PutRole(ctx, r))
	}
	wantErr(t, ps.PutRole(ctx, types.Role{ProjectKey: "nope", Name: "x"}), store.ErrNotFound)

	same(t, "Role", reader, get[types.Role](t)(ps.Role(ctx, "", "Reader")))
	same(t, "global Roles", []types.Role{admin, reader}, get[[]types.Role](t)(ps.Roles(ctx, "")))
	same(t, "project Roles", []types.Role{dev}, get[[]types.Role](t)(ps.Roles(ctx, "p1")))
	_, err := ps.Role(ctx, "p1", "Reader")
	wantErr(t, err, store.ErrNotFound)

	got := get[types.Role](t)(ps.Role(ctx, "", "Reader"))
	got.Environments[0] = "mutated"
	same(t, "Role is a copy", reader, get[types.Role](t)(ps.Role(ctx, "", "Reader")))

	reader.Description, reader.Environments = "reads everything", nil
	must(t, ps.PutRole(ctx, reader))
	same(t, "replaced Role", reader, get[types.Role](t)(ps.Role(ctx, "", "Reader")))

	must(t, ps.DeleteRole(ctx, "", "Reader"))
	wantErr(t, ps.DeleteRole(ctx, "", "Reader"), store.ErrNotFound)
	_, err = ps.Role(ctx, "", "Reader")
	wantErr(t, err, store.ErrNotFound)
}

func testEnvironments(t *testing.T, s store.Store) {
	ctx := context.Background()
	ps := s.Projects()
	createProjects(t, s, "p1")
	for _, e := range []types.Environment{
		{Name: "PROD"}, {Name: "DEV"}, {ProjectKey: "p1", Name: "p1-STAGE"},
	} {
		must(t, ps.CreateEnvironment(ctx, e))
	}
	// Names are unique across scopes.
	wantErr(
		t,
		ps.CreateEnvironment(ctx, types.Environment{ProjectKey: "p1", Name: "DEV"}),
		store.ErrConflict,
	)
	wantErr(
		t,
		ps.CreateEnvironment(ctx, types.Environment{ProjectKey: "nope", Name: "X"}),
		store.ErrNotFound,
	)
	same(t, "global", []types.Environment{{Name: "DEV"}, {Name: "PROD"}},
		get[[]types.Environment](t)(ps.Environments(ctx, "")))
	same(t, "project", []types.Environment{{ProjectKey: "p1", Name: "p1-STAGE"}},
		get[[]types.Environment](t)(ps.Environments(ctx, "p1")))

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
	must(t, ps.PutRole(ctx, role))
	must(t, ps.PutRole(ctx, untouched))
	assigned := types.RepositoryAssignment{
		RepoKey:      "p1-libs",
		ProjectKey:   "p1",
		Environments: []string{"p1-STAGE"},
	}
	must(t, ps.PutAssignment(ctx, assigned))

	// Renaming to the same name changes nothing.
	must(t, ps.RenameEnvironment(ctx, "p1", "p1-STAGE", "p1-STAGE"))
	same(t, "role after no-op rename", role, get[types.Role](t)(ps.Role(ctx, "p1", "qa")))
	must(t, ps.RenameEnvironment(ctx, "p1", "p1-STAGE", "p1-QA"))
	same(t, "renamed", []types.Environment{{ProjectKey: "p1", Name: "p1-QA"}},
		get[[]types.Environment](t)(ps.Environments(ctx, "p1")))
	role.Environments = []string{"DEV", "p1-QA"}
	same(t, "role after rename", role, get[types.Role](t)(ps.Role(ctx, "p1", "qa")))
	same(t, "untouched role", untouched, get[types.Role](t)(ps.Role(ctx, "", "ops")))
	assigned.Environments = []string{"p1-QA"}
	same(t, "assignment after rename", assigned,
		get[types.RepositoryAssignment](t)(ps.Assignment(ctx, "p1-libs")))

	wantErr(t, ps.RenameEnvironment(ctx, "p1", "p1-QA", "DEV"), store.ErrConflict)
	wantErr(t, ps.RenameEnvironment(ctx, "p1", "nope", "X"), store.ErrNotFound)
	wantErr(t, ps.RenameEnvironment(ctx, "", "p1-QA", "X"), store.ErrNotFound)

	wantErr(t, ps.DeleteEnvironment(ctx, "", "p1-QA"), store.ErrNotFound)
	must(t, ps.DeleteEnvironment(ctx, "p1", "p1-QA"))
	wantErr(t, ps.DeleteEnvironment(ctx, "p1", "p1-QA"), store.ErrNotFound)
	if envs := get[[]types.Environment](t)(ps.Environments(ctx, "p1")); len(envs) != 0 {
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
		must(t, ps.PutMember(ctx, m))
	}
	wantErr(
		t,
		ps.PutMember(
			ctx,
			types.ProjectMember{ProjectKey: "nope", Type: types.PrincipalUser, Name: "x"},
		),
		store.ErrNotFound,
	)

	same(
		t,
		"Member",
		ann,
		get[types.ProjectMember](t)(ps.Member(ctx, "p1", types.PrincipalUser, "ann")),
	)
	same(t, "users", []types.ProjectMember{ann, bob},
		get[[]types.ProjectMember](t)(ps.Members(ctx, "p1", types.PrincipalUser)))
	same(t, "groups", []types.ProjectMember{devs},
		get[[]types.ProjectMember](t)(ps.Members(ctx, "p1", types.PrincipalGroup)))
	_, err := ps.Member(ctx, "p1", types.PrincipalGroup, "bob")
	wantErr(t, err, store.ErrNotFound)

	got := get[types.ProjectMember](t)(ps.Member(ctx, "p1", types.PrincipalUser, "ann"))
	got.Roles[0] = "mutated"
	same(
		t,
		"Member is a copy",
		ann,
		get[types.ProjectMember](t)(ps.Member(ctx, "p1", types.PrincipalUser, "ann")),
	)

	ann.Roles = []string{"Admin"}
	must(t, ps.PutMember(ctx, ann))
	same(
		t,
		"replaced",
		ann,
		get[types.ProjectMember](t)(ps.Member(ctx, "p1", types.PrincipalUser, "ann")),
	)

	must(t, ps.DeleteMember(ctx, "p1", types.PrincipalUser, "ann"))
	wantErr(t, ps.DeleteMember(ctx, "p1", types.PrincipalUser, "ann"), store.ErrNotFound)
	same(t, "after delete", []types.ProjectMember{bob},
		get[[]types.ProjectMember](t)(ps.Members(ctx, "p1", types.PrincipalUser)))
}

func testAssignments(t *testing.T, s store.Store) {
	ctx := context.Background()
	ps := s.Projects()
	createProjects(t, s, "p1", "p2", "p3")
	a := types.RepositoryAssignment{
		RepoKey: "repo-a", ProjectKey: "p1", Environments: []string{"DEV"},
		SharedWith: []string{"p3", "p2", "p3"}, Updated: at(1),
	}
	b := types.RepositoryAssignment{RepoKey: "repo-b", ProjectKey: "p2", Updated: at(2)}
	c := types.RepositoryAssignment{RepoKey: "repo-c", SharedWithAll: true, SharedReadOnly: true}
	d := types.RepositoryAssignment{
		RepoKey:        "repo-d",
		ProjectKey:     "p3",
		SharedWith:     []string{"p1"},
		SharedReadOnly: true,
	}
	for _, v := range []types.RepositoryAssignment{d, c, b, a} {
		must(t, ps.PutAssignment(ctx, v))
	}
	wantErr(
		t,
		ps.PutAssignment(ctx, types.RepositoryAssignment{RepoKey: "x", ProjectKey: "nope"}),
		store.ErrNotFound,
	)

	// Shares come back sorted and without duplicates.
	a.SharedWith = []string{"p2", "p3"}
	same(t, "Assignment", a, get[types.RepositoryAssignment](t)(ps.Assignment(ctx, "repo-a")))
	_, err := ps.Assignment(ctx, "nope")
	wantErr(t, err, store.ErrNotFound)

	same(t, "Assignments p1", []types.RepositoryAssignment{a, c, d},
		get[[]types.RepositoryAssignment](t)(ps.Assignments(ctx, "p1")))
	same(t, "Assignments p2", []types.RepositoryAssignment{a, b, c},
		get[[]types.RepositoryAssignment](t)(ps.Assignments(ctx, "p2")))

	a.SharedWith, a.ProjectKey = nil, "p2"
	must(t, ps.PutAssignment(ctx, a))
	same(t, "replaced", a, get[types.RepositoryAssignment](t)(ps.Assignment(ctx, "repo-a")))
	same(t, "Assignments p3 after replace", []types.RepositoryAssignment{c, d},
		get[[]types.RepositoryAssignment](t)(ps.Assignments(ctx, "p3")))

	must(t, ps.DeleteAssignment(ctx, "repo-d"))
	wantErr(t, ps.DeleteAssignment(ctx, "repo-d"), store.ErrNotFound)
	_, err = ps.Assignment(ctx, "repo-d")
	wantErr(t, err, store.ErrNotFound)
}
