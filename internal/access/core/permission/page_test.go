package permission_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/permission"
)

// TestUnitPrincipalsOfAndPage checks that walking Page over PrincipalsOf's
// principals, at every page size, visits exactly what Granting collects for
// the same filter, and that PrincipalsOf refuses unknown names with
// Granting's messages.
func TestUnitPrincipalsOfAndPage(t *testing.T) {
	ctx := context.Background()
	svc := service(seeded(t))
	for _, p := range []model.Permission{
		perm("a-to-bob", map[api.ResourceType]model.Resource{
			api.ResourceArtifact: artifact(everywhere("libs-local"), map[string]api.Actions{"bob": r}, nil),
		}),
		perm("b-to-nobody", map[api.ResourceType]model.Resource{
			api.ResourceArtifact: artifact(everywhere("libs-local"), nil, nil),
		}),
		perm("c-to-devs", map[api.ResourceType]model.Resource{
			api.ResourceArtifact: artifact(everywhere("libs-local"), nil, map[string]api.Actions{"devs": r}),
		}),
		perm("d-to-readers", map[api.ResourceType]model.Resource{
			api.ResourceBuild: artifact(map[string]model.Target{"artifactory-build-info": {Include: []string{"**"}}},
				nil, map[string]api.Actions{"readers": r}),
		}),
		perm("e-to-bob-and-readers", map[api.ResourceType]model.Resource{
			api.ResourceArtifact: artifact(everywhere("central"),
				map[string]api.Actions{"bob": r}, map[string]api.Actions{"readers": m}),
		}),
	} {
		if err := svc.Create(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	names := func(ps []model.Permission) []string {
		out := []string{}
		for _, p := range ps {
			out = append(out, p.Name)
		}
		return out
	}
	walk := func(t *testing.T, limit int, pr *permission.Principals) []string {
		t.Helper()
		out := []string{}
		after := ""
		for range 10 {
			ps, more, err := svc.Page(ctx, after, limit, pr)
			if err != nil {
				t.Fatal(err)
			}
			if len(ps) > limit || (more && len(ps) != limit) {
				t.Fatalf("a page of %d with more %v at limit %d", len(ps), more, limit)
			}
			out = append(out, names(ps)...)
			if !more {
				return out
			}
			after = ps[len(ps)-1].Name
		}
		t.Fatalf("the walk at limit %d did not end", limit)
		return nil
	}
	everyName, err := svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for limit := 1; limit <= 6; limit++ {
		if got := walk(t, limit, nil); !slices.Equal(got, names(everyName)) {
			t.Errorf("unfiltered at limit %d = %v; want %v", limit, got, names(everyName))
		}
	}
	for _, c := range []struct {
		users, groups []string
		inherited     bool
	}{
		{[]string{"bob"}, nil, false},
		{[]string{"BOB", "bob"}, nil, true},
		{nil, []string{"readers"}, false},
		{[]string{"carol"}, []string{"devs", "readers"}, true},
		{[]string{"carol"}, nil, true},
	} {
		want, wantPr, err := svc.Granting(ctx, c.users, c.groups, c.inherited)
		if err != nil {
			t.Fatal(err)
		}
		pr, err := svc.PrincipalsOf(ctx, c.users, c.groups, c.inherited)
		if err != nil || !slices.Equal(pr.Users, wantPr.Users) || !slices.Equal(pr.Groups, wantPr.Groups) {
			t.Errorf("principals of %v %v %v = %+v, %v; want %+v", c.users, c.groups, c.inherited,
				pr, err, wantPr)
		}
		for limit := 1; limit <= 6; limit++ {
			if got := walk(t, limit, &pr); !slices.Equal(got, names(want)) {
				t.Errorf("%v %v %v at limit %d = %v; want %v", c.users, c.groups, c.inherited,
					limit, got, names(want))
			}
		}
	}
	for _, c := range []struct {
		users, groups []string
	}{
		{[]string{"zed"}, nil},
		{[]string{"bob"}, []string{"ops"}},
		{[]string{"zed"}, []string{"ops"}},
	} {
		_, _, want := svc.Granting(ctx, c.users, c.groups, true)
		_, err := svc.PrincipalsOf(ctx, c.users, c.groups, true)
		if !errors.Is(err, permission.ErrNotFound) || err.Error() != want.Error() {
			t.Errorf("principals of %v %v: %v; want %v", c.users, c.groups, err, want)
		}
	}
}
