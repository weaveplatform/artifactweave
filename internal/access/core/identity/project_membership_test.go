package identity_test

import (
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
)

func TestUnitIdentityRecreationDoesNotRestoreProjectAssignments(t *testing.T) {
	for _, typ := range []model.PrincipalType{model.PrincipalUser, model.PrincipalGroup} {
		t.Run(string(typ), func(t *testing.T) {
			s, db := newService(t)
			ctx := t.Context()
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			_, err := s.PutUser(ctx, newUser("member"), "Member-pass1")
			must(err)
			_, err = s.PutGroup(ctx, model.Group{Name: "member"}, nil)
			must(err)
			projects := project.Service{Store: db}
			before := map[string]model.ProjectAuthority{}
			other := model.PrincipalUser
			if typ == model.PrincipalUser {
				other = model.PrincipalGroup
			}
			for _, key := range []string{"alpha", "beta", "unrelated"} {
				must(db.Projects().Create(ctx, model.Project{Key: key}))
				if key != "unrelated" {
					_, err := projects.PutMember(ctx, key, typ, "member", []string{"Viewer"})
					must(err)
				}
				if key == "alpha" {
					_, err := projects.PutMember(ctx, key, other, "member", []string{"Developer"})
					must(err)
				}
				before[key], err = db.Projects().Authority(ctx, key)
				must(err)
			}
			var user string
			var groups []string
			if typ == model.PrincipalUser {
				must(s.DeleteUser(ctx, "MEMBER"))
				_, err = s.PutUser(ctx, newUser("member"), "New-member-pass2")
				must(err)
				user = "MEMBER"
			} else {
				must(s.DeleteGroup(ctx, "member"))
				_, err = s.PutGroup(ctx, model.Group{Name: "member"}, nil)
				must(err)
				groups = []string{"member"}
			}
			for _, key := range []string{"alpha", "beta"} {
				roles, err := projects.AssignedRoles(ctx, key, user, groups)
				must(err)
				if len(roles) != 0 {
					t.Fatalf("recreated %s inherited old %s project assignments: %+v", typ, key, roles)
				}
			}
			retained, err := projects.Member(ctx, "alpha", other, "member")
			must(err)
			if !slices.Equal(retained.Roles, []string{"Developer"}) {
				t.Fatalf("deleting %s changed same-name %s assignment: %+v", typ, other, retained)
			}
			for key, initial := range before {
				after, err := db.Projects().Authority(ctx, key)
				must(err)
				if after.Incarnation != initial.Incarnation || (after.Revision == initial.Revision) != (key == "unrelated") {
					t.Fatalf("project %s authority change after %s deletion: before=%+v after=%+v", key, typ, initial, after)
				}
			}
		})
	}
}
