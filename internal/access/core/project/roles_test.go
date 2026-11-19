package project_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	"github.com/weaveplatform/artifactweave/internal/access/core/roles"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func world(t *testing.T) (project.Service, *inmem.Store) {
	t.Helper()
	db := inmem.New()
	for _, key := range []string{"alpha", "beta"} {
		must(t, db.Projects().Create(t.Context(), model.Project{Key: key, DisplayName: key}))
	}
	for _, e := range []model.Environment{{Name: "QA"}, {ProjectKey: "alpha", Name: "alpha-stage"}, {ProjectKey: "beta", Name: "beta-stage"}} {
		must(t, db.Projects().CreateEnvironment(t.Context(), e))
	}
	return project.Service{Store: db}, db
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func custom(name string) model.Role {
	return model.Role{Name: name, Type: model.RoleCustom, Actions: []string{"READ_REPOSITORY"}, Environments: []string{"DEV"}}
}

func TestUnitProjectRoleLifecycleAndGlobalOverrides(t *testing.T) {
	s, db := world(t)
	ctx := t.Context()
	global := roles.Service{Store: db}
	must(t, global.CreateRole(ctx, model.Role{Name: "auditor", Type: model.RoleCustomGlobal, Actions: []string{"READ_BUILD"}, Environments: []string{"DEV"}}))
	must(
		t,
		global.UpdateRole(
			ctx,
			"Viewer",
			model.Role{Name: "Viewer", Type: model.RolePredefined, Actions: []string{"READ_REPOSITORY", "DEPLOY_CACHE_REPOSITORY"}, Environments: []string{"PROD"}},
		),
	)
	r := custom("reader")
	r.ProjectKey = "beta" // the path owns the scope; a body cannot select another project
	r.Actions = []string{"READ_REPOSITORY", "MANAGE_MEMBERS", "READ_REPOSITORY"}
	r.Environments = []string{"dev", "qa", "ALPHA-STAGE", "DEV"}
	must(t, s.CreateRole(ctx, "alpha", r))
	r.Actions[0], r.Environments[0] = "DEPLOY_CACHE_REPOSITORY", "PROD"
	got, err := s.Role(ctx, "alpha", "reader")
	must(t, err)
	if got.ProjectKey != "alpha" || !slices.Equal(got.Actions, []string{"READ_REPOSITORY", "MANAGE_MEMBERS"}) ||
		!slices.Equal(got.Environments, []string{"DEV", "QA", "alpha-stage"}) {
		t.Fatalf("stored role after normalization and caller mutation: %+v", got)
	}
	got.Actions[0] = "DEPLOY_CACHE_REPOSITORY"
	got, err = s.Role(ctx, "alpha", "reader")
	must(t, err)
	if got.Actions[0] != "READ_REPOSITORY" {
		t.Fatal("mutating a returned role changed the stored role")
	}
	viewer, err := s.Role(ctx, "alpha", "Viewer")
	must(t, err)
	if viewer.ProjectKey != "" || !slices.Equal(viewer.Actions, []string{"READ_REPOSITORY", "DEPLOY_CACHE_REPOSITORY"}) ||
		!slices.Equal(viewer.Environments, []string{"PROD"}) {
		t.Fatalf("project did not see the current global override: %+v", viewer)
	}
	all, err := s.Roles(ctx, "alpha")
	must(t, err)
	if len(all) != len(model.BuiltinGlobalRoles())+2 || all[len(all)-1].Name != "reader" {
		t.Fatalf("project role list: %+v", all)
	}
	if _, err := s.Role(ctx, "beta", "reader"); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("another project inherited the role: %v", err)
	}
	updated := custom("reader")
	updated.Description, updated.Actions = "replacement", []string{"DEPLOY_CACHE_REPOSITORY"}
	must(t, s.UpdateRole(ctx, "alpha", "reader", updated))
	got, err = s.Role(ctx, "alpha", "reader")
	must(t, err)
	if got.Description != "replacement" || !slices.Equal(got.Actions, updated.Actions) {
		t.Fatalf("role replacement: %+v", got)
	}
}

func TestUnitProjectRoleValidationLeavesPolicyUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name string
		role model.Role
	}{
		{"blank name", model.Role{Name: " \t", Type: model.RoleCustom}},
		{"global type", model.Role{Name: "bad", Type: model.RoleCustomGlobal}},
		{"predefined type", model.Role{Name: "bad", Type: model.RolePredefined}},
		{"unknown action", model.Role{Name: "bad", Type: model.RoleCustom, Actions: []string{"READ_REPOSITORY", "UNKNOWN"}}},
		{"global-only action", model.Role{Name: "bad", Type: model.RoleCustom, Actions: []string{"READ_POLICIES_SECURITY"}}},
		{"another project's environment", model.Role{Name: "bad", Type: model.RoleCustom, Environments: []string{"beta-stage"}}},
		{"unknown environment", model.Role{Name: "bad", Type: model.RoleCustom, Environments: []string{"DEV", "missing"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db := world(t)
			before, err := db.Projects().Authority(t.Context(), "alpha")
			must(t, err)
			if err := s.CreateRole(t.Context(), "alpha", tc.role); !errors.Is(err, project.ErrInvalid) {
				t.Fatalf("invalid role: %v", err)
			}
			after, err := db.Projects().Authority(t.Context(), "alpha")
			must(t, err)
			if after != before {
				t.Fatal("rejected role changed project authority")
			}
			stored, err := db.Projects().Roles(t.Context(), "alpha")
			must(t, err)
			if len(stored) != 0 {
				t.Fatalf("invalid role was partially stored: %+v", stored)
			}
		})
	}
}

