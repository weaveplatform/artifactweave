package authz

import (
	"context"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
)

// visibilityWorld adds to world the targets VisibleRepos turns into sets
// one by one: ANY LOCAL, ANY DISTRIBUTION, a rule whose include never
// reaches the root, one whose exclude covers it, one granting no read, and
// two naming the same key.
func visibilityWorld(t *testing.T) Sources {
	t.Helper()
	ctx := context.Background()
	db := world(t)
	grant := func(name, repo string, include, exclude []string, a accessapi.Actions) model.Permission {
		return model.Permission{Name: name, Resources: map[accessapi.ResourceType]model.Resource{
			accessapi.ResourceArtifact: {
				Actions: model.Grants{
					Users:  map[string]accessapi.Actions{"carol": a},
					Groups: map[string]accessapi.Actions{"locals": a},
				},
				Targets: map[string]model.Target{repo: target(include, exclude)},
			},
		}}
	}
	for _, p := range []model.Permission{
		grant("any-local", accessapi.TargetAnyLocal, nil, nil, r),
		grant("any-distribution", accessapi.TargetAnyDistribution, nil, nil, r),
		grant("rooted-elsewhere", "ext-local", []string{"a.txt"}, nil, r),
		grant("root-excluded", "fed-local", nil, []string{"**"}, r),
		grant("write-only", "virt", nil, nil, w),
		grant("named-twice", "jcenter", nil, nil, r),
		grant("named-again", "jcenter", []string{"org/**"}, nil, r|w),
	} {
		if err := db.Permissions().Create(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Groups().Create(ctx, model.Group{Name: "locals"}); err != nil {
		t.Fatal(err)
	}
	return Sources{Permissions: db.Permissions(), Groups: db.Groups(), AnonymousEnabled: true}
}

// TestUnitVisibleReposDescribesIsRepoVisible: for every subject, token scope
// and stored repository of the matrix, the set VisibleRepos describes holds
// exactly the repositories IsRepoVisible admits, which is what lets a
// listing filter in its store.
func TestUnitVisibleReposDescribesIsRepoVisible(t *testing.T) {
	ctx := context.Background()
	src := visibilityWorld(t)
	repos := []accessapi.Repo{
		{Key: "libs-local", Class: "local"}, {Key: "ext-local", Class: "local"},
		{Key: "fed-local", Class: "federated"}, {Key: "jcenter", Class: "remote"},
		{Key: "other-remote", Class: "remote"}, {Key: "virt", Class: "virtual"},
		{Key: "all", Class: "virtual"}, {Key: "dist", Class: "distribution"},
		{Key: "gone-local", Class: "local"}, {Key: "ANY", Class: "local"},
	}
	subjects := []accessapi.Subject{
		{Name: "bob", Groups: []string{"devs"}},
		{Name: "bob", Groups: []string{"devs", "readers"}},
		{Name: "bob", Groups: []string{"admins"}},
		{Name: "bob", Admin: true},
		{Name: "carol"},
		{Name: "dave", Groups: []string{"locals"}},
		{Name: "erin"},
		{Name: "anonymous", Anonymous: true},
	}
	scopes := []string{
		"",
		"applied-permissions/user",
		"applied-permissions/admin",
		"applied-permissions/groups:readers",
		"applied-permissions/groups:locals",
		"repo:libs-local:r",
		"repo:jcenter:r repo:ext-local:w",
		"repo:*:r",
		"applied-permissions/user repo:*:r",
		"artifact:libs-local:r",
		"artifact:*:r",
		"artifact:jcenter/org/**:r",
		"artifact:jcenter/a.txt:r",
		"artifact:libs-local:w",
		"applied-permissions/user artifact:ext-local:r artifact:fed-local:r",
		"system:info:r",
		"system:info:w",
		"applied-permissions/user system:info:r",
		"build:*:r",
	}
	visible := 0
	for _, sub := range subjects {
		for _, sc := range scopes {
			s := sub
			if sc != "" {
				s.Scope = scope(t, sc)
			}
			d, err := New(ctx, src, s)
			if err != nil {
				t.Fatal(err)
			}
			v := d.VisibleRepos()
			for _, repo := range repos {
				want := d.IsRepoVisible(repo)
				if want {
					visible++
				}
				if got := v.Has(repo); got != want {
					t.Errorf("%+v scope %q: VisibleRepos %v holds %s: %v, IsRepoVisible %v",
						sub, sc, v, repo.Key, got, want)
				}
			}
		}
	}
	// The matrix shows some repositories and hides others.
	if total := len(subjects) * len(scopes) * len(repos); visible == 0 || visible == total {
		t.Fatalf("%d of %d visible: the matrix does not tell sets apart", visible, total)
	}
}
