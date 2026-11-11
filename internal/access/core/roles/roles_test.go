package roles_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/roles"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func world(t *testing.T) (roles.Service, *inmem.Store) {
	t.Helper()
	db := inmem.New()
	ctx := context.Background()
	if err := db.Projects().Create(ctx, model.Project{Key: "p", DisplayName: "P"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Projects().
		CreateEnvironment(ctx, model.Environment{ProjectKey: "p", Name: "PENV"}); err != nil {
		t.Fatal(err)
	}
	return roles.Service{Store: db, Repositories: repositories{db: db}}, db
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestUnitEnvironments(t *testing.T) {
	ctx := context.Background()
	s, db := world(t)
	envs, err := s.Environments(ctx)
	if err != nil || !slices.Equal(envs, []string{"DEV", "PROD"}) {
		t.Fatalf("built-ins %v %v", envs, err)
	}
	must(t, s.CreateEnvironment(ctx, "QA"))
	for name, want := range map[string]error{
		"qa": roles.ErrConflict, "dev": roles.ErrConflict, "penv": roles.ErrConflict,
		"": roles.ErrInvalid, "bad name": roles.ErrInvalid, "x23456789012345678901234567890123": roles.ErrInvalid,
	} {
		if err := s.CreateEnvironment(ctx, name); !errors.Is(err, want) {
			t.Errorf("create %q: %v, want %v", name, err, want)
		}
	}

	// A repository and a custom role in QA follow its rename.
	must(
		t,
		db.Repos().
			Create(ctx, repo.Config{Key: "r", Class: repo.Local, Environments: []string{"QA"}}),
	)
	must(t, db.Repos().Create(ctx, repo.Config{Key: "other", Class: repo.Local}))
	must(
		t,
		s.CreateRole(
			ctx,
			model.Role{Name: "ci", Type: model.RoleCustomGlobal, Environments: []string{"qa"}},
		),
	)
	for from, c := range map[string]struct {
		to       string
		expected error
	}{
		"DEV": {"X", roles.ErrBuiltin}, "MISSING": {"Y", roles.ErrNotFound},
		"QA": {"bad name", roles.ErrInvalid}, "Qa": {"PROD", roles.ErrConflict},
	} {
		if err := s.RenameEnvironment(ctx, from, c.to); !errors.Is(err, c.expected) {
			t.Errorf("rename %s to %s: %v, want %v", from, c.to, err, c.expected)
		}
	}
	must(t, s.RenameEnvironment(ctx, "qa", "qa")) // a change of case only
	must(t, s.RenameEnvironment(ctx, "qa", "STAGE"))
	c, _ := db.Repos().Get(ctx, "r")
	r, _ := s.Role(ctx, "ci")
	if !slices.Equal(c.Environments, []string{"STAGE"}) ||
		!slices.Equal(r.Environments, []string{"STAGE"}) {
		t.Fatalf("rename reached repository %v and role %v", c.Environments, r.Environments)
	}

	// In use by a repository configuration, then by a project assignment.
	for name, want := range map[string]error{
		"PROD": roles.ErrBuiltin, "MISSING": roles.ErrNotFound, "STAGE": roles.ErrInUse,
	} {
		if err := s.DeleteEnvironment(ctx, name); !errors.Is(err, want) {
			t.Errorf("delete %s: %v, want %v", name, err, want)
		}
	}
	c.Environments = nil
	must(t, db.Repos().Update(ctx, c))
	must(t, db.Projects().PutAssignment(ctx, model.RepositoryAssignment{
		RepoKey: "r", ProjectKey: "p", Environments: []string{"STAGE"},
	}))
	if err := s.DeleteEnvironment(ctx, "STAGE"); !errors.Is(err, roles.ErrInUse) {
		t.Fatalf("assigned through a project: %v", err)
	}
	must(t, db.Projects().DeleteAssignment(ctx, "r"))
	// ci still names STAGE, and a role keeps it too.
	if err := s.DeleteEnvironment(ctx, "STAGE"); !errors.Is(err, roles.ErrInUse) {
		t.Fatalf("named by a role: %v", err)
	}
	must(t, s.UpdateRole(ctx, "ci", model.Role{Name: "ci", Type: model.RoleCustomGlobal}))
	must(t, s.DeleteEnvironment(ctx, "stage"))
	if envs, _ := s.Environments(ctx); len(envs) != 2 {
		t.Fatalf("after delete %v", envs)
	}
}

func TestUnitRoles(t *testing.T) {
	ctx := context.Background()
	s, db := world(t)
	all, err := s.Roles(ctx)
	if err != nil || len(all) != len(model.BuiltinGlobalRoles()) || all[0].Type != model.RoleAdmin {
		t.Fatalf("built-in roles %v %v", all, err)
	}
	if r, err := s.Role(ctx, "Viewer"); err != nil || r.Type != model.RolePredefined {
		t.Fatalf("Viewer %+v %v", r, err)
	}
	if _, err := s.Role(ctx, "missing"); !errors.Is(err, roles.ErrNotFound) {
		t.Fatalf("missing role: %v", err)
	}
	ci := model.Role{
		Name: "ci", Type: model.RoleCustomGlobal, Environments: []string{"dev", "PROD", "DEV"},
		Actions: []string{"READ_BUILD", "READ_REPOSITORY", "READ_BUILD"},
	}
	must(t, s.CreateRole(ctx, ci))
	got, _ := s.Role(ctx, "ci")
	if !slices.Equal(got.Environments, []string{"DEV", "PROD"}) ||
		!slices.Equal(got.Actions, []string{"READ_BUILD", "READ_REPOSITORY"}) {
		t.Fatalf("canonical %+v", got)
	}
	for name, c := range map[string]struct {
		r        model.Role
		expected error
	}{
		"existing":    {ci, roles.ErrConflict},
		"built in":    {model.Role{Name: "Viewer", Type: model.RoleCustomGlobal}, roles.ErrConflict},
		"no name":     {model.Role{Name: " ", Type: model.RoleCustomGlobal}, roles.ErrInvalid},
		"wrong type":  {model.Role{Name: "x", Type: model.RoleAdmin}, roles.ErrInvalid},
		"bad action":  {model.Role{Name: "x", Type: model.RoleCustomGlobal, Actions: []string{"FLY"}}, roles.ErrInvalid},
		"unknown env": {model.Role{Name: "x", Type: model.RoleCustomGlobal, Environments: []string{"NOPE"}}, roles.ErrInvalid},
	} {
		if err := s.CreateRole(ctx, c.r); !errors.Is(err, c.expected) {
			t.Errorf("create %s: %v, want %v", name, err, c.expected)
		}
	}

	update := model.Role{
		Name:    "ci",
		Type:    model.RoleCustomGlobal,
		Actions: []string{"DEPLOY_BUILD"},
	}
	for name, c := range map[string]struct {
		path     string
		r        model.Role
		expected error
	}{
		"admin":           {"Project Admin", model.Role{Name: "Project Admin", Type: model.RoleAdmin}, roles.ErrBuiltin},
		"predefined type": {"Viewer", model.Role{Name: "Viewer", Type: model.RoleCustomGlobal}, roles.ErrInvalid},
		"rename":          {"Viewer", model.Role{Name: "Watcher", Type: model.RolePredefined}, roles.ErrInvalid},
		"predefined env":  {"Viewer", model.Role{Name: "Viewer", Type: model.RolePredefined, Environments: []string{"NOPE"}}, roles.ErrInvalid},
		"mismatch":        {"ci", model.Role{Name: "other", Type: model.RoleCustomGlobal}, roles.ErrInvalid},
		"missing":         {"gone", model.Role{Name: "gone", Type: model.RoleCustomGlobal}, roles.ErrNotFound},
		"invalid":         {"ci", model.Role{Name: "ci", Type: model.RolePredefined}, roles.ErrInvalid},
	} {
		if err := s.UpdateRole(ctx, c.path, c.r); !errors.Is(err, c.expected) {
			t.Errorf("update %s: %v, want %v", name, err, c.expected)
		}
	}
	must(t, s.UpdateRole(ctx, "ci", update))
	if got, _ := s.Role(ctx, "ci"); !slices.Equal(got.Actions, []string{"DEPLOY_BUILD"}) {
		t.Fatalf("updated %+v", got)
	}

	// A predefined role's actions and environments change and read back,
	// its description stays, and the change is not a custom role.
	viewer, _ := model.BuiltinGlobalRole("Viewer")
	must(t, s.UpdateRole(ctx, "Viewer", model.Role{
		Name: "Viewer", Type: model.RolePredefined, Description: "ignored",
		Actions: []string{"READ_BUILD"}, Environments: []string{"prod"},
	}))
	got, err = s.Role(ctx, "Viewer")
	if err != nil || got.Type != model.RolePredefined || got.Description != viewer.Description ||
		!slices.Equal(
			got.Actions,
			[]string{"READ_BUILD"},
		) || !slices.Equal(got.Environments, []string{"PROD"}) {
		t.Fatalf("changed Viewer %+v %v", got, err)
	}
	all, err = s.Roles(ctx)
	if err != nil || len(all) != len(model.BuiltinGlobalRoles())+1 {
		t.Fatalf("the change to Viewer is listed as a role of its own: %v %v", all, err)
	}
	for _, r := range all {
		if r.Name == "Viewer" && !slices.Equal(r.Actions, []string{"READ_BUILD"}) {
			t.Fatalf("the list shows Viewer's defaults: %+v", r)
		}
	}

	// Deleting ci removes it from memberships; one left with no role goes.
	must(t, db.Projects().PutMember(ctx, model.ProjectMember{
		ProjectKey: "p", Type: model.PrincipalUser, Name: "ann", Roles: []string{"ci", "Viewer"},
	}))
	must(t, db.Projects().PutMember(ctx, model.ProjectMember{
		ProjectKey: "p", Type: model.PrincipalGroup, Name: "devs", Roles: []string{"ci"},
	}))
	for name, want := range map[string]error{"Viewer": roles.ErrBuiltin, "gone": roles.ErrNotFound} {
		if err := s.DeleteRole(ctx, name); !errors.Is(err, want) {
			t.Errorf("delete %s: %v, want %v", name, err, want)
		}
	}
	must(t, s.DeleteRole(ctx, "ci"))
	if m, err := db.Projects().Member(ctx, "p", model.PrincipalUser, "ann"); err != nil ||
		!slices.Equal(m.Roles, []string{"Viewer"}) {
		t.Fatalf("ann %+v %v", m, err)
	}
	if _, err := db.Projects().
		Member(ctx, "p", model.PrincipalGroup, "devs"); !errors.Is(
		err,
		kernel.ErrNotFound,
	) {
		t.Fatalf("devs kept with no role: %v", err)
	}
}

func TestUnitCustomRoleLimit(t *testing.T) {
	ctx := context.Background()
	s, _ := world(t)
	for i := range model.MaxCustomGlobalRoles {
		must(
			t,
			s.CreateRole(
				ctx,
				model.Role{
					Name: string(rune('a'+i%26)) + string(rune('0'+i/26)),
					Type: model.RoleCustomGlobal,
				},
			),
		)
	}
	if err := s.CreateRole(
		ctx,
		model.Role{Name: "one-too-many", Type: model.RoleCustomGlobal},
	); !errors.Is(
		err,
		roles.ErrInvalid,
	) {
		t.Fatalf("the 31st custom role: %v", err)
	}
}

// TestUnitGlobalEnvironmentLimit checks at most GlobalLimit global
// environments exist, DEV and PROD among them, ten unless set
// (createglobalenvironment), and project environments do not count.
func TestUnitGlobalEnvironmentLimit(t *testing.T) {
	ctx := context.Background()
	s, _ := world(t)
	for i := range roles.DefaultGlobalEnvironmentLimit - 2 {
		must(t, s.CreateEnvironment(ctx, fmt.Sprintf("E%d", i)))
	}
	err := s.CreateEnvironment(ctx, "ELEVENTH")
	if !errors.Is(err, roles.ErrInvalid) ||
		err.Error() != "at most 10 global environments may exist: invalid" {
		t.Fatalf("the 11th global environment: %v", err)
	}
	// A name in use is still a conflict at the limit.
	if err := s.CreateEnvironment(ctx, "e0"); !errors.Is(err, roles.ErrConflict) {
		t.Fatalf("a taken name at the limit: %v", err)
	}
	// Deleting one makes room again.
	must(t, s.DeleteEnvironment(ctx, "E0"))
	must(t, s.CreateEnvironment(ctx, "ELEVENTH"))

	_, db := world(t)
	small := roles.Service{Store: db, GlobalLimit: 3}
	must(t, small.CreateEnvironment(ctx, "QA"))
	if err := small.CreateEnvironment(ctx, "STAGE"); !errors.Is(err, roles.ErrInvalid) {
		t.Fatalf("a fourth with a limit of 3: %v", err)
	}
}
