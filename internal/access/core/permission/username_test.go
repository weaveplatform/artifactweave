package permission_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/permission"
)

// TestUnitUserGrantsFold: a grant names its user in any case and is stored
// under the user's stored name (#1432), so it reaches the user and a later
// write in another case replaces it rather than joining it. An unknown user
// is named as the document spells it.
func TestUnitUserGrantsFold(t *testing.T) {
	ctx := context.Background()
	svc := service(seeded(t))
	if err := svc.Create(ctx, perm("p", map[api.ResourceType]model.Resource{
		api.ResourceArtifact: artifact(everywhere("libs-local"),
			map[string]api.Actions{"BOB": r, "bob": m}, nil),
	})); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	if users := got.Resources[api.ResourceArtifact].Actions.Users; !maps.Equal(users,
		map[string]api.Actions{"bob": r | m}) {
		t.Fatalf("stored grants %v", users)
	}
	ps, pr, err := svc.Granting(ctx, []string{"BOB", "Bob"}, nil, false)
	if err != nil || len(ps) != 1 || !slices.Equal(pr.Users, []string{"bob"}) {
		t.Fatalf("granting BOB = %v, %+v, %v", ps, pr, err)
	}
	merged, err := svc.MergeResource(ctx, "p", api.ResourceArtifact, artifact(nil,
		map[string]api.Actions{"Bob": r}, nil))
	if err != nil || !maps.Equal(merged.Actions.Users, map[string]api.Actions{"bob": r}) {
		t.Fatalf("merged %+v, %v", merged.Actions.Users, err)
	}
	if _, err := svc.Put(ctx, perm("p", map[api.ResourceType]model.Resource{
		api.ResourceArtifact: artifact(everywhere("libs-local"),
			map[string]api.Actions{"CAROL": r}, nil),
	})); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Get(ctx, "p"); !maps.Equal(got.Resources[api.ResourceArtifact].Actions.Users,
		map[string]api.Actions{"carol": r}) {
		t.Fatalf("replaced grants %v", got.Resources[api.ResourceArtifact].Actions.Users)
	}
	err = svc.Validate(ctx, perm("q", map[api.ResourceType]model.Resource{
		api.ResourceArtifact: artifact(everywhere("libs-local"),
			map[string]api.Actions{"Zed": r}, nil),
	}))
	if !errors.Is(err, permission.ErrInvalid) || !strings.Contains(err.Error(), "'Zed'") {
		t.Fatalf("an unknown user: %v", err)
	}
}

// TestUnitManagerOwnEntryFolds: a MANAGE holder cannot raise its own entry
// by adding a second spelling of its name. bob may grant write through its
// group, and BOB is bob, so the write is to its own entry and refused; the
// writes would otherwise fold BOB's write into bob's grant.
func TestUnitManagerOwnEntryFolds(t *testing.T) {
	const w = api.ActionWrite
	section := func(users map[string]api.Actions) model.Permission {
		return perm("p", map[api.ResourceType]model.Resource{
			api.ResourceArtifact: artifact(everywhere("libs-local"), users,
				map[string]api.Actions{"leads": w | m}),
		})
	}
	err := permission.CheckManagerEdit(section(map[string]api.Actions{"bob": r | m}),
		section(map[string]api.Actions{"bob": r | m, "BOB": w}),
		[]api.ResourceType{api.ResourceArtifact}, manager{})
	if !errors.Is(err, permission.ErrForbidden) ||
		!strings.Contains(err.Error(), "cannot change its own actions") {
		t.Fatalf("its own entry spelled BOB: %v", err)
	}
}
