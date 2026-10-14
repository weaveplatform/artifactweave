package authz

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/pkg/types/wire"
)

const (
	r = types.ActionRead
	w = types.ActionWrite
	d = types.ActionDelete
	m = types.ActionManage
)

func target(include, exclude []string) types.Target {
	return types.Target{Include: include, Exclude: exclude}
}

// world is a permission set covering every evaluation rule.
func world(t *testing.T) *inmem.Store {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	for _, g := range []types.Group{
		{Name: "devs"}, {Name: "readers"}, {Name: "admins", AdminPrivileges: true},
	} {
		if err := db.Groups().Create(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []types.Permission{
		{Name: "dev-local", Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{Groups: map[string]types.Actions{"devs": r | w}},
				Targets: map[string]types.Target{"libs-local": target([]string{"org/acme/**"}, []string{"org/acme/secret/**"})},
			},
			types.ResourceBuild: {
				Actions: types.Grants{Groups: map[string]types.Actions{"devs": r}},
				Targets: map[string]types.Target{types.BuildInfoRepository: target([]string{"app/**"}, nil)},
			},
		}},
		{Name: "anything", Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{Groups: map[string]types.Actions{"readers": r}},
				Targets: map[string]types.Target{types.TargetAny: target(nil, nil)},
			},
		}},
		{Name: "any-remote", Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{"anonymous": r | w}},
				Targets: map[string]types.Target{types.TargetAnyRemote: target([]string{"**"}, nil)},
			},
		}},
		{Name: "bob-jars", Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{"bob": d}},
				Targets: map[string]types.Target{"libs-local": target([]string{"**/*.jar"}, []string{""})},
			},
		}},
		{Name: "deleted-repo", Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{"bob": m}},
				Targets: map[string]types.Target{"gone-local": target(nil, nil)},
			},
		}},
	} {
		if err := db.Permissions().Create(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func scope(t *testing.T, s string) *types.Scope {
	t.Helper()
	sc, err := types.ParseScope(s)
	if err != nil {
		t.Fatal(err)
	}
	return &sc
}

var (
	libs    = Repo{Key: "libs-local", Class: "local"}
	ext     = Repo{Key: "ext-local", Class: "local"}
	virt    = Repo{Key: "all", Class: "virtual"}
	jcenter = Repo{Key: "jcenter", Class: "remote"}
	cache   = Repo{Key: "jcenter-cache", Class: "remote"}
	builds  = Repo{Key: types.BuildInfoRepository}
)

type check struct {
	rt     types.ResourceType
	repo   Repo
	path   string
	folder bool
	want   types.Actions
	ok     bool
}

func art(repo Repo, path string, want types.Actions, ok bool) check {
	return check{types.ResourceArtifact, repo, path, false, want, ok}
}

func dir(repo Repo, path string, want types.Actions, ok bool) check {
	return check{types.ResourceArtifact, repo, path, true, want, ok}
}

func TestEvaluation(t *testing.T) {
	ctx := context.Background()
	db := world(t)
	bob := Subject{Name: "bob", Groups: []string{"devs"}}
	for _, c := range []struct {
		name      string
		subject   Subject
		anonymous bool
		admin     bool
		checks    []check
	}{
		{"admin user", Subject{Name: "root", Admin: true}, false, true, []check{
			art(ext, "any/thing", r|w|d|m, true),
			{types.ResourceBuild, builds, "x/1", false, r | m, true},
		}},
		{"admin through a group", Subject{Name: "carol", Groups: []string{"admins"}}, false, true, []check{
			art(ext, "a", d, true),
		}},
		{"group grant with include and exclude", bob, false, false, []check{
			art(libs, "org/acme/a.txt", r|w, true),
			art(libs, "org/acme/a.txt", d, false),
			art(libs, "org/acme/secret/key.txt", r, false),
			art(libs, "org/other/a.txt", r, false),
			art(ext, "org/acme/a.txt", r, false),
			art(virt, "org/acme/a.txt", r, false),
		}},
		{"user and group grants unite", bob, false, false, []check{
			art(libs, "org/acme/lib/x.jar", r|w|d, true),
			art(libs, "x.jar", d, true),
			art(libs, "x.jar", r, false),
		}},
		{"folders on the way to permitted paths", bob, false, false, []check{
			dir(libs, "", r, true),
			dir(libs, "org", r, true),
			dir(libs, "org/acme", r, true),
			dir(libs, "org/other", r, false),
			dir(libs, "org/acme/secret", r, false),
		}},
		{"a target naming a deleted repository grants nothing elsewhere", bob, false, false, []check{
			art(libs, "org/acme/a.txt", m, false),
			art(Repo{Key: "gone-local", Class: "local"}, "x", m, true),
		}},
		{"build resource", bob, false, false, []check{
			{types.ResourceBuild, builds, "app/42", false, r, true},
			{types.ResourceBuild, builds, "lib/1", false, r, false},
			{types.ResourceReleaseBundle, builds, "app/42", false, r, false},
		}},
		{"ANY covers every class", Subject{Name: "dave", Groups: []string{"readers"}}, false, false, []check{
			art(libs, "x", r, true), art(virt, "x", r, true), art(jcenter, "x", r, true),
			art(libs, "x", w, false),
		}},
		{"anonymous off grants nothing", Subject{Name: "anonymous", Anonymous: true}, false, false, []check{
			art(jcenter, "x", r, false),
		}},
		{"anonymous on: its grants and the remote cache", Subject{Name: "anonymous", Anonymous: true}, true, false, []check{
			art(jcenter, "x", r|w, true), art(cache, "x", r, true), art(libs, "x", r, false),
		}},
		{"want of nothing is never allowed", bob, false, false, []check{
			art(libs, "org/acme/a.txt", 0, false),
		}},
		{"applied-permissions/user is the user", Subject{Name: "bob", Groups: []string{"devs"}, Scope: scope(t, "applied-permissions/user")}, false, false, []check{
			art(libs, "org/acme/a.txt", r|w, true), art(libs, "x.jar", d, true),
		}},
		{"groups scope drops the user's own grants", Subject{Name: "bob", Groups: []string{"devs"}, Scope: scope(t, "applied-permissions/groups:devs")}, false, false, []check{
			art(libs, "org/acme/a.txt", r|w, true), art(libs, "x.jar", d, false),
		}},
		{"groups scope never adds a group", Subject{Name: "bob", Groups: []string{"devs"}, Scope: scope(t, "applied-permissions/groups:readers")}, false, false, []check{
			art(libs, "org/acme/a.txt", r, false), art(ext, "x", r, false),
		}},
		{"artifact scope intersects", Subject{Name: "bob", Groups: []string{"devs"}, Scope: scope(t, "artifact:libs-local/org/acme/**:r")}, false, false, []check{
			art(libs, "org/acme/a.txt", r, true),
			art(libs, "org/acme/a.txt", w, false),
			dir(libs, "org", r, true),
			art(libs, "org/acme/lib/x.jar", d, false),
			{types.ResourceBuild, builds, "app/1", false, r, false},
		}},
		{"artifact scope on top of applied permissions", Subject{Name: "bob", Groups: []string{"devs"}, Scope: scope(t, "applied-permissions/user artifact:*:r,w")}, false, false, []check{
			art(libs, "org/acme/a.txt", r|w, true),
			art(libs, "x.jar", d, false),
			{types.ResourceBuild, builds, "app/1", false, r, true},
		}},
		{"an admin's resource-only token keeps content, loses admin", Subject{Name: "root", Admin: true, Scope: scope(t, "artifact:ext-local:r")}, false, false, []check{
			art(ext, "x/y", r, true), art(ext, "x/y", w, false), art(libs, "x", r, false),
		}},
		{"an admin's groups token is not admin", Subject{Name: "root", Admin: true, Groups: []string{"devs"}, Scope: scope(t, "applied-permissions/groups:devs")}, false, false, []check{
			art(libs, "org/acme/a.txt", r, true), art(ext, "x", r, false),
		}},
		{"a groups token through an admin group is admin", Subject{Name: "svc", Groups: []string{"admins"}, Scope: scope(t, "applied-permissions/groups:admins")}, false, true, []check{
			art(ext, "x", d, true),
		}},
		{"roles scope grants nothing until projects", Subject{Name: "bob", Groups: []string{"devs"}, Scope: scope(t, "applied-permissions/roles:proj:developer")}, false, false, []check{
			art(libs, "org/acme/a.txt", r, false),
		}},
		{"admin scope for a non-admin grants no admin", Subject{Name: "bob", Groups: []string{"devs"}, Scope: scope(t, "applied-permissions/admin")}, false, false, []check{
			art(ext, "x", r, false), art(libs, "org/acme/a.txt", r, true),
		}},
		{"a membership in a deleted group", Subject{Name: "eve", Groups: []string{"ghosts", "devs"}}, false, false, []check{
			art(libs, "org/acme/a.txt", r, true),
		}},
	} {
		dec, err := New(
			ctx,
			Sources{
				Permissions:      db.Permissions(),
				Groups:           db.Groups(),
				AnonymousEnabled: c.anonymous,
			},
			c.subject,
		)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if dec.Admin() != c.admin {
			t.Errorf("%s: admin %v, want %v", c.name, dec.Admin(), c.admin)
		}
		for _, k := range c.checks {
			var got bool
			if k.folder {
				got = dec.AllowsFolder(k.rt, k.repo, k.path, k.want)
			} else {
				got = dec.Allows(k.rt, k.repo, k.path, k.want)
			}
			if got != k.ok {
				t.Errorf(
					"%s: %s %s %q folder=%v want %v: got %v (holds %v)",
					c.name,
					k.rt,
					k.repo.Key,
					k.path,
					k.folder,
					k.want.AccessNames(),
					got,
					dec.Actions(k.rt, k.repo, k.path, k.folder).AccessNames(),
				)
			}
		}
	}
}

func TestScopedConfigurationAndSystem(t *testing.T) {
	ctx := context.Background()
	db := world(t)
	src := Sources{Permissions: db.Permissions(), Groups: db.Groups()}
	mk := func(s string, admin bool) *Decider {
		t.Helper()
		sub := Subject{Name: "bob", Groups: []string{"devs"}, Admin: admin}
		if s != "" {
			sub.Scope = scope(t, s)
		}
		dec, err := New(ctx, src, sub)
		if err != nil {
			t.Fatal(err)
		}
		return dec
	}
	for _, c := range []struct {
		scope         string
		admin         bool
		repo          string
		config        bool
		area          string
		system        bool
		systemActions types.ScopeActions
	}{
		{"", false, "libs-local", true, "info", false, types.ScopeRead},
		{"", true, "libs-local", true, "metrics", true, types.ScopeRead},
		{"applied-permissions/user", false, "ext-local", true, "info", false, types.ScopeRead},
		{"repo:libs-local:r", false, "libs-local", true, "info", false, types.ScopeRead},
		{"repo:libs-local:r", false, "ext-local", false, "info", false, types.ScopeRead},
		{"applied-permissions/user repo:*:r", false, "ext-local", true, "info", false, types.ScopeRead},
		{"artifact:libs-local:r", false, "libs-local", false, "info", false, types.ScopeRead},
		{"system:info:r", false, "libs-local", false, "info/storage", true, types.ScopeRead},
		{"system:info:r", false, "libs-local", false, "info", false, types.ScopeWrite},
		{"system:info:r", false, "libs-local", false, "metrics", false, types.ScopeRead},
	} {
		dec := mk(c.scope, c.admin)
		if got := dec.RepoConfig(c.repo); got != c.config {
			t.Errorf("scope %q: RepoConfig(%s) = %v, want %v", c.scope, c.repo, got, c.config)
		}
		if got := dec.System(c.area, c.systemActions); got != c.system {
			t.Errorf(
				"scope %q: System(%s, %v) = %v, want %v",
				c.scope,
				c.area,
				c.systemActions,
				got,
				c.system,
			)
		}
	}
}

func TestScopeLettersMapToActions(t *testing.T) {
	for _, c := range []struct {
		letters string
		want    types.Actions
	}{
		{"r", r},
		{"w", w},
		{"d", d},
		{"m", m},
		{"a", types.ActionAnnotate},
		{"x", types.ActionExecute},
		{"s", types.ActionScan},
		{"*", allActions},
	} {
		sc, err := types.ParseScope("artifact:x:" + c.letters)
		if err != nil {
			t.Fatal(err)
		}
		if got := toActions(sc.Entries[0].Actions); got != c.want {
			t.Errorf("%s: %v, want %v", c.letters, got.AccessNames(), c.want.AccessNames())
		}
	}
}

var errBoom = errors.New("boom")

type failingPermissions struct{ store.PermissionStore }

func (failingPermissions) ForPrincipal(
	context.Context,
	string,
	[]string,
) ([]types.Permission, error) {
	return nil, errBoom
}

type failingGroups struct{ store.GroupStore }

func (failingGroups) Get(context.Context, string) (types.Group, error) {
	return types.Group{}, errBoom
}

func TestNewReportsStoreFailures(t *testing.T) {
	ctx := context.Background()
	db := world(t)
	sub := Subject{Name: "bob", Groups: []string{"devs"}}
	if _, err := New(
		ctx,
		Sources{Permissions: failingPermissions{}, Groups: db.Groups()},
		sub,
	); !errors.Is(
		err,
		errBoom,
	) {
		t.Errorf("permissions failure: %v", err)
	}
	if _, err := New(
		ctx,
		Sources{Permissions: db.Permissions(), Groups: failingGroups{}},
		sub,
	); !errors.Is(
		err,
		errBoom,
	) {
		t.Errorf("groups failure: %v", err)
	}
}

// One grant written in each wire form (research 05's documented shapes)
// must decide identically once converted to the internal permission.
func TestWireFormsEvaluateAlike(t *testing.T) {
	ctx := context.Background()
	v1 := `{"name":"p","includesPattern":"org/**","excludesPattern":"org/secret/**",
		"repositories":["libs-local","ANY REMOTE"],
		"principals":{"users":{"bob":["r","w"]},"groups":{"devs":["r"]}}}`
	v2 := `{"name":"p","repo":{"include-patterns":["org/**"],"exclude-patterns":["org/secret/**"],
		"repositories":["libs-local","ANY REMOTE"],
		"actions":{"users":{"bob":["read","write"]},"groups":{"devs":["read"]}}}}`
	access := `{"name":"p","resources":{"artifact":{
		"actions":{"users":{"bob":["READ","WRITE"]},"groups":{"devs":["READ"]}},
		"targets":{"libs-local":{"include_patterns":["org/**"],"exclude_patterns":["org/secret/**"]},
		           "ANY REMOTE":{"include_patterns":["org/**"],"exclude_patterns":["org/secret/**"]}}}}}`
	decisions := map[string][]types.Actions{}
	for name, convert := range map[string]func() (types.Permission, error){
		"v1": func() (types.Permission, error) {
			var w wire.PermissionTargetV1
			if err := json.Unmarshal([]byte(v1), &w); err != nil {
				return types.Permission{}, err
			}
			return types.PermissionFromV1(w)
		},
		"v2": func() (types.Permission, error) {
			var w wire.PermissionTargetV2
			if err := json.Unmarshal([]byte(v2), &w); err != nil {
				return types.Permission{}, err
			}
			return types.PermissionFromV2(w)
		},
		"access": func() (types.Permission, error) {
			var w wire.AccessPermission
			if err := json.Unmarshal([]byte(access), &w); err != nil {
				return types.Permission{}, err
			}
			return types.PermissionFromAccess(w)
		},
	} {
		p, err := convert()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		db := inmem.New()
		if err := db.Groups().Create(ctx, types.Group{Name: "devs"}); err != nil {
			t.Fatal(err)
		}
		if err := db.Permissions().Create(ctx, p); err != nil {
			t.Fatal(err)
		}
		for _, sub := range []Subject{{Name: "bob"}, {Name: "carol", Groups: []string{"devs"}}} {
			dec, err := New(ctx, Sources{Permissions: db.Permissions(), Groups: db.Groups()}, sub)
			if err != nil {
				t.Fatal(err)
			}
			for _, k := range []struct {
				repo Repo
				path string
			}{
				{libs, "org/a.txt"},
				{libs, "org/secret/k"},
				{libs, "com/a"},
				{jcenter, "org/a"},
				{cache, "org/a"},
				{ext, "org/a"},
			} {
				decisions[name] = append(
					decisions[name],
					dec.Actions(types.ResourceArtifact, k.repo, k.path, false),
				)
			}
		}
	}
	// bob: read and write on libs-local/org and the remote and its cache;
	// carol through devs: read on the same; nothing under org/secret, com
	// or in other repositories.
	want := []types.Actions{r | w, 0, 0, r | w, r | w, 0, r, 0, 0, r, r, 0}
	for name, got := range decisions {
		if !slices.Equal(got, want) {
			t.Errorf("%s decides %v, want %v", name, got, want)
		}
	}
}

func TestRepoVisibleAndGroups(t *testing.T) {
	ctx := context.Background()
	db := world(t)
	src := Sources{Permissions: db.Permissions(), Groups: db.Groups()}
	mk := func(s string, admin bool, groups ...string) *Decider {
		t.Helper()
		sub := Subject{Name: "bob", Groups: groups, Admin: admin}
		if s != "" {
			sub.Scope = scope(t, s)
		}
		dec, err := New(ctx, src, sub)
		if err != nil {
			t.Fatal(err)
		}
		return dec
	}
	for _, c := range []struct {
		scope   string
		admin   bool
		repo    Repo
		visible bool
	}{
		// devs read only org/acme/** in libs-local: the root leads there.
		{"", false, libs, true},
		{"", false, ext, false},
		{"", true, ext, true},
		// repo entries decide alone, whatever the content rights.
		{"repo:libs-local:r", false, libs, true},
		{"repo:libs-local:r", false, ext, false},
		{"applied-permissions/user repo:*:r", false, ext, true},
		// system:info:r lists every repository.
		{"system:info:r", false, ext, true},
		// an artifact scope sees what it may read.
		{"artifact:libs-local:r", false, libs, true},
		{"artifact:libs-local:r", false, ext, false},
	} {
		if got := mk(c.scope, c.admin, "devs").RepoVisible(c.repo); got != c.visible {
			t.Errorf("scope %q admin %v: RepoVisible(%s) = %v, want %v",
				c.scope, c.admin, c.repo.Key, got, c.visible)
		}
	}
	if got := mk(
		"",
		false,
		"devs",
		"readers",
	).Groups(); !slices.Equal(
		got,
		[]string{"devs", "readers"},
	) {
		t.Errorf("unscoped groups %v", got)
	}
	if got := mk(
		"applied-permissions/groups:readers",
		false,
		"devs",
		"readers",
	).Groups(); !slices.Equal(
		got,
		[]string{"readers"},
	) {
		t.Errorf("a groups scope narrows to the named groups: %v", got)
	}
}

func TestTargetAllows(t *testing.T) {
	tg := target([]string{"com/**"}, []string{"**/*.tmp"})
	for _, c := range []struct {
		path   string
		folder bool
		want   bool
	}{
		{"com/a.jar", false, true},
		{"com/a.tmp", false, false},
		{"org/a.jar", false, false},
		{"", true, true},
		{"", false, false},
	} {
		if got := TargetAllows(tg, c.path, c.folder); got != c.want {
			t.Errorf("TargetAllows(%q, folder=%v) = %v, want %v", c.path, c.folder, got, c.want)
		}
	}
	if !TargetAllows(types.Target{}, "anything", false) {
		t.Error("an empty include list admits everything")
	}
}

func TestHolds(t *testing.T) {
	ctx := context.Background()
	db := world(t)
	grants := types.Grants{
		Users:  map[string]types.Actions{"bob": m},
		Groups: map[string]types.Actions{"devs": r | m},
	}
	for _, c := range []struct {
		name string
		sub  Subject
		want bool
	}{
		{"direct grant", Subject{Name: "bob"}, true},
		{"through a group", Subject{Name: "carol", Groups: []string{"devs"}}, true},
		{"no grant", Subject{Name: "carol", Groups: []string{"readers"}}, false},
		{"a groups scope drops the user's own grant", Subject{
			Name: "bob", Scope: scope(t, "applied-permissions/groups:readers"),
		}, false},
		{"anonymous while anonymous access is off", Subject{Name: "bob", Anonymous: true}, false},
	} {
		dec, err := New(ctx, Sources{Permissions: db.Permissions(), Groups: db.Groups()}, c.sub)
		if err != nil {
			t.Fatal(err)
		}
		if got := dec.Holds(grants, m); got != c.want {
			t.Errorf("%s: Holds = %v, want %v", c.name, got, c.want)
		}
	}
}
