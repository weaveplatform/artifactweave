package storetest

import (
	"context"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func testIdentityPages(t *testing.T, s store.Store) {
	ctx := t.Context()
	for _, g := range []types.Group{
		{Name: "admins", AdminPrivileges: true}, {Name: "readers"}, {Name: "ÉQUIPE_%"},
	} {
		must(t, s.Groups().Create(ctx, g))
	}
	for _, u := range []types.User{
		{Name: "alpha", Admin: true},
		{Name: "bravo", Groups: []string{"admins", "readers"}},
		{Name: "charlie", Status: types.UserDisabled},
		{Name: "delta"},
		{Name: "ÉLODIE_%"},
	} {
		must(t, s.Users().Create(ctx, u))
	}
	p := types.Permission{Name: "read", Resources: map[types.ResourceType]types.Resource{
		types.ResourceArtifact: {
			Actions: types.Grants{
				Users:  map[string]types.Actions{"alpha": types.ActionRead, "charlie": 0},
				Groups: map[string]types.Actions{"readers": types.ActionRead, "admins": 0},
			},
			Targets: map[string]types.Target{"repo": {}},
		},
	}}
	must(t, s.Permissions().Create(ctx, p))
	target := &store.IdentityResource{Type: types.ResourceArtifact, Name: "repo"}
	for _, c := range []struct {
		name   string
		filter store.UserFilter
		want   []string
	}{
		{"all", store.UserFilter{}, []string{"alpha", "bravo", "charlie", "delta", "ÉLODIE_%"}},
		{
			"descending",
			store.UserFilter{IdentityFilter: store.IdentityFilter{Descending: true}},
			[]string{"ÉLODIE_%", "delta", "charlie", "bravo", "alpha"},
		},
		{"effective admins", store.UserFilter{OnlyAdmins: true}, []string{"alpha", "bravo"}},
		{"disabled", store.UserFilter{Status: types.UserDisabled}, []string{"charlie"}},
		{"default enabled", store.UserFilter{Status: types.UserEnabled}, []string{"alpha", "bravo", "delta", "ÉLODIE_%"}},
		{"Unicode", store.UserFilter{IdentityFilter: store.IdentityFilter{NameContains: "élodie"}}, []string{"ÉLODIE_%"}},
		{"literal wildcard", store.UserFilter{IdentityFilter: store.IdentityFilter{NameContains: "_%"}}, []string{"ÉLODIE_%"}},
		{"grants", store.UserFilter{IdentityFilter: store.IdentityFilter{Resource: target}}, []string{"alpha", "bravo"}},
		{"resource type", store.UserFilter{IdentityFilter: store.IdentityFilter{Resource: &store.IdentityResource{
			Type: types.ResourceBuild, Name: "repo",
		}}}, nil},
		{"no target expansion", store.UserFilter{IdentityFilter: store.IdentityFilter{Resource: &store.IdentityResource{
			Type: types.ResourceArtifact, Name: types.TargetAny,
		}}}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			page := store.Page{Limit: 1}
			for calls := 0; ; calls++ {
				if calls > 10 {
					t.Fatal("pagination did not terminate")
				}
				rows, next, err := s.Users().ListSummaries(ctx, c.filter, page)
				must(t, err)
				if len(rows) > page.Limit {
					t.Fatal("page limit exceeded")
				}
				for _, row := range rows {
					got = append(got, row.Name)
				}
				if next == "" {
					break
				}
				if next != store.Cursor(rows[len(rows)-1].Name) {
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
		filter store.IdentityFilter
		want   []string
	}{
		{store.IdentityFilter{}, []string{"admins", "readers", "ÉQUIPE_%"}},
		{store.IdentityFilter{Descending: true}, []string{"ÉQUIPE_%", "readers", "admins"}},
		{store.IdentityFilter{Resource: target}, []string{"readers"}},
		{store.IdentityFilter{NameContains: "équipe_%"}, []string{"ÉQUIPE_%"}},
	} {
		got := pages(
			t,
			1,
			func(p store.Page) ([]string, string, error) { return s.Groups().ListNames(ctx, c.filter, p) },
		)
		if !slices.Equal(got, c.want) {
			t.Fatalf("groups = %v; want %v", got, c.want)
		}
	}
	// Continuation does not require the cursor row to still exist, and reads
	// current authorization rather than freezing grants into the cursor.
	must(t, s.Users().Delete(ctx, "alpha"))
	p.Resources[types.ResourceArtifact] = types.Resource{
		Targets: map[string]types.Target{"repo": {}},
	}
	must(t, s.Permissions().Update(ctx, p))
	rows, _, err := s.Users().
		ListSummaries(ctx, store.UserFilter{IdentityFilter: store.IdentityFilter{Resource: target}},
			store.Page{After: store.Cursor("alpha"), Limit: 1})
	must(t, err)
	if len(rows) != 0 {
		t.Fatal("revoked grants remain visible")
	}
	rows, _, err = s.Users().
		ListSummaries(ctx, store.UserFilter{}, store.Page{After: store.Cursor("alpha"), Limit: 1})
	must(t, err)
	if len(rows) != 1 || rows[0].Name != "bravo" {
		t.Fatalf("deleted cursor continuation: %v", rows)
	}
	for _, cursor := range []string{"%%%", store.Cursor("a", "b")} {
		_, _, err := s.Users().ListSummaries(ctx, store.UserFilter{}, store.Page{After: cursor})
		wantErr(t, err, store.ErrInvalidCursor)
		_, _, err = s.Groups().ListNames(ctx, store.IdentityFilter{}, store.Page{After: cursor})
		wantErr(t, err, store.ErrInvalidCursor)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, _, err = s.Users().ListSummaries(cancelled, store.UserFilter{}, store.Page{})
	wantErr(t, err, context.Canceled)
	_, _, err = s.Groups().ListNames(cancelled, store.IdentityFilter{}, store.Page{})
	wantErr(t, err, context.Canceled)
}
