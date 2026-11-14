package storetest

import (
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func runPermissionPages(t *testing.T, newStore Factory) {
	t.Run("PermissionPages", func(t *testing.T) { testPermissionPages(t, newStore(t)) })
}

// testPermissionPages: PermissionStore.Page in name order whatever the
// insertion order, strictly after its position, with More only while
// permissions follow, a limit past List's cap, and the grant filter over
// users and groups, a grant of no action matching nothing.
func testPermissionPages(t *testing.T, s port.Store) {
	ctx := t.Context()
	grant := func(name string, users, groups map[string]api.Actions) model.Permission {
		return model.Permission{Name: name, Resources: map[api.ResourceType]model.Resource{
			api.ResourceArtifact: {
				Actions: model.Grants{Users: users, Groups: groups},
				Targets: map[string]model.Target{"libs-local": {Include: []string{"**"}}},
			},
		}}
	}
	r := api.ActionRead
	for _, p := range []model.Permission{
		grant("p-e", nil, map[string]api.Actions{"devs": r}),
		grant("p-d", map[string]api.Actions{"bob": 0}, nil),
		grant("p-c", map[string]api.Actions{"ann": r}, map[string]api.Actions{"ops": r}),
		grant("P-B", map[string]api.Actions{"bob": r}, nil),
		grant("p-a", nil, nil),
	} {
		storekit.Must(t, s.Permissions().Create(ctx, p))
	}
	for _, c := range []struct {
		name string
		q    port.PermissionQuery
		want string
	}{
		{"first page", port.PermissionQuery{Limit: 2}, "[P-B p-a] true"},
		{"after a name", port.PermissionQuery{After: "p-a", Limit: 2}, "[p-c p-d] true"},
		{"last page", port.PermissionQuery{After: "p-c", Limit: 2}, "[p-d p-e] false"},
		{"after a name that is not stored", port.PermissionQuery{After: "p-bb", Limit: 10}, "[p-c p-d p-e] false"},
		{"after the last", port.PermissionQuery{After: "p-e", Limit: 10}, "[] false"},
		{"every permission", port.PermissionQuery{Limit: 99999}, "[P-B p-a p-c p-d p-e] false"},
		{"a user", port.PermissionQuery{Limit: 10, Granting: true, Users: []string{"bob"}}, "[P-B] false"},
		{"users and groups", port.PermissionQuery{
			Limit: 1, Granting: true, Users: []string{"ann", "bob"}, Groups: []string{"devs"},
		}, "[P-B] true"},
		{"users and groups after", port.PermissionQuery{
			After: "P-B", Limit: 1, Granting: true, Users: []string{"ann", "bob"}, Groups: []string{"devs"},
		}, "[p-c] true"},
		{"a group", port.PermissionQuery{Limit: 10, Granting: true, Groups: []string{"ops", "devs"}},
			"[p-c p-e] false"},
		{"nobody", port.PermissionQuery{Limit: 10, Granting: true}, "[] false"},
		{"an unknown user", port.PermissionQuery{Limit: 10, Granting: true, Users: []string{"zed"}}, "[] false"},
	} {
		ps, more, err := s.Permissions().Page(ctx, c.q)
		storekit.Must(t, err)
		names := []string{}
		for _, p := range ps {
			names = append(names, p.Name)
		}
		storekit.Same(t, c.name, c.want, fmt.Sprint(names, more))
	}
	// A page holds the whole permission, as Get does.
	ps, _, err := s.Permissions().Page(ctx, port.PermissionQuery{After: "p-b", Limit: 1})
	storekit.Must(t, err)
	want, err := s.Permissions().Get(ctx, "p-c")
	storekit.Must(t, err)
	storekit.Same(t, "page document", []model.Permission{want}, ps)
}
