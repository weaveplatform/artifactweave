package storetest

import (
	"context"
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

// RunProjectRolePages proves exact role matching, distinct projects and stable
// name/key seeks in either direction, including imported names with separators.
func RunProjectRolePages(t *testing.T, newStore Factory) {
	t.Helper()
	db, ctx := newStore(t), t.Context()
	ps := db.Projects()
	const role = "role\x1fquoted'_%角色"
	projects := []model.Project{
		{Key: "alpha", DisplayName: "A"},
		{Key: "bravo", DisplayName: "A"},
		{Key: "charlie", DisplayName: "A\x1fafter"},
		{Key: "delta", DisplayName: "é"},
		{Key: "other", DisplayName: "B"},
		{Key: "unsupported", DisplayName: "C"},
		{Key: "empty", DisplayName: "D"},
	}
	for _, p := range projects {
		storekit.Must(t, ps.Create(ctx, p))
	}
	for _, key := range []string{"alpha", "bravo", "charlie", "delta"} {
		storekit.Must(t, ps.PutMember(ctx, model.ProjectMember{ProjectKey: key, Type: model.PrincipalUser, Name: "alice", Roles: []string{role}}))
	}
	storekit.Must(t, ps.PutMember(ctx, model.ProjectMember{ProjectKey: "alpha", Type: model.PrincipalGroup, Name: "team", Roles: []string{role}}))
	storekit.Must(t, ps.PutMember(ctx, model.ProjectMember{ProjectKey: "other", Type: model.PrincipalUser, Name: "alice", Roles: []string{"prefix" + role}}))
	storekit.Must(t, ps.PutMember(ctx, model.ProjectMember{ProjectKey: "unsupported", Type: "robot", Name: "ignored", Roles: []string{role}}))
	storekit.Must(t, ps.PutMember(ctx, model.ProjectMember{ProjectKey: "empty", Type: model.PrincipalUser, Name: "empty"}))
	for _, descending := range []bool{false, true} {
		q := port.ProjectRoleQuery{RoleName: role, Descending: descending, Limit: 1}
		var keys []string
		for i := 0; ; i++ {
			if i > len(projects) {
				t.Fatal("project cursor failed to advance")
			}
			rows, more, err := ps.RoleProjects(ctx, q)
			storekit.Must(t, err)
			if len(rows) != 1 {
				t.Fatalf("expected one distinct project, got %d", len(rows))
			}
			keys = append(keys, rows[0].Key)
			q.AfterName, q.AfterKey = rows[0].DisplayName, rows[0].Key
			if !more {
				break
			}
		}
		want := []string{"alpha", "bravo", "charlie", "delta"}
		if descending {
			slices.Reverse(want)
		}
		storekit.Same(t, "distinct bytewise project order", want, keys)
	}
	storekit.Must(t, ps.Delete(ctx, "alpha"))
	storekit.Must(t, ps.DeleteMember(ctx, "bravo", model.PrincipalUser, "alice"))
	rows, more, err := ps.RoleProjects(ctx, port.ProjectRoleQuery{RoleName: role, AfterName: "A", AfterKey: "alpha", Limit: 1})
	storekit.Must(t, err)
	if len(rows) != 1 || rows[0].Key != "charlie" || !more {
		t.Fatalf("deleted cursor row/revoked membership prevented a strict seek: %+v, more=%t", rows, more)
	}
	rows, more, err = ps.RoleProjects(ctx, port.ProjectRoleQuery{RoleName: role, Limit: math.MaxInt})
	storekit.Must(t, err)
	if len(rows) != 2 || more {
		t.Fatal("large positive page size overflowed or allocated phantom rows")
	}
	if _, _, err := ps.RoleProjects(ctx, port.ProjectRoleQuery{RoleName: role}); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatalf("zero store page limit was accepted: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := ps.RoleProjects(cancelled, port.ProjectRoleQuery{RoleName: role, Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled role page returned %v", err)
	}
}
