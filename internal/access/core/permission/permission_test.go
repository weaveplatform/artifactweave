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

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/permission"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

const (
	r = api.ActionRead
	m = api.ActionManage
)

var clock = time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)

// seeded is a store with the users, groups and repositories the tests name.
func seeded(t *testing.T) *inmem.Store {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	for _, g := range []string{"devs", "readers"} {
		if err := db.Groups().Create(ctx, model.Group{Name: g}); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []model.User{{Name: "bob", Groups: []string{"devs"}}, {Name: "carol"}} {
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

func service(db *inmem.Store) permission.Service {
	return permission.Service{
		Store:        db,
		Repositories: repositories{db: db},
		Now:          func() time.Time { return clock },
	}
}

func artifact(
	targets map[string]model.Target,
	users, groups map[string]api.Actions,
) model.Resource {
	return model.Resource{
		Actions: model.Grants{Users: users, Groups: groups},
		Targets: targets,
	}
}

func everywhere(keys ...string) map[string]model.Target {
	out := map[string]model.Target{}
	for _, k := range keys {
		out[k] = model.Target{Include: []string{"**"}}
	}
	return out
}

func perm(name string, res map[api.ResourceType]model.Resource) model.Permission {
	return model.Permission{Name: name, Resources: res}
}

func TestUnitCreateGetDelete(t *testing.T) {
	ctx := context.Background()
	svc := service(seeded(t))
	p := perm("dev", map[api.ResourceType]model.Resource{
		api.ResourceArtifact: artifact(everywhere("libs-local"),
			map[string]api.Actions{"bob": r | m}, nil),
	})
	if err := svc.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := svc.Create(ctx, p); !errors.Is(err, permission.ErrConflict) {
		t.Fatalf("second create: %v", err)
	}
	got, err := svc.Get(ctx, "dev")
	if err != nil || !got.Created.Equal(clock) ||
		got.Resources[api.ResourceArtifact].Actions.Users["bob"] != r|m {
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

func TestUnitValidate(t *testing.T) {
	ctx := context.Background()
	svc := service(seeded(t))
	res := func(rt api.ResourceType, targets map[string]model.Target) model.Permission {
		return perm("p", map[api.ResourceType]model.Resource{rt: artifact(targets, nil, nil)})
	}
	long := strings.Repeat("a", permission.MaxPatternLength+1)
	for name, c := range map[string]struct {
		p        model.Permission
		expected string
	}{
		"empty name":      {perm("", nil), "not valid"},
		"padded name":     {perm(" p", nil), "not valid"},
		"slash in name":   {perm("a/b", nil), "slash"},
		"long name":       {perm(strings.Repeat("n", 65), nil), "longer than 64"},
		"unknown section": {res("widgets", nil), "Unknown resource type"},
		"unknown repo":    {res(api.ResourceArtifact, everywhere("nope")), "Repository 'nope' does not exist"},
		"virtual repo":    {res(api.ResourceArtifact, everywhere("virt")), "Virtual repository"},
		"cache of a local": {
			res(api.ResourceArtifact, everywhere("libs-local-cache")),
			"Repository 'libs-local-cache' does not exist",
		},
		"two build targets": {
			res(api.ResourceBuild, everywhere(api.BuildInfoRepository, "x")),
			"exactly one target",
		},
		"wrong build target": {res(api.ResourceBuild, everywhere("libs-local")), "must be artifactory-build-info"},
		"long pattern": {res(api.ResourceArtifact, map[string]model.Target{
			"libs-local": {Include: []string{long}},
		}), "longer than 1024"},
		"unknown user": {perm("p", map[api.ResourceType]model.Resource{
			api.ResourceArtifact: artifact(everywhere("libs-local"), map[string]api.Actions{"zed": r}, nil),
		}), "User 'zed' does not exist"},
		"unknown group": {perm("p", map[api.ResourceType]model.Resource{
			api.ResourceArtifact: artifact(everywhere("libs-local"), nil, map[string]api.Actions{"ops": r}),
		}), "Group 'ops' does not exist"},
	} {
		err := svc.Validate(ctx, c.p)
		if !errors.Is(err, permission.ErrInvalid) || !strings.Contains(err.Error(), c.expected) {
			t.Errorf("%s: %v, want ErrInvalid containing %q", name, err, c.expected)
		}
	}
	ok := perm("ok", map[api.ResourceType]model.Resource{
		api.ResourceArtifact: artifact(everywhere(
			"libs-local",
			"docs-cache",
			"central-cache",
			api.TargetAny,
			api.TargetAnyLocal,
			api.TargetAnyRemote,
			api.TargetAnyDistribution,
		),
			map[string]api.Actions{"bob": r}, map[string]api.Actions{"devs": r}),
		api.ResourceBuild:          artifact(everywhere(api.BuildInfoRepository), nil, nil),
		api.ResourceReleaseBundle:  artifact(everywhere("release-bundles"), nil, nil),
		api.ResourceDestination:    artifact(everywhere("edge-1"), nil, nil),
		api.ResourcePipelineSource: artifact(nil, nil, nil),
	})
	if err := svc.Validate(ctx, ok); err != nil {
		t.Fatalf("a permission naming every accepted target: %v", err)
	}
}

func TestUnitPutAndPutResource(t *testing.T) {
	ctx := context.Background()
	svc := service(seeded(t))
	full := perm("multi", map[api.ResourceType]model.Resource{
		api.ResourceArtifact: artifact(
			everywhere("libs-local"),
			map[string]api.Actions{"bob": r},
			nil,
		),
		api.ResourceBuild: artifact(
			everywhere(api.BuildInfoRepository),
			map[string]api.Actions{"bob": r},
			nil,
		),
	})
	created, err := svc.Put(ctx, full)
	if err != nil || !created {
		t.Fatalf("put = %v, %v", created, err)
	}
	// A v1-style write replaces the artifact section and keeps build.
	created, err = svc.PutResource(ctx, "multi", api.ResourceArtifact,
		artifact(everywhere("central"), map[string]api.Actions{"carol": r}, nil), true)
	if err != nil || created {
		t.Fatalf("put resource = %v, %v", created, err)
	}
	got, _ := svc.Get(ctx, "multi")
	if _, ok := got.Resources[api.ResourceBuild]; !ok ||
		got.Resources[api.ResourceArtifact].Actions.Users["carol"] != r {
		t.Fatalf("after put resource: %+v", got.Resources)
	}
	if !got.Created.Equal(clock) {
		t.Fatalf("created time lost: %v", got.Created)
	}
	// A whole replace drops sections it does not carry.
	if _, err := svc.Put(ctx, perm("multi", map[api.ResourceType]model.Resource{
		api.ResourceArtifact: artifact(everywhere("libs-local"), nil, nil),
	})); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Get(ctx, "multi"); len(got.Resources) != 1 {
		t.Fatalf("replace kept %v", got.ResourceTypes())
	}
	if _, err := svc.PutResource(ctx, "missing", api.ResourceArtifact,
		artifact(nil, nil, nil), false); !errors.Is(err, permission.ErrNotFound) {
		t.Fatalf("put resource without create: %v", err)
	}
	if created, err := svc.PutResource(ctx, "fresh", api.ResourceArtifact,
		artifact(everywhere("libs-local"), nil, nil), true); err != nil || !created {
		t.Fatalf("put resource creating: %v, %v", created, err)
	}
	if _, err := svc.Put(ctx, perm("bad", map[api.ResourceType]model.Resource{
		api.ResourceArtifact: artifact(everywhere("nope"), nil, nil),
	})); !errors.Is(err, permission.ErrInvalid) {
		t.Fatalf("invalid put: %v", err)
	}
	if _, err := svc.Put(ctx, perm("", nil)); !errors.Is(err, permission.ErrInvalid) {
		t.Fatalf("put without a name: %v", err)
	}
}

// TestUnitWrite checks that change sees the stored permission and that an
// error from it stores nothing.
func TestUnitWrite(t *testing.T) {
	ctx := context.Background()
	svc := service(seeded(t))
	first := perm("w", map[api.ResourceType]model.Resource{
		api.ResourceArtifact: artifact(
			everywhere("libs-local"),
			map[string]api.Actions{"bob": r},
			nil,
		),
	})
	created, err := svc.Write(
		ctx,
		"w",
		func(_ model.Permission, exists bool) (model.Permission, error) {
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
	_, err = svc.Write(ctx, "w", func(old model.Permission, exists bool) (model.Permission, error) {
		if !exists || old.Resources[api.ResourceArtifact].Actions.Users["bob"] != r {
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

// manager is a holder of MANAGE as CheckManagerEdit sees one: the user bob
// in the group leads, holding in a section what those two entries grant.
type manager struct{}

func (manager) Grantable(g model.Grants) api.Actions {
	held := g.Users["bob"] | g.Groups["leads"]
	if !held.Has(m) {
		return 0
	}
	return held &^ m
}

func (manager) Self() (user string, groups []string) { return "bob", []string{"leads"} }

// TestUnitCheckManagerEdit checks what a holder of MANAGE who is not an
// administrator may change: actions only, never in its own entry or its
// groups', and of the rest only the ones Grantable allows in each section,
// judged on the stored grants.
func TestUnitCheckManagerEdit(t *testing.T) {
	const (
		w = api.ActionWrite
		d = api.ActionDelete
	)
	sections := []api.ResourceType{
		api.ResourceArtifact, api.ResourceBuild, api.ResourceReleaseBundle,
	}
	// bob holds read, write and MANAGE on artifacts, and only read on builds.
	stored := func() model.Permission {
		return perm("p", map[api.ResourceType]model.Resource{
			api.ResourceArtifact: artifact(
				map[string]model.Target{"libs-local": {}},
				map[string]api.Actions{"bob": r | w | m, "carol": r},
				map[string]api.Actions{"ops": r | d, "leads": r},
			),
			api.ResourceBuild: artifact(
				everywhere(api.BuildInfoRepository),
				map[string]api.Actions{"bob": r},
				map[string]api.Actions{},
			),
			api.ResourceDestination: artifact(everywhere("edge"), nil, nil),
		})
	}
	const (
		actionsOnly = "can only edit the actions"
		heldOnly    = "only actions it holds"
		own         = "cannot change its own actions"
	)
	for _, c := range []struct {
		name     string
		edit     func(p *model.Permission)
		expected string
	}{
		{"no change, its own entries included", func(*model.Permission) {}, ""},
		{"grant a held action", func(p *model.Permission) {
			p.Resources[api.ResourceArtifact].Actions.Users["carol"] = r | w
		}, ""},
		{"revoke a held action", func(p *model.Permission) {
			delete(p.Resources[api.ResourceArtifact].Actions.Users, "carol")
		}, ""},
		{"add a group with held actions", func(p *model.Permission) {
			p.Resources[api.ResourceArtifact].Actions.Groups["devs"] = r | w
		}, ""},
		{"an empty include list is everything", func(p *model.Permission) {
			p.Resources[api.ResourceArtifact].Targets["libs-local"] = model.Target{
				Include: []string{"**"},
			}
		}, ""},
		{"a section the form does not carry", func(p *model.Permission) {
			delete(p.Resources, api.ResourceDestination)
		}, ""},
		{"grant an action it lacks", func(p *model.Permission) {
			p.Resources[api.ResourceArtifact].Actions.Users["carol"] = r | d
		}, heldOnly},
		{"grant manage", func(p *model.Permission) {
			p.Resources[api.ResourceArtifact].Actions.Users["carol"] = r | m
		}, heldOnly},
		{"revoke an action it lacks", func(p *model.Permission) {
			p.Resources[api.ResourceArtifact].Actions.Groups["ops"] = r
		}, heldOnly},
		{"remove a principal holding an action it lacks", func(p *model.Permission) {
			delete(p.Resources[api.ResourceArtifact].Actions.Groups, "ops")
		}, heldOnly},
		{"grant in a section it does not manage", func(p *model.Permission) {
			p.Resources[api.ResourceBuild].Actions.Users["carol"] = r
		}, heldOnly},
		// Each of these would pass the held-actions rule but for its entry.
		{"revoke a held action from itself", func(p *model.Permission) {
			p.Resources[api.ResourceArtifact].Actions.Users["bob"] = r | m
		}, own},
		{"revoke its own manage", func(p *model.Permission) {
			p.Resources[api.ResourceArtifact].Actions.Users["bob"] = r | w
		}, own},
		{"remove its own entry", func(p *model.Permission) {
			delete(p.Resources[api.ResourceArtifact].Actions.Users, "bob")
		}, own},
		{"grant its group a held action", func(p *model.Permission) {
			p.Resources[api.ResourceArtifact].Actions.Groups["leads"] = r | w
		}, own},
		{"remove its group's entry", func(p *model.Permission) {
			delete(p.Resources[api.ResourceArtifact].Actions.Groups, "leads")
		}, own},
		{"add its group to another section", func(p *model.Permission) {
			p.Resources[api.ResourceBuild].Actions.Groups["leads"] = r
		}, own},
		{"change a pattern", func(p *model.Permission) {
			p.Resources[api.ResourceArtifact].Targets["libs-local"] = model.Target{
				Exclude: []string{"secret/**"},
			}
		}, actionsOnly},
		{"add a target", func(p *model.Permission) {
			p.Resources[api.ResourceArtifact].Targets["central"] = model.Target{}
		}, actionsOnly},
		{"drop a section", func(p *model.Permission) {
			delete(p.Resources, api.ResourceBuild)
		}, actionsOnly},
		{"add a section", func(p *model.Permission) {
			p.Resources[api.ResourceReleaseBundle] = artifact(nil, nil, nil)
		}, actionsOnly},
	} {
		p := stored()
		c.edit(&p)
		err := permission.CheckManagerEdit(stored(), p, sections, manager{})
		switch {
		case c.expected == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.expected != "" && (!errors.Is(err, permission.ErrForbidden) ||
			!strings.Contains(err.Error(), c.expected)):
			t.Errorf("%s: %v, want %q", c.name, err, c.expected)
		}
	}
}

func TestUnitMergeAndDeleteResource(t *testing.T) {
	ctx := context.Background()
	svc := service(seeded(t))
	if err := svc.Create(ctx, perm("m", map[api.ResourceType]model.Resource{
		api.ResourceArtifact: artifact(everywhere("libs-local"),
			map[string]api.Actions{"bob": r}, map[string]api.Actions{"devs": r}),
	})); err != nil {
		t.Fatal(err)
	}
	merged, err := svc.MergeResource(ctx, "m", api.ResourceArtifact, artifact(
		everywhere("central"), map[string]api.Actions{"bob": r | m, "carol": r}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if merged.Actions.Users["bob"] != r|m || merged.Actions.Users["carol"] != r ||
		merged.Actions.Groups["devs"] != r || len(merged.Targets) != 2 {
		t.Fatalf("merged = %+v", merged)
	}
	// A section with no groups stays without them, then gains some.
	if err := svc.Create(ctx, perm("n", map[api.ResourceType]model.Resource{
		api.ResourceArtifact: artifact(
			everywhere("libs-local"),
			map[string]api.Actions{"bob": r},
			nil,
		),
	})); err != nil {
		t.Fatal(err)
	}
	if merged, err = svc.MergeResource(
		ctx,
		"n",
		api.ResourceArtifact,
		artifact(nil, nil, nil),
	); err != nil ||
		len(merged.Actions.Groups) != 0 ||
		merged.Actions.Users["bob"] != r {
		t.Fatalf("empty patch = %+v, %v", merged, err)
	}
	if merged, err = svc.MergeResource(
		ctx,
		"n",
		api.ResourceArtifact,
		artifact(
			nil,
			nil,
			map[string]api.Actions{"devs": r},
		),
	); err != nil || merged.Actions.Groups["devs"] != r {
		t.Fatalf("groups patch = %+v, %v", merged, err)
	}
	if _, err := svc.MergeResource(
		ctx,
		"m",
		api.ResourceBuild,
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
		api.ResourceArtifact,
		artifact(nil, nil, nil),
	); !errors.Is(
		err,
		permission.ErrNotFound,
	) {
		t.Fatalf("merge into a missing permission: %v", err)
	}
	if err := svc.DeleteResource(ctx, "m", api.ResourceArtifact); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx, "m")
	if err != nil || len(got.Resources) != 0 {
		t.Fatalf("permission after deleting its only section: %+v, %v", got, err)
	}
	if err := svc.DeleteResource(
		ctx,
		"m",
		api.ResourceArtifact,
	); !errors.Is(
		err,
		permission.ErrNotFound,
	) {
		t.Fatalf("delete a missing section: %v", err)
	}
	if err := svc.DeleteResource(
		ctx,
		"none",
		api.ResourceArtifact,
	); !errors.Is(
		err,
		permission.ErrNotFound,
	) {
		t.Fatalf("delete a section of a missing permission: %v", err)
	}
}

func TestUnitGrantingAndList(t *testing.T) {
	ctx := context.Background()
	svc := service(seeded(t))
	for _, p := range []model.Permission{
		perm("to-bob", map[api.ResourceType]model.Resource{
			api.ResourceArtifact: artifact(everywhere("libs-local"), map[string]api.Actions{"bob": r}, nil),
		}),
		perm("to-devs", map[api.ResourceType]model.Resource{
			api.ResourceArtifact: artifact(everywhere("libs-local"), nil, map[string]api.Actions{"devs": r}),
		}),
		perm("to-readers", map[api.ResourceType]model.Resource{
			api.ResourceArtifact: artifact(everywhere("libs-local"), nil, map[string]api.Actions{"readers": r}),
		}),
	} {
		if err := svc.Create(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	names := func(ps []model.Permission) []string {
		var out []string
		for _, p := range ps {
			out = append(out, p.Name)
		}
		return out
	}
	for _, c := range []struct {
		users, groups []string
		inherited     bool
		expected      []string
	}{
		{[]string{"bob"}, nil, false, []string{"to-bob"}},
		{[]string{"bob"}, nil, true, []string{"to-bob", "to-devs"}},
		{nil, []string{"readers"}, false, []string{"to-readers"}},
		{[]string{"carol"}, []string{"devs", "readers"}, true, []string{"to-devs", "to-readers"}},
	} {
		got, _, err := svc.Granting(ctx, c.users, c.groups, c.inherited)
		if err != nil || !slices.Equal(names(got), c.expected) {
			t.Errorf("granting %v %v %v = %v, %v; want %v", c.users, c.groups, c.inherited,
				names(got), err, c.expected)
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

func TestUnitValidResourceType(t *testing.T) {
	for _, rt := range []api.ResourceType{"artifact", "build", "release_bundle", "destination", "pipeline_source"} {
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

func TestUnitDefaultClock(t *testing.T) {
	ctx := context.Background()
	svc := permission.Service{Store: seeded(t)}
	if err := svc.Create(ctx, perm("now", nil)); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Get(ctx, "now"); got.Created.IsZero() {
		t.Fatal("no creation time")
	}
}

// TestUnitNarrow keeps the grants to the principals a listing asked about: the
// user, and its groups when inherited grants count.
func TestUnitNarrow(t *testing.T) {
	ctx := context.Background()
	svc := service(seeded(t))
	if err := svc.Create(ctx, perm("shared", map[api.ResourceType]model.Resource{
		api.ResourceArtifact: artifact(everywhere("libs-local"),
			map[string]api.Actions{"bob": r, "carol": r | m},
			map[string]api.Actions{"devs": r, "readers": r}),
	})); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		inherited bool
		expected  model.Grants
	}{
		{true, model.Grants{
			Users:  map[string]api.Actions{"bob": r},
			Groups: map[string]api.Actions{"devs": r},
		}},
		{false, model.Grants{Users: map[string]api.Actions{"bob": r}}},
	} {
		ps, pr, err := svc.Granting(ctx, []string{"bob"}, nil, c.inherited)
		if err != nil || len(ps) != 1 {
			t.Fatalf("granting = %v, %v", ps, err)
		}
		got := pr.Narrow(ps[0])
		if a := got.Resources[api.ResourceArtifact]; !reflect.DeepEqual(a.Actions, c.expected) ||
			len(a.Targets) != 1 {
			t.Errorf("inherited %v: narrowed to %+v, want %+v", c.inherited, a, c.expected)
		}
	}
	if got := (permission.Principals{}).Narrow(perm("bare", nil)); len(got.Resources) != 0 {
		t.Errorf("narrowing a permission without sections = %+v", got)
	}
}

// TestUnitFoldUsers checks the Access listing's user filter matches names
// without regard to case (getpermissions) by looking up the folded name: a
// row stored before user names were folded, Bob, is no longer reached, and
// bob is.
func TestUnitFoldUsers(t *testing.T) {
	ctx := context.Background()
	db := seeded(t)
	if err := db.Users().Create(ctx, model.User{Name: "Bob"}); err != nil {
		t.Fatal(err)
	}
	svc := service(db)
	for _, c := range []struct {
		names, expected []string
	}{
		{nil, nil},
		{[]string{"carol"}, []string{"carol"}},
		{[]string{"CAROL"}, []string{"carol"}},
		{[]string{"BOB"}, []string{"bob"}},
		{[]string{"Bob"}, []string{"bob"}},
		{[]string{"bob", "Carol", "bOb"}, []string{"bob", "carol"}},
	} {
		got, err := svc.FoldUsers(ctx, c.names)
		if err != nil || !slices.Equal(got, c.expected) {
			t.Errorf("FoldUsers(%v) = %v, %v; want %v", c.names, got, err, c.expected)
		}
	}
	if _, err := svc.FoldUsers(
		ctx,
		[]string{"carol", "Zed"},
	); !errors.Is(
		err,
		permission.ErrNotFound,
	) ||
		!strings.Contains(err.Error(), "'Zed'") {
		t.Fatalf("an unknown user: %v", err)
	}
}
