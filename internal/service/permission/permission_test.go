package permission_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/service/permission"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	r = types.ActionRead
	m = types.ActionManage
)

var clock = time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)

// seeded is a store with the users, groups and repositories the tests name.
func seeded(t *testing.T) *inmem.Store {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	for _, g := range []string{"devs", "readers"} {
		if err := db.Groups().Create(ctx, types.Group{Name: g}); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []types.User{{Name: "bob", Groups: []string{"devs"}}, {Name: "carol"}} {
		if err := db.Users().Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []repo.Config{
		{Key: "libs-local", Class: repo.Local},
		{Key: "docs-cache", Class: repo.Local},
		{Key: "central", Class: repo.Remote, URL: "https://repo.example"},
		{Key: "virt", Class: repo.Virtual},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func service(db store.Store) permission.Service {
	return permission.Service{Store: db, Now: func() time.Time { return clock }}
}

func artifact(
	targets map[string]types.Target,
	users, groups map[string]types.Actions,
) types.Resource {
	return types.Resource{
		Actions: types.Grants{Users: users, Groups: groups},
		Targets: targets,
	}
}

func everywhere(keys ...string) map[string]types.Target {
	out := map[string]types.Target{}
	for _, k := range keys {
		out[k] = types.Target{Include: []string{"**"}}
	}
	return out
}

func perm(name string, res map[types.ResourceType]types.Resource) types.Permission {
	return types.Permission{Name: name, Resources: res}
}

func TestCreateGetDelete(t *testing.T) {
	ctx := context.Background()
	svc := service(seeded(t))
	p := perm("dev", map[types.ResourceType]types.Resource{
		types.ResourceArtifact: artifact(everywhere("libs-local"),
			map[string]types.Actions{"bob": r | m}, nil),
	})
	if err := svc.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := svc.Create(ctx, p); !errors.Is(err, permission.ErrConflict) {
		t.Fatalf("second create: %v", err)
	}
	got, err := svc.Get(ctx, "dev")
	if err != nil || !got.Created.Equal(clock) ||
		got.Resources[types.ResourceArtifact].Actions.Users["bob"] != r|m {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if err := svc.Delete(ctx, "dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, "dev"); !errors.Is(err, permission.ErrNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
	if err := svc.Delete(ctx, "dev"); !errors.Is(err, permission.ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestValidate(t *testing.T) {
	ctx := context.Background()
	svc := service(seeded(t))
	res := func(rt types.ResourceType, targets map[string]types.Target) types.Permission {
		return perm("p", map[types.ResourceType]types.Resource{rt: artifact(targets, nil, nil)})
	}
	long := strings.Repeat("a", permission.MaxPatternLength+1)
	for name, c := range map[string]struct {
		p    types.Permission
		want string
	}{
		"empty name":      {perm("", nil), "not valid"},
		"padded name":     {perm(" p", nil), "not valid"},
		"slash in name":   {perm("a/b", nil), "slash"},
		"long name":       {perm(strings.Repeat("n", 65), nil), "longer than 64"},
		"unknown section": {res("widgets", nil), "Unknown resource type"},
		"unknown repo":    {res(types.ResourceArtifact, everywhere("nope")), "Repository 'nope' does not exist"},
		"virtual repo":    {res(types.ResourceArtifact, everywhere("virt")), "Virtual repository"},
		"cache of a local": {
			res(types.ResourceArtifact, everywhere("libs-local-cache")),
			"Repository 'libs-local-cache' does not exist",
		},
		"two build targets": {
			res(types.ResourceBuild, everywhere(types.BuildInfoRepository, "x")),
			"exactly one target",
		},
		"wrong build target": {res(types.ResourceBuild, everywhere("libs-local")), "must be artifactory-build-info"},
		"long pattern": {res(types.ResourceArtifact, map[string]types.Target{
			"libs-local": {Include: []string{long}},
		}), "longer than 1024"},
		"unknown user": {perm("p", map[types.ResourceType]types.Resource{
			types.ResourceArtifact: artifact(everywhere("libs-local"), map[string]types.Actions{"zed": r}, nil),
		}), "User 'zed' does not exist"},
		"unknown group": {perm("p", map[types.ResourceType]types.Resource{
			types.ResourceArtifact: artifact(everywhere("libs-local"), nil, map[string]types.Actions{"ops": r}),
		}), "Group 'ops' does not exist"},
	} {
		err := svc.Validate(ctx, c.p)
		if !errors.Is(err, permission.ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want ErrInvalid containing %q", name, err, c.want)
		}
	}
	ok := perm("ok", map[types.ResourceType]types.Resource{
		types.ResourceArtifact: artifact(everywhere(
			"libs-local",
			"docs-cache",
			"central-cache",
			types.TargetAny,
			types.TargetAnyLocal,
			types.TargetAnyRemote,
			types.TargetAnyDistribution,
		),
			map[string]types.Actions{"bob": r}, map[string]types.Actions{"devs": r}),
		types.ResourceBuild:          artifact(everywhere(types.BuildInfoRepository), nil, nil),
		types.ResourceReleaseBundle:  artifact(everywhere("release-bundles"), nil, nil),
		types.ResourceDestination:    artifact(everywhere("edge-1"), nil, nil),
		types.ResourcePipelineSource: artifact(nil, nil, nil),
	})
	if err := svc.Validate(ctx, ok); err != nil {
		t.Fatalf("a permission naming every accepted target: %v", err)
	}
}

func TestPutAndPutResource(t *testing.T) {
	ctx := context.Background()
	svc := service(seeded(t))
	full := perm("multi", map[types.ResourceType]types.Resource{
		types.ResourceArtifact: artifact(
			everywhere("libs-local"),
			map[string]types.Actions{"bob": r},
			nil,
		),
		types.ResourceBuild: artifact(
			everywhere(types.BuildInfoRepository),
			map[string]types.Actions{"bob": r},
			nil,
		),
	})
	created, err := svc.Put(ctx, full)
	if err != nil || !created {
		t.Fatalf("put = %v, %v", created, err)
	}
	// A v1-style write replaces the artifact section and keeps build.
	created, err = svc.PutResource(ctx, "multi", types.ResourceArtifact,
		artifact(everywhere("central"), map[string]types.Actions{"carol": r}, nil), true)
	if err != nil || created {
		t.Fatalf("put resource = %v, %v", created, err)
	}
	got, _ := svc.Get(ctx, "multi")
	if _, ok := got.Resources[types.ResourceBuild]; !ok ||
		got.Resources[types.ResourceArtifact].Actions.Users["carol"] != r {
		t.Fatalf("after put resource: %+v", got.Resources)
	}
	if !got.Created.Equal(clock) {
		t.Fatalf("created time lost: %v", got.Created)
	}
	// A whole replace drops sections it does not carry.
	if _, err := svc.Put(ctx, perm("multi", map[types.ResourceType]types.Resource{
		types.ResourceArtifact: artifact(everywhere("libs-local"), nil, nil),
	})); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Get(ctx, "multi"); len(got.Resources) != 1 {
		t.Fatalf("replace kept %v", got.ResourceTypes())
	}
	if _, err := svc.PutResource(ctx, "missing", types.ResourceArtifact,
		artifact(nil, nil, nil), false); !errors.Is(err, permission.ErrNotFound) {
		t.Fatalf("put resource without create: %v", err)
	}
	if created, err := svc.PutResource(ctx, "fresh", types.ResourceArtifact,
		artifact(everywhere("libs-local"), nil, nil), true); err != nil || !created {
		t.Fatalf("put resource creating: %v, %v", created, err)
	}
	if _, err := svc.Put(ctx, perm("bad", map[types.ResourceType]types.Resource{
		types.ResourceArtifact: artifact(everywhere("nope"), nil, nil),
	})); !errors.Is(err, permission.ErrInvalid) {
		t.Fatalf("invalid put: %v", err)
	}
	if _, err := svc.Put(ctx, perm("", nil)); !errors.Is(err, permission.ErrInvalid) {
		t.Fatalf("put without a name: %v", err)
	}
}

// TestWrite checks that change sees the stored permission and that an
// error from it stores nothing.
func TestWrite(t *testing.T) {
	ctx := context.Background()
	svc := service(seeded(t))
	first := perm("w", map[types.ResourceType]types.Resource{
		types.ResourceArtifact: artifact(
			everywhere("libs-local"),
			map[string]types.Actions{"bob": r},
			nil,
		),
	})
	created, err := svc.Write(
		ctx,
		"w",
		func(_ types.Permission, exists bool) (types.Permission, error) {
			if exists {
				t.Error("a new name reported as stored")
			}
			return first, nil
		},
	)
	if err != nil || !created {
		t.Fatalf("write creating = %v, %v", created, err)
	}
	refused := errors.New("refused")
	_, err = svc.Write(ctx, "w", func(old types.Permission, exists bool) (types.Permission, error) {
		if !exists || old.Resources[types.ResourceArtifact].Actions.Users["bob"] != r {
			t.Errorf("change saw %v, %+v", exists, old)
		}
		return perm("w", nil), refused
	})
	if !errors.Is(err, refused) {
		t.Fatalf("a refused write = %v", err)
	}
	if got, _ := svc.Get(ctx, "w"); len(got.Resources) != 1 {
		t.Fatalf("a refused write stored %+v", got.Resources)
	}
}

// TestCheckManagerEdit checks what a holder of MANAGE who is not an
// administrator may change: actions only, and of those only the ones
// grantable allows in each section, judged on the stored grants.
func TestCheckManagerEdit(t *testing.T) {
	const (
		w = types.ActionWrite
		d = types.ActionDelete
	)
	sections := []types.ResourceType{
		types.ResourceArtifact, types.ResourceBuild, types.ResourceReleaseBundle,
	}
	// bob holds read, write and MANAGE on artifacts, and only read on builds.
	grantable := func(g types.Grants) types.Actions {
		held := g.Users["bob"]
		if !held.Has(m) {
			return 0
		}
		return held &^ m
	}
	stored := func() types.Permission {
		return perm("p", map[types.ResourceType]types.Resource{
			types.ResourceArtifact: artifact(
				map[string]types.Target{"libs-local": {}},
				map[string]types.Actions{"bob": r | w | m, "carol": r},
				map[string]types.Actions{"ops": r | d},
			),
			types.ResourceBuild: artifact(
				everywhere(types.BuildInfoRepository),
				map[string]types.Actions{"bob": r},
				nil,
			),
			types.ResourceDestination: artifact(everywhere("edge"), nil, nil),
		})
	}
	const (
		actionsOnly = "can only edit the actions"
		heldOnly    = "only actions it holds"
	)
	for _, c := range []struct {
		name string
		edit func(p *types.Permission)
		want string
	}{
		{"no change", func(*types.Permission) {}, ""},
		{"grant a held action", func(p *types.Permission) {
			p.Resources[types.ResourceArtifact].Actions.Users["carol"] = r | w
		}, ""},
		{"revoke a held action", func(p *types.Permission) {
			delete(p.Resources[types.ResourceArtifact].Actions.Users, "carol")
		}, ""},
		{"add a group with held actions", func(p *types.Permission) {
			p.Resources[types.ResourceArtifact].Actions.Groups["devs"] = r | w
		}, ""},
		{"an empty include list is everything", func(p *types.Permission) {
			p.Resources[types.ResourceArtifact].Targets["libs-local"] = types.Target{
				Include: []string{"**"},
			}
		}, ""},
		{"a section the form does not carry", func(p *types.Permission) {
			delete(p.Resources, types.ResourceDestination)
		}, ""},
		{"grant an action it lacks", func(p *types.Permission) {
			p.Resources[types.ResourceArtifact].Actions.Users["carol"] = r | d
		}, heldOnly},
		{"grant MANAGE", func(p *types.Permission) {
			p.Resources[types.ResourceArtifact].Actions.Users["carol"] = r | m
		}, heldOnly},
		{"revoke its own MANAGE", func(p *types.Permission) {
			p.Resources[types.ResourceArtifact].Actions.Users["bob"] = r | w
		}, heldOnly},
		{"revoke an action it lacks", func(p *types.Permission) {
			p.Resources[types.ResourceArtifact].Actions.Groups["ops"] = r
		}, heldOnly},
		{"remove a principal holding an action it lacks", func(p *types.Permission) {
			delete(p.Resources[types.ResourceArtifact].Actions.Groups, "ops")
		}, heldOnly},
		{"grant in a section it does not manage", func(p *types.Permission) {
			p.Resources[types.ResourceBuild].Actions.Users["carol"] = r
		}, heldOnly},
		{"change a pattern", func(p *types.Permission) {
			p.Resources[types.ResourceArtifact].Targets["libs-local"] = types.Target{
				Exclude: []string{"secret/**"},
			}
		}, actionsOnly},
		{"add a target", func(p *types.Permission) {
			p.Resources[types.ResourceArtifact].Targets["central"] = types.Target{}
		}, actionsOnly},
		{"drop a section", func(p *types.Permission) {
			delete(p.Resources, types.ResourceBuild)
		}, actionsOnly},
		{"add a section", func(p *types.Permission) {
			p.Resources[types.ResourceReleaseBundle] = artifact(nil, nil, nil)
		}, actionsOnly},
	} {
		p := stored()
		c.edit(&p)
		err := permission.CheckManagerEdit(stored(), p, sections, grantable)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.want != "" && (!errors.Is(err, permission.ErrForbidden) ||
			!strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

func TestMergeAndDeleteResource(t *testing.T) {
	ctx := context.Background()
	svc := service(seeded(t))
	if err := svc.Create(ctx, perm("m", map[types.ResourceType]types.Resource{
		types.ResourceArtifact: artifact(everywhere("libs-local"),
			map[string]types.Actions{"bob": r}, map[string]types.Actions{"devs": r}),
	})); err != nil {
		t.Fatal(err)
	}
	merged, err := svc.MergeResource(ctx, "m", types.ResourceArtifact, artifact(
		everywhere("central"), map[string]types.Actions{"bob": r | m, "carol": r}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if merged.Actions.Users["bob"] != r|m || merged.Actions.Users["carol"] != r ||
		merged.Actions.Groups["devs"] != r || len(merged.Targets) != 2 {
		t.Fatalf("merged = %+v", merged)
	}
	// A section with no groups stays without them, then gains some.
	if err := svc.Create(ctx, perm("n", map[types.ResourceType]types.Resource{
		types.ResourceArtifact: artifact(
			everywhere("libs-local"),
			map[string]types.Actions{"bob": r},
			nil,
		),
	})); err != nil {
		t.Fatal(err)
	}
	if merged, err = svc.MergeResource(
		ctx,
		"n",
		types.ResourceArtifact,
		artifact(nil, nil, nil),
	); err != nil ||
		len(merged.Actions.Groups) != 0 ||
		merged.Actions.Users["bob"] != r {
		t.Fatalf("empty patch = %+v, %v", merged, err)
	}
	if merged, err = svc.MergeResource(
		ctx,
		"n",
		types.ResourceArtifact,
		artifact(
			nil,
			nil,
			map[string]types.Actions{"devs": r},
		),
	); err != nil || merged.Actions.Groups["devs"] != r {
		t.Fatalf("groups patch = %+v, %v", merged, err)
	}
	if _, err := svc.MergeResource(
		ctx,
		"m",
		types.ResourceBuild,
		artifact(nil, nil, nil),
	); !errors.Is(
		err,
		permission.ErrNotFound,
	) {
		t.Fatalf("merge into a missing section: %v", err)
	}
	if _, err := svc.MergeResource(
		ctx,
		"none",
		types.ResourceArtifact,
		artifact(nil, nil, nil),
	); !errors.Is(
		err,
		permission.ErrNotFound,
	) {
		t.Fatalf("merge into a missing permission: %v", err)
	}
	if err := svc.DeleteResource(ctx, "m", types.ResourceArtifact); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx, "m")
	if err != nil || len(got.Resources) != 0 {
		t.Fatalf("permission after deleting its only section: %+v, %v", got, err)
	}
	if err := svc.DeleteResource(
		ctx,
		"m",
		types.ResourceArtifact,
	); !errors.Is(
		err,
		permission.ErrNotFound,
	) {
		t.Fatalf("delete a missing section: %v", err)
	}
	if err := svc.DeleteResource(
		ctx,
		"none",
		types.ResourceArtifact,
	); !errors.Is(
		err,
		permission.ErrNotFound,
	) {
		t.Fatalf("delete a section of a missing permission: %v", err)
	}
}

func TestGrantingAndList(t *testing.T) {
	ctx := context.Background()
	svc := service(seeded(t))
	for _, p := range []types.Permission{
		perm("to-bob", map[types.ResourceType]types.Resource{
			types.ResourceArtifact: artifact(everywhere("libs-local"), map[string]types.Actions{"bob": r}, nil),
		}),
		perm("to-devs", map[types.ResourceType]types.Resource{
			types.ResourceArtifact: artifact(everywhere("libs-local"), nil, map[string]types.Actions{"devs": r}),
		}),
		perm("to-readers", map[types.ResourceType]types.Resource{
			types.ResourceArtifact: artifact(everywhere("libs-local"), nil, map[string]types.Actions{"readers": r}),
		}),
	} {
		if err := svc.Create(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	names := func(ps []types.Permission) []string {
		var out []string
		for _, p := range ps {
			out = append(out, p.Name)
		}
		return out
	}
	for _, c := range []struct {
		users, groups []string
		inherited     bool
		want          []string
	}{
		{[]string{"bob"}, nil, false, []string{"to-bob"}},
		{[]string{"bob"}, nil, true, []string{"to-bob", "to-devs"}},
		{nil, []string{"readers"}, false, []string{"to-readers"}},
		{[]string{"carol"}, []string{"devs", "readers"}, true, []string{"to-devs", "to-readers"}},
	} {
		got, _, err := svc.Granting(ctx, c.users, c.groups, c.inherited)
		if err != nil || !slices.Equal(names(got), c.want) {
			t.Errorf("granting %v %v %v = %v, %v; want %v", c.users, c.groups, c.inherited,
				names(got), err, c.want)
		}
	}
	if _, _, err := svc.Granting(
		ctx,
		[]string{"zed"},
		nil,
		true,
	); !errors.Is(
		err,
		permission.ErrNotFound,
	) {
		t.Fatalf("an unknown user: %v", err)
	}
	if _, _, err := svc.Granting(
		ctx,
		nil,
		[]string{"ops"},
		false,
	); !errors.Is(
		err,
		permission.ErrNotFound,
	) {
		t.Fatalf("an unknown group: %v", err)
	}
	defer permission.SetListPage(2)()
	all, err := svc.List(ctx)
	if err != nil || !slices.Equal(names(all), []string{"to-bob", "to-devs", "to-readers"}) {
		t.Fatalf("list across pages = %v, %v", names(all), err)
	}
}

func TestValidResourceType(t *testing.T) {
	for _, rt := range []types.ResourceType{"artifact", "build", "release_bundle", "destination", "pipeline_source"} {
		if !permission.ValidResourceType(rt) {
			t.Errorf("%s refused", rt)
		}
	}
	if permission.ValidResourceType("widget") {
		t.Error("widget accepted")
	}
	if err := permission.ValidName(fmt.Sprintf("%064d", 0)); err != nil {
		t.Errorf("a 64-character name: %v", err)
	}
}

func TestDefaultClock(t *testing.T) {
	ctx := context.Background()
	svc := permission.Service{Store: seeded(t)}
	if err := svc.Create(ctx, perm("now", nil)); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Get(ctx, "now"); got.Created.IsZero() {
		t.Fatal("no creation time")
	}
}

// TestNarrow keeps the grants to the principals a listing asked about: the
// user, and its groups when inherited grants count.
func TestNarrow(t *testing.T) {
	ctx := context.Background()
	svc := service(seeded(t))
	if err := svc.Create(ctx, perm("shared", map[types.ResourceType]types.Resource{
		types.ResourceArtifact: artifact(everywhere("libs-local"),
			map[string]types.Actions{"bob": r, "carol": r | m},
			map[string]types.Actions{"devs": r, "readers": r}),
	})); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		inherited bool
		want      types.Grants
	}{
		{true, types.Grants{
			Users:  map[string]types.Actions{"bob": r},
			Groups: map[string]types.Actions{"devs": r},
		}},
		{false, types.Grants{Users: map[string]types.Actions{"bob": r}}},
	} {
		ps, pr, err := svc.Granting(ctx, []string{"bob"}, nil, c.inherited)
		if err != nil || len(ps) != 1 {
			t.Fatalf("granting = %v, %v", ps, err)
		}
		got := pr.Narrow(ps[0])
		if a := got.Resources[types.ResourceArtifact]; !reflect.DeepEqual(a.Actions, c.want) ||
			len(a.Targets) != 1 {
			t.Errorf("inherited %v: narrowed to %+v, want %+v", c.inherited, a, c.want)
		}
	}
	if got := (permission.Principals{}).Narrow(perm("bare", nil)); len(got.Resources) != 0 {
		t.Errorf("narrowing a permission without sections = %+v", got)
	}
}
