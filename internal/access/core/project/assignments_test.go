package project_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	"github.com/weaveplatform/artifactweave/internal/access/core/roles"
)

func roleNames(all []model.Role) []string {
	names := make([]string, len(all))
	for i, r := range all {
		names[i] = r.Name
	}
	return names
}

func TestUnitProjectAssignedRolesKeepMembershipAndProjectBoundaries(t *testing.T) {
	s, db := world(t)
	ctx := t.Context()
	must(t, db.Users().Create(ctx, model.User{Name: "alice"}))
	must(t, db.Groups().Create(ctx, model.Group{Name: "team", Members: []string{"alice"}}))
	must(t, s.CreateRole(ctx, "alpha", custom("reader")))
	for _, m := range []model.ProjectMember{
		{ProjectKey: "alpha", Type: model.PrincipalUser, Name: "alice", Roles: []string{"reader", "Viewer"}},
		{ProjectKey: "alpha", Type: model.PrincipalGroup, Name: "team", Roles: []string{"Viewer", "Developer"}},
		{ProjectKey: "beta", Type: model.PrincipalUser, Name: "alice", Roles: []string{"Project Admin"}},
	} {
		_, err := s.PutMember(ctx, m.ProjectKey, m.Type, m.Name, m.Roles)
		must(t, err)
	}
	all, err := s.AssignedRoles(ctx, "alpha", "ALICE", []string{"team", "team", "missing"})
	must(t, err)
	if !slices.Equal(roleNames(all), []string{"reader", "Viewer", "Developer"}) {
		t.Fatalf("effective assignments leaked projects or duplicated inherited roles: %+v", all)
	}
	all, err = s.AssignedRoles(ctx, "alpha", "alice", nil)
	must(t, err)
	if !slices.Equal(roleNames(all), []string{"reader", "Viewer"}) {
		t.Fatalf("unselected groups supplied roles: %+v", all)
	}
	must(t, db.Groups().Delete(ctx, "team"))
	all, err = s.AssignedRoles(ctx, "alpha", "alice", []string{"team"})
	must(t, err)
	if !slices.Equal(roleNames(all), []string{"reader", "Viewer"}) {
		t.Fatalf("deleted group retained a project grant: %+v", all)
	}
	must(t, db.Users().Delete(ctx, "alice"))
	all, err = s.AssignedRoles(ctx, "alpha", "ALICE", nil)
	must(t, err)
	if len(all) != 0 {
		t.Fatalf("deleted user retained project grants: %+v", all)
	}
}

func TestUnitProjectNamedRolesResolveCurrentDefinitionsWithoutMembership(t *testing.T) {
	s, db := world(t)
	ctx := t.Context()
	must(t, s.CreateRole(ctx, "alpha", custom("ci-reader")))
	all, err := s.NamedRoles(ctx, "alpha", []string{"ci-reader", "Viewer", "ci-reader", "missing"})
	must(t, err)
	if !slices.Equal(roleNames(all), []string{"ci-reader", "Viewer"}) {
		t.Fatalf("named role lookup: %+v", all)
	}
	global := roles.Service{Store: db}
	must(t, global.UpdateRole(ctx, "Viewer", model.Role{Name: "Viewer", Type: model.RolePredefined, Actions: []string{"READ_BUILD"}, Environments: []string{"PROD"}}))
	must(t, s.DeleteRole(ctx, "alpha", "ci-reader"))
	all, err = s.NamedRoles(ctx, "alpha", []string{"ci-reader", "Viewer"})
	must(t, err)
	if len(all) != 1 || !slices.Equal(all[0].Actions, []string{"READ_BUILD"}) || !slices.Equal(all[0].Environments, []string{"PROD"}) {
		t.Fatalf("named role token used a stale definition or environment: %+v", all)
	}
	if _, err := s.NamedRoles(ctx, "missing", []string{"Viewer"}); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("missing project was treated as a global role grant: %v", err)
	}
}
