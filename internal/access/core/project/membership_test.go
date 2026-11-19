package project_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
)

func TestUnitProjectDirectMembershipReplacementAndGroupIsolation(t *testing.T) {
	s, db := world(t)
	ctx := t.Context()
	must(t, db.Users().Create(ctx, model.User{Name: "alice"}))
	must(t, db.Groups().Create(ctx, model.Group{Name: "team", Members: []string{"alice"}}))
	must(t, s.CreateRole(ctx, "alpha", custom("reader")))
	names := []string{"reader", "Viewer", "reader"}
	member, err := s.PutMember(ctx, "alpha", model.PrincipalUser, "ALICE", names)
	must(t, err)
	names[0] = "Project Admin"
	if member.Name != "alice" || !slices.Equal(member.Roles, []string{"reader", "Viewer"}) {
		t.Fatalf("direct membership: %+v", member)
	}
	member.Roles[0] = "Project Admin"
	member, err = s.Member(ctx, "alpha", model.PrincipalUser, "Alice")
	must(t, err)
	if !slices.Equal(member.Roles, []string{"reader", "Viewer"}) {
		t.Fatalf("caller mutation changed membership: %+v", member)
	}
	_, err = s.PutMember(ctx, "alpha", model.PrincipalGroup, "team", []string{"Developer"})
	must(t, err)
	_, err = s.PutMember(ctx, "alpha", model.PrincipalUser, "alice", []string{"Viewer"})
	must(t, err)
	members, err := s.Members(ctx, "alpha", model.PrincipalUser)
	must(t, err)
	if len(members) != 1 || !slices.Equal(members[0].Roles, []string{"Viewer"}) {
		t.Fatalf("PUT did not replace only the direct roles: %+v", members)
	}
	must(t, s.DeleteMember(ctx, "alpha", model.PrincipalUser, "ALICE"))
	if _, err := s.Member(ctx, "alpha", model.PrincipalUser, "alice"); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("removed direct member: %v", err)
	}
	group, err := s.Member(ctx, "alpha", model.PrincipalGroup, "team")
	must(t, err)
	if !slices.Equal(group.Roles, []string{"Developer"}) {
		t.Fatalf("direct user removal changed group assignment: %+v", group)
	}
	identity, err := db.Groups().Get(ctx, "team")
	must(t, err)
	if !slices.Equal(identity.Members, []string{"alice"}) {
		t.Fatalf("project membership changed platform group membership: %+v", identity)
	}
}

func TestUnitProjectMembershipValidationDoesNotPartiallyAssignRoles(t *testing.T) {
	s, db := world(t)
	ctx := t.Context()
	must(t, db.Users().Create(ctx, model.User{Name: "alice"}))
	must(t, s.CreateRole(ctx, "alpha", custom("reader")))
	for _, tc := range []struct {
		name  string
		typ   model.PrincipalType
		who   string
		roles []string
		want  error
	}{
		{"missing user", model.PrincipalUser, "missing", []string{"Viewer"}, project.ErrNotFound},
		{"missing group", model.PrincipalGroup, "missing", []string{"Viewer"}, project.ErrNotFound},
		{"unknown principal type", "service", "alice", []string{"Viewer"}, project.ErrInvalid},
		{"empty roles", model.PrincipalUser, "alice", nil, project.ErrInvalid},
		{"unknown later role", model.PrincipalUser, "alice", []string{"reader", "missing"}, project.ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := db.Projects().Authority(t.Context(), "alpha")
			must(t, err)
			if _, err := s.PutMember(t.Context(), "alpha", tc.typ, tc.who, tc.roles); !errors.Is(err, tc.want) {
				t.Fatalf("invalid membership: %v, want %v", err, tc.want)
			}
			after, err := db.Projects().Authority(t.Context(), "alpha")
			must(t, err)
			if before != after {
				t.Fatal("rejected membership changed project authority")
			}
			all, err := s.Members(t.Context(), "alpha", model.PrincipalUser)
			must(t, err)
			if len(all) != 0 {
				t.Fatalf("rejected membership left assigned roles: %+v", all)
			}
		})
	}
}

func TestUnitProjectMembershipCanBeRemovedAfterPrincipalDeletion(t *testing.T) {
	s, db := world(t)
	ctx := t.Context()
	must(t, db.Users().Create(ctx, model.User{Name: "alice"}))
	_, err := s.PutMember(ctx, "alpha", model.PrincipalUser, "alice", []string{"Viewer"})
	must(t, err)
	must(t, db.Users().Delete(ctx, "alice"))
	must(t, s.DeleteMember(ctx, "alpha", model.PrincipalUser, "ALICE"))
	if _, err := s.Member(ctx, "alpha", model.PrincipalUser, "alice"); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("assignment survived cleanup after identity deletion: %v", err)
	}
}
