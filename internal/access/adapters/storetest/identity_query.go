package storetest

import (
	"context"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func testIdentityPages(t *testing.T, s port.Store) {
	ctx := t.Context()
	for _, g := range []model.Group{
		{Name: "admins", AdminPrivileges: true}, {Name: "readers"}, {Name: "ÉQUIPE_%"},
	} {
		storekit.Must(t, s.Groups().Create(ctx, g))
	}
	for _, u := range []model.User{
		{Name: "alpha", Admin: true},
		{Name: "bravo", Groups: []string{"admins", "readers"}},
		{Name: "charlie", Status: model.UserDisabled},
		{Name: "delta"},
		{Name: "ÉLODIE_%"},
	} {
		storekit.Must(t, s.Users().Create(ctx, u))
	}
	p := model.Permission{Name: "read", Resources: map[api.ResourceType]model.Resource{
		api.ResourceArtifact: {
			Actions: model.Grants{
				Users:  map[string]api.Actions{"alpha": api.ActionRead, "charlie": 0},
				Groups: map[string]api.Actions{"readers": api.ActionRead, "admins": 0},
			},
			Targets: map[string]model.Target{"repo": {}},
		},
	}}
	storekit.Must(t, s.Permissions().Create(ctx, p))
	target := &port.IdentityResource{Type: api.ResourceArtifact, Name: "repo"}
	for _, c := range []struct {
		name   string
		filter port.UserFilter
		want   []string
	}{
		{"all", port.UserFilter{}, []string{"alpha", "bravo", "charlie", "delta", "ÉLODIE_%"}},
		{
			"descending",
			port.UserFilter{IdentityFilter: port.IdentityFilter{Descending: true}},
			[]string{"ÉLODIE_%", "delta", "charlie", "bravo", "alpha"},
		},
		{"effective admins", port.UserFilter{OnlyAdmins: true}, []string{"alpha", "bravo"}},
		{"disabled", port.UserFilter{Status: model.UserDisabled}, []string{"charlie"}},
		{"default enabled", port.UserFilter{Status: model.UserEnabled}, []string{"alpha", "bravo", "delta", "ÉLODIE_%"}},
		{"Unicode", port.UserFilter{IdentityFilter: port.IdentityFilter{NameContains: "élodie"}}, []string{"ÉLODIE_%"}},
		{"literal wildcard", port.UserFilter{IdentityFilter: port.IdentityFilter{NameContains: "_%"}}, []string{"ÉLODIE_%"}},
		{"grants", port.UserFilter{IdentityFilter: port.IdentityFilter{Resource: target}}, []string{"alpha", "bravo"}},
		{"resource type", port.UserFilter{IdentityFilter: port.IdentityFilter{Resource: &port.IdentityResource{
			Type: api.ResourceBuild, Name: "repo",
		}}}, nil},
		{"no target expansion", port.UserFilter{IdentityFilter: port.IdentityFilter{Resource: &port.IdentityResource{
			Type: api.ResourceArtifact, Name: api.TargetAny,
		}}}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			page := kernel.Page{Limit: 1}
			for calls := 0; ; calls++ {
				if calls > 10 {
					t.Fatal("pagination did not terminate")
				}
				rows, next, err := s.Users().ListSummaries(ctx, c.filter, page)
				storekit.Must(t, err)
				if len(rows) > page.Limit {
					t.Fatal("page limit exceeded")
				}
				for _, row := range rows {
					got = append(got, row.Name)
				}
				if next == "" {
					break
				}
				if next != kernel.Cursor(rows[len(rows)-1].Name) {
					t.Fatal("changed the public cursor encoding")
				}
				page.After = next
			}
			if !slices.Equal(got, c.want) {
				t.Fatalf("names = %v; want %v", got, c.want)
			}
		})
	}
	for _, c := range []struct {
		filter port.IdentityFilter
		want   []string
	}{
		{port.IdentityFilter{}, []string{"admins", "readers", "ÉQUIPE_%"}},
		{port.IdentityFilter{Descending: true}, []string{"ÉQUIPE_%", "readers", "admins"}},
		{port.IdentityFilter{Resource: target}, []string{"readers"}},
		{port.IdentityFilter{NameContains: "équipe_%"}, []string{"ÉQUIPE_%"}},
	} {
		got := storekit.Pages(
			t,
			1,
			func(p kernel.Page) ([]string, string, error) { return s.Groups().ListNames(ctx, c.filter, p) },
		)
		if !slices.Equal(got, c.want) {
			t.Fatalf("groups = %v; want %v", got, c.want)
		}
	}
	// Continuation does not require the cursor row to still exist, and reads
	// current authorization rather than freezing grants into the cursor.
	storekit.Must(t, s.Users().Delete(ctx, "alpha"))
	p.Resources[api.ResourceArtifact] = model.Resource{
		Targets: map[string]model.Target{"repo": {}},
	}
	storekit.Must(t, s.Permissions().Update(ctx, p))
	rows, _, err := s.Users().
		ListSummaries(ctx, port.UserFilter{IdentityFilter: port.IdentityFilter{Resource: target}},
			kernel.Page{After: kernel.Cursor("alpha"), Limit: 1})
	storekit.Must(t, err)
	if len(rows) != 0 {
		t.Fatal("revoked grants remain visible")
	}
	rows, _, err = s.Users().
		ListSummaries(ctx, port.UserFilter{}, kernel.Page{After: kernel.Cursor("alpha"), Limit: 1})
	storekit.Must(t, err)
	if len(rows) != 1 || rows[0].Name != "bravo" {
		t.Fatalf("deleted cursor continuation: %v", rows)
	}
	for _, cursor := range []string{"%%%", kernel.Cursor("a", "b")} {
		_, _, err := s.Users().ListSummaries(ctx, port.UserFilter{}, kernel.Page{After: cursor})
		storekit.WantErr(t, err, kernel.ErrInvalidCursor)
		_, _, err = s.Groups().ListNames(ctx, port.IdentityFilter{}, kernel.Page{After: cursor})
		storekit.WantErr(t, err, kernel.ErrInvalidCursor)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, _, err = s.Users().ListSummaries(cancelled, port.UserFilter{}, kernel.Page{})
	storekit.WantErr(t, err, context.Canceled)
	_, _, err = s.Groups().ListNames(cancelled, port.IdentityFilter{}, kernel.Page{})
	storekit.WantErr(t, err, context.Canceled)
}