func TestUnitProjectRoleDeleteCleansOnlyItsProjectMemberships(t *testing.T) {
	s, db := world(t)
	ctx := t.Context()
	must(t, s.CreateRole(ctx, "alpha", custom("reader")))
	must(t, s.CreateRole(ctx, "beta", custom("reader")))
	for _, m := range []model.ProjectMember{
		{ProjectKey: "alpha", Type: model.PrincipalUser, Name: "alice", Roles: []string{"reader", "Viewer"}},
		{ProjectKey: "alpha", Type: model.PrincipalUser, Name: "bob", Roles: []string{"reader"}},
		{ProjectKey: "alpha", Type: model.PrincipalGroup, Name: "team", Roles: []string{"reader"}},
		{ProjectKey: "beta", Type: model.PrincipalUser, Name: "bob", Roles: []string{"reader"}},
	} {
		must(t, db.Projects().PutMember(ctx, m))
	}
	before, err := db.Projects().Authority(ctx, "alpha")
	must(t, err)
	must(t, s.DeleteRole(ctx, "alpha", "reader"))
	if _, err := s.Role(ctx, "alpha", "reader"); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("deleted role: %v", err)
	}
	remaining, err := db.Projects().Member(ctx, "alpha", model.PrincipalUser, "alice")
	must(t, err)
	if !slices.Equal(remaining.Roles, []string{"Viewer"}) {
		t.Fatalf("other roles were removed: %+v", remaining)
	}
	for _, m := range []model.ProjectMember{{Type: model.PrincipalUser, Name: "bob"}, {Type: model.PrincipalGroup, Name: "team"}} {
		if _, err := db.Projects().Member(ctx, "alpha", m.Type, m.Name); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("role-less membership survived: %s %s: %v", m.Type, m.Name, err)
		}
	}
	if _, err := db.Projects().Member(ctx, "beta", model.PrincipalUser, "bob"); err != nil {
		t.Fatalf("another project's membership was removed: %v", err)
	}
	after, err := db.Projects().Authority(ctx, "alpha")
	must(t, err)
	if after.Revision == before.Revision || after.Incarnation != before.Incarnation {
		t.Fatal("role deletion did not fence the old policy within the same project lifetime")
	}
}

func TestUnitProjectRoleRejectsShadowingAndCrossScopeMutations(t *testing.T) {
	s, db := world(t)
	ctx := t.Context()
	must(t, s.CreateRole(ctx, "alpha", custom("reader")))
	for _, name := range []string{"reader", "Viewer", "Project Admin"} {
		if err := s.CreateRole(ctx, "alpha", custom(name)); !errors.Is(err, project.ErrConflict) {
			t.Errorf("duplicate role %q: %v", name, err)
		}
	}
	for _, name := range []string{"Viewer", "Project Admin"} {
		if err := s.UpdateRole(ctx, "alpha", name, custom(name)); !errors.Is(err, project.ErrImmutable) {
			t.Errorf("project update of global role %q: %v", name, err)
		}
		if err := s.DeleteRole(ctx, "alpha", name); !errors.Is(err, project.ErrImmutable) {
			t.Errorf("project delete of global role %q: %v", name, err)
		}
	}
	if err := s.UpdateRole(ctx, "alpha", "reader", custom("renamed")); !errors.Is(err, project.ErrInvalid) {
		t.Fatalf("role rename was accepted: %v", err)
	}
	// Existing/imported collisions must not silently substitute a broader
	// global role for a narrow project role. Public collision semantics still
	// require the contract audit before the new service is wired to HTTP.
	must(t, db.Projects().PutRole(ctx, model.Role{Name: "reader", Type: model.RoleCustomGlobal, Actions: []string{"DEPLOY_CACHE_REPOSITORY"}}))
	if _, err := s.Role(ctx, "alpha", "reader"); !errors.Is(err, project.ErrAmbiguousRole) {
		t.Fatalf("ambiguous role supplied permissions from one scope: %v", err)
	}
	if list, err := s.Roles(ctx, "alpha"); list != nil || !errors.Is(err, project.ErrAmbiguousRole) {
		t.Fatalf("ambiguous roles produced a partial list: %+v %v", list, err)
	}
	if err := s.DeleteRole(ctx, "alpha", "reader"); !errors.Is(err, project.ErrAmbiguousRole) {
		t.Fatalf("ambiguous role selected a deletion scope: %v", err)
	}
}
