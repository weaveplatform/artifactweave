package storetest

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

// testGroupNameCase holds every backend to 7.x's rule for group names
// (#1309): a name keeps its case and is compared exactly, so ops and Ops are
// two groups and OPS names neither, in the group rows, the memberships and
// the permission grants alike. A backend that folded case anywhere, through
// a collation or a lowered key, would merge them. Only the list filter
// ignores case, as a search does.
func testGroupNameCase(t *testing.T, s port.Store) {
	ctx := t.Context()
	gs := s.Groups()
	storekit.Must(t, s.Users().Create(ctx, user(1, "alice")))
	lower, title := group("ops", false, "alice"), group("Ops", false)
	storekit.Must(t, gs.Create(ctx, lower))
	storekit.Must(t, gs.Create(ctx, title))
	storekit.WantErr(t, gs.Create(ctx, group("ops", false)), kernel.ErrConflict)

	_, err := gs.Get(ctx, "OPS")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.Same(t, "Get ops", lower, storekit.Get[model.Group](t)(gs.Get(ctx, "ops")))
	storekit.Same(t, "Get Ops", title, storekit.Get[model.Group](t)(gs.Get(ctx, "Ops")))
	all := storekit.Pages(
		t,
		1,
		func(p kernel.Page) ([]model.Group, string, error) { return gs.List(ctx, p) },
	)
	storekit.Same(t, "List", []string{"Ops", "ops"}, groupNames(all))
	found, _, err := gs.ListNames(ctx, port.IdentityFilter{NameContains: "OPS"}, kernel.Page{})
	storekit.Must(t, err)
	storekit.Same(t, "ListNames ignores case", []string{"Ops", "ops"}, found)

	storekit.WantErr(t, gs.Update(ctx, group("OPS", false)), kernel.ErrNotFound)
	storekit.WantErr(t, gs.AddMembers(ctx, "OPS", "alice"), kernel.ErrNotFound)
	storekit.WantErr(t, gs.RemoveMembers(ctx, "OPS", "alice"), kernel.ErrNotFound)
	storekit.WantErr(t, gs.Delete(ctx, "OPS"), kernel.ErrNotFound)
	storekit.WantErr(t, s.Users().Create(ctx, user(2, "bob", "OPS")), kernel.ErrNotFound)
	storekit.Same(
		t,
		"alice's groups",
		[]string{"ops"},
		storekit.Get[model.User](t)(s.Users().Get(ctx, "alice")).Groups,
	)

	ps := s.Permissions()
	storekit.Must(t, ps.Create(ctx, permission(1, "ops-read", map[api.ResourceType]model.Resource{
		api.ResourceArtifact: {
			Actions: model.Grants{Groups: map[string]api.Actions{"ops": api.ActionRead}},
			Targets: map[string]model.Target{"libs-local": {Include: []string{"**"}}},
		},
	})))
	for _, c := range []struct {
		group string
		want  []string
	}{{"OPS", nil}, {"Ops", nil}, {"ops", []string{"ops-read"}}} {
		storekit.Same(t, "ForPrincipal "+c.group, c.want, permissionNames(
			storekit.Get[[]model.Permission](t)(ps.ForPrincipal(ctx, "", []string{c.group}))))
	}

	storekit.Must(t, gs.Delete(ctx, "Ops"))
	storekit.Same(t, "ops after deleting Ops", lower,
		storekit.Get[model.Group](t)(gs.Get(ctx, "ops")))
}
