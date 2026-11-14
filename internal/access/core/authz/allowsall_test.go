package authz

import (
	"context"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/adapters/inmem"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// TestUnitAllowsAll covers the whole-folder decision a list's count relies
// on: a permission that covers the folder whole, and a token scope that
// narrows it no further than the folder, give it; an include that names
// paths inside the folder, an exclude that may match below it, a grant that
// covers only part, or a scope entry that names paths inside it, do not.
func TestUnitAllowsAll(t *testing.T) {
	ctx := context.Background()
	db := world(t)
	bob := accessapi.Subject{Name: "bob", Groups: []string{"devs"}}
	type all struct {
		rt     accessapi.ResourceType
		repo   accessapi.Repo
		folder string
		want   accessapi.Actions
		ok     bool
	}
	art := func(repo accessapi.Repo, folder string, want accessapi.Actions, ok bool) all {
		return all{accessapi.ResourceArtifact, repo, folder, want, ok}
	}
	for _, c := range []struct {
		name      string
		subject   accessapi.Subject
		anonymous bool
		checks    []all
	}{
		{"admin user", accessapi.Subject{Name: "root", Admin: true}, false, []all{
			art(ext, "", r|w|d|m, true),
			{accessapi.ResourceBuild, builds, "", r, true},
		}},
		{"an include inside the repository, and its exclude", bob, false, []all{
			art(libs, "", r, false),
			art(libs, "org", r, false),
			art(libs, "org/acme", r, false),
			art(libs, "org/acme/lib", r|w, true),
			art(libs, "org/acme/lib", d, false),
			art(libs, "org/acme/secret", r, false),
			art(libs, "org/other", r, false),
			art(ext, "org/acme/lib", r, false),
		}},
		{"a pattern that names files", bob, false, []all{
			art(libs, "org/acme/lib", r|w|d, false),
			art(libs, "", d, false),
		}},
		{"a target with no patterns covers every class", accessapi.Subject{Name: "dave", Groups: []string{"readers"}}, false, []all{
			art(libs, "", r, true), art(virt, "", r, true), art(jcenter, "a/b", r, true),
			art(libs, "", w, false),
		}},
		{"anonymous off holds nothing", accessapi.Subject{Name: "anonymous", Anonymous: true}, false, []all{
			art(jcenter, "", r, false),
		}},
		{"anonymous on: ** on any remote and its cache", accessapi.Subject{Name: "anonymous", Anonymous: true}, true, []all{
			art(jcenter, "", r|w, true), art(cache, "", r, true), art(libs, "", r, false),
		}},
		{"want of nothing is never allowed", accessapi.Subject{Name: "root", Admin: true}, false, []all{
			art(ext, "", 0, false),
		}},
		{"build resource below a folder", bob, false, []all{
			{accessapi.ResourceBuild, builds, "", r, false},
			{accessapi.ResourceBuild, builds, "app", r, true},
			{accessapi.ResourceReleaseBundle, builds, "app", r, false},
		}},
		{"an artifact scope narrows to its path", accessapi.Subject{Name: "bob", Groups: []string{"devs"}, Scope: scope(t, "artifact:libs-local/org/acme/**:r")}, false, []all{
			art(libs, "org/acme/lib", r, true),
			art(libs, "org/acme/lib", w, false),
			art(libs, "", r, false),
			{accessapi.ResourceBuild, builds, "app", r, false},
		}},
		{"an artifact scope without a path", accessapi.Subject{Name: "dave", Groups: []string{"readers"}, Scope: scope(t, "artifact:libs-local:r")}, false, []all{
			art(libs, "", r, true), art(ext, "", r, false),
		}},
		{"an artifact scope's path that names files", accessapi.Subject{Name: "dave", Groups: []string{"readers"}, Scope: scope(t, "artifact:*/**/*.tgz:r")}, false, []all{
			art(libs, "", r, false),
		}},
		{"a build scope narrows builds", accessapi.Subject{Name: "bob", Groups: []string{"devs"}, Scope: scope(t, "build:"+accessapi.BuildInfoRepository+"/app/**:r")}, false, []all{
			{accessapi.ResourceBuild, builds, "app", r, true},
			{accessapi.ResourceBuild, builds, "", r, false},
		}},
		{"a repo scope reaches no artifact", accessapi.Subject{Name: "dave", Groups: []string{"readers"}, Scope: scope(t, "repo:libs-local:r")}, false, []all{
			art(libs, "", r, false),
		}},
		{"an admin's resource-only token keeps its repository", accessapi.Subject{Name: "root", Admin: true, Scope: scope(t, "artifact:ext-local:r")}, false, []all{
			art(ext, "", r, true), art(ext, "", w, false), art(libs, "", r, false),
		}},
		{"admin scope", accessapi.Subject{Name: "ghost", Scope: scope(t, "applied-permissions/admin")}, false, []all{
			art(ext, "", r|w|d|m, true),
		}},
	} {
		dec, err := New(ctx, Sources{
			Permissions: db.Permissions(), Groups: db.Groups(), AnonymousEnabled: c.anonymous,
		}, c.subject)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		for _, k := range c.checks {
			if got := dec.AllowsAll(k.rt, k.repo, k.folder, k.want); got != k.ok {
				t.Errorf("%s: AllowsAll(%s, %s, %q, %v) = %v, want %v",
					c.name, k.rt, k.repo.Key, k.folder, k.want.AccessNames(), got, k.ok)
			}
		}
	}
	if (&Decider{}).AllowsAll(accessapi.ResourceArtifact, libs, "", r) {
		t.Error("the zero Decider allows a whole repository")
	}
}

// TestUnitAllowsAllImpliesAllows checks over random permissions, scopes and
// paths that AllowsAll never claims a path below the folder that Allows
// refuses, which is what a list's count in the store relies on. A seeded
// source keeps it repeatable.
func TestUnitAllowsAllImpliesAllows(t *testing.T) {
	ctx := context.Background()
	rnd := rand.New(rand.NewPCG(1425, 1426))
	segments := []string{"a", "b", "ab", "*", "a*", "?", "**", ""}
	pattern := func() string {
		n := 1 + rnd.IntN(3)
		parts := make([]string, n)
		for i := range parts {
			parts[i] = segments[rnd.IntN(len(segments))]
		}
		p := strings.Join(parts, "/")
		if rnd.IntN(5) == 0 {
			p += "/"
		}
		return p
	}
	patterns := func(max int) []string {
		out := make([]string, rnd.IntN(max+1))
		for i := range out {
			out[i] = pattern()
		}
		return out
	}
	names := []string{"a", "b", "ab", "abc", ""}
	path := func(min int) string {
		n := min + rnd.IntN(3)
		parts := make([]string, n)
		for i := range parts {
			parts[i] = names[rnd.IntN(len(names)-1)]
		}
		return strings.Join(parts, "/")
	}
	scopes := []string{"", "artifact:libs-local:r", "applied-permissions/user", "artifact:*:r,w"}
	checked := 0
	for round := range 400 {
		db := inmem.New(memtx.New())
		if err := db.Groups().Create(ctx, model.Group{Name: "g"}); err != nil {
			t.Fatal(err)
		}
		for i := range 1 + rnd.IntN(3) {
			tg := target(patterns(2), patterns(1))
			if err := db.Permissions().Create(ctx, model.Permission{
				Name: "p" + string(rune('a'+i)),
				Resources: map[accessapi.ResourceType]model.Resource{
					accessapi.ResourceArtifact: {
						Actions: model.Grants{
							Groups: map[string]accessapi.Actions{
								"g": r | accessapi.Actions(rnd.IntN(2))*w,
							},
						},
						Targets: map[string]model.Target{"libs-local": tg},
					},
				},
			}); err != nil {
				t.Fatal(err)
			}
		}
		sub := accessapi.Subject{Name: "u", Groups: []string{"g"}}
		if s := scopes[rnd.IntN(len(scopes))]; s != "" {
			sub.Scope = scope(t, s)
		} else if rnd.IntN(4) == 0 {
			sub.Scope = scope(t, "artifact:libs-local/"+pattern()+":r")
		}
		dec, err := New(ctx, Sources{Permissions: db.Permissions(), Groups: db.Groups()}, sub)
		if err != nil {
			t.Fatal(err)
		}
		for range 20 {
			folder := path(0)
			want := r | accessapi.Actions(rnd.IntN(2))*w
			if !dec.AllowsAll(accessapi.ResourceArtifact, libs, folder, want) {
				continue
			}
			for range 30 {
				p := strings.Trim(folder+"/"+path(1), "/")
				if !dec.Allows(accessapi.ResourceArtifact, libs, p, want) ||
					!dec.AllowsFolder(accessapi.ResourceArtifact, libs, p, want) {
					t.Fatalf("round %d: AllowsAll(%q, %v) holds but %q is refused (scope %v)",
						round, folder, want.AccessNames(), p, sub.Scope)
				}
				checked++
			}
		}
	}
	if checked < 1000 {
		t.Fatalf("only %d paths checked; the generator rarely covers a folder", checked)
	}
}
