package authz

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/adapters/inmem"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/kernel/wire"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

const (
	r = accessapi.ActionRead
	w = accessapi.ActionWrite
	d = accessapi.ActionDelete
	m = accessapi.ActionManage
)

func target(include, exclude []string) model.Target {
	return model.Target{Include: include, Exclude: exclude}
}

// world is a permission set covering every evaluation rule.
func world(t *testing.T) *inmem.Store {
	t.Helper()
	ctx := context.Background()
	db := inmem.New(memtx.New())
	for _, g := range []model.Group{
		{Name: "devs"}, {Name: "readers"}, {Name: "admins", AdminPrivileges: true},
	} {
		if err := db.Groups().Create(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []model.Permission{
		{Name: "dev-local", Resources: map[accessapi.ResourceType]model.Resource{
			accessapi.ResourceArtifact: {
				Actions: model.Grants{Groups: map[string]accessapi.Actions{"devs": r | w}},
				Targets: map[string]model.Target{"libs-local": target([]string{"org/acme/**"}, []string{"org/acme/secret/**"})},
			},
			accessapi.ResourceBuild: {
				Actions: model.Grants{Groups: map[string]accessapi.Actions{"devs": r}},
				Targets: map[string]model.Target{accessapi.BuildInfoRepository: target([]string{"app/**"}, nil)},
			},
		}},
		{Name: "anything", Resources: map[accessapi.ResourceType]model.Resource{
			accessapi.ResourceArtifact: {
				Actions: model.Grants{Groups: map[string]accessapi.Actions{"readers": r}},
				Targets: map[string]model.Target{accessapi.TargetAny: target(nil, nil)},
			},
		}},
		{Name: "any-remote", Resources: map[accessapi.ResourceType]model.Resource{
			accessapi.ResourceArtifact: {
				Actions: model.Grants{Users: map[string]accessapi.Actions{"anonymous": r | w}},
				Targets: map[string]model.Target{accessapi.TargetAnyRemote: target([]string{"**"}, nil)},
			},
		}},
		{Name: "bob-jars", Resources: map[accessapi.ResourceType]model.Resource{
			accessapi.ResourceArtifact: {
				Actions: model.Grants{Users: map[string]accessapi.Actions{"bob": d}},
				Targets: map[string]model.Target{"libs-local": target([]string{"**/*.jar"}, []string{""})},
			},
		}},
		{Name: "deleted-repo", Resources: map[accessapi.ResourceType]model.Resource{
			accessapi.ResourceArtifact: {
				Actions: model.Grants{Users: map[string]accessapi.Actions{"bob": m}},
				Targets: map[string]model.Target{"gone-local": target(nil, nil)},
			},
		}},
	} {
		if err := db.Permissions().Create(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func scope(t *testing.T, s string) *kernel.Scope {
	t.Helper()
	sc, err := kernel.ParseScope(s)
	if err != nil {
		t.Fatal(err)
	}
	return &sc
}

var (
	libs    = accessapi.Repo{Key: "libs-local", Class: "local"}
	ext     = accessapi.Repo{Key: "ext-local", Class: "local"}
	virt    = accessapi.Repo{Key: "all", Class: "virtual"}
	jcenter = accessapi.Repo{Key: "jcenter", Class: "remote"}
	cache   = accessapi.Repo{Key: "jcenter-cache", Class: "remote"}
	builds  = accessapi.Repo{Key: accessapi.BuildInfoRepository}
)

type check struct {
	rt       accessapi.ResourceType
	repo     accessapi.Repo
	path     string
	folder   bool
	expected accessapi.Actions
	ok       bool
}

func art(repo accessapi.Repo, path string, want accessapi.Actions, ok bool) check {
	return check{accessapi.ResourceArtifact, repo, path, false, want, ok}
}

func dir(repo accessapi.Repo, path string, want accessapi.Actions, ok bool) check {
	return check{accessapi.ResourceArtifact, repo, path, true, want, ok}
}

func TestUnitEvaluation(t *testing.T) {
	ctx := context.Background()
	db := world(t)
	bob := accessapi.Subject{Name: "bob", Groups: []string{"devs"}}
	for _, c := range []struct {
		name      string
		subject   accessapi.Subject
		anonymous bool
		admin     bool
		checks    []check
	}{
		{"admin user", accessapi.Subject{Name: "root", Admin: true}, false, true, []check{
			art(ext, "any/thing", r|w|d|m, true),
			{accessapi.ResourceBuild, builds, "x/1", false, r | m, true},
		}},
		{"admin through a group", accessapi.Subject{Name: "carol", Groups: []string{"admins"}}, false, true, []check{
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
			art(accessapi.Repo{Key: "gone-local", Class: "local"}, "x", m, true),
		}},
		{"build resource", bob, false, false, []check{
			{accessapi.ResourceBuild, builds, "app/42", false, r, true},
			{accessapi.ResourceBuild, builds, "lib/1", false, r, false},
			{accessapi.ResourceReleaseBundle, builds, "app/42", false, r, false},
		}},
		{"any covers every class", accessapi.Subject{Name: "dave", Groups: []string{"readers"}}, false, false, []check{
			art(libs, "x", r, true), art(virt, "x", r, true), art(jcenter, "x", r, true),
			art(libs, "x", w, false),
		}},
		{"anonymous off grants nothing", accessapi.Subject{Name: "anonymous", Anonymous: true}, false, false, []check{
			art(jcenter, "x", r, false),
		}},
		{"anonymous on: its grants and the remote cache", accessapi.Subject{Name: "anonymous", Anonymous: true}, true, false, []check{
			art(jcenter, "x", r|w, true), art(cache, "x", r, true), art(libs, "x", r, false),
		}},
		{"want of nothing is never allowed", bob, false, false, []check{
			art(libs, "org/acme/a.txt", 0, false),
		}},
		{"applied-permissions/user is the user", accessapi.Subject{Name: "bob", Groups: []string{"devs"}, Scope: scope(t, "applied-permissions/user")}, false, false, []check{
			art(libs, "org/acme/a.txt", r|w, true), art(libs, "x.jar", d, true),
		}},
		{"groups scope drops the user's own grants", accessapi.Subject{Name: "bob", Groups: []string{"devs"}, Scope: scope(t, "applied-permissions/groups:devs")}, false, false, []check{
			art(libs, "org/acme/a.txt", r|w, true), art(libs, "x.jar", d, false),
		}},
		{"groups scope never adds a group", accessapi.Subject{Name: "bob", Groups: []string{"devs"}, Scope: scope(t, "applied-permissions/groups:readers")}, false, false, []check{
			art(libs, "org/acme/a.txt", r, false), art(ext, "x", r, false),
		}},
		{"artifact scope intersects", accessapi.Subject{Name: "bob", Groups: []string{"devs"}, Scope: scope(t, "artifact:libs-local/org/acme/**:r")}, false, false, []check{
			art(libs, "org/acme/a.txt", r, true),
			art(libs, "org/acme/a.txt", w, false),
			dir(libs, "org", r, true),
			art(libs, "org/acme/lib/x.jar", d, false),
			{accessapi.ResourceBuild, builds, "app/1", false, r, false},
		}},
		{"artifact scope on top of applied permissions", accessapi.Subject{Name: "bob", Groups: []string{"devs"}, Scope: scope(t, "applied-permissions/user artifact:*:r,w")}, false, false, []check{
			art(libs, "org/acme/a.txt", r|w, true),
			art(libs, "x.jar", d, false),
			{accessapi.ResourceBuild, builds, "app/1", false, r, true},
		}},
		{"build scope intersects", accessapi.Subject{Name: "bob", Groups: []string{"devs"}, Scope: scope(t, "build:"+accessapi.BuildInfoRepository+"/app/4*:r")}, false, false, []check{
			{accessapi.ResourceBuild, builds, "app/42", false, r, true},
			{accessapi.ResourceBuild, builds, "app/1", false, r, false},
			{accessapi.ResourceBuild, builds, "lib/4", false, r, false},
			{accessapi.ResourceBuild, builds, "app/42", false, w, false},
			art(libs, "org/acme/a.txt", r, false),
		}},
		{"build scope on top of applied permissions", accessapi.Subject{Name: "bob", Groups: []string{"devs"}, Scope: scope(t, "applied-permissions/user build:*:r")}, false, false, []check{
			{accessapi.ResourceBuild, builds, "app/1", false, r, true},
			{accessapi.ResourceBuild, builds, "lib/1", false, r, false},
			art(libs, "org/acme/a.txt", r, true),
		}},
		{"a repo scope reaches no artifact", accessapi.Subject{Name: "bob", Groups: []string{"devs"}, Scope: scope(t, "repo:libs-local:r")}, false, false, []check{
			art(libs, "org/acme/a.txt", r, false),
		}},
		{"an admin's resource-only token keeps content, loses admin", accessapi.Subject{Name: "root", Admin: true, Scope: scope(t, "artifact:ext-local:r")}, false, false, []check{
			art(ext, "x/y", r, true), art(ext, "x/y", w, false), art(libs, "x", r, false),
		}},
		{"an admin's groups token is not admin", accessapi.Subject{Name: "root", Admin: true, Groups: []string{"devs"}, Scope: scope(t, "applied-permissions/groups:devs")}, false, false, []check{
			art(libs, "org/acme/a.txt", r, true), art(ext, "x", r, false),
		}},
		{"a groups token through an admin group is admin", accessapi.Subject{Name: "svc", Groups: []string{"admins"}, Scope: scope(t, "applied-permissions/groups:admins")}, false, true, []check{
			art(ext, "x", d, true),
		}},
		{"roles scope grants nothing until projects", accessapi.Subject{Name: "bob", Groups: []string{"devs"}, Scope: scope(t, "applied-permissions/roles:proj:developer")}, false, false, []check{
			art(libs, "org/acme/a.txt", r, false),
		}},
		// applied-permissions/admin is an admin token whatever its subject
		// (create-scoped-token): a user who is no administrator, and one
		// who does not exist at all.
		{"admin scope makes a non-admin an admin", accessapi.Subject{Name: "bob", Groups: []string{"devs"}, Scope: scope(t, "applied-permissions/admin")}, false, true, []check{
			art(ext, "x", r|w|d|m, true), art(libs, "org/acme/a.txt", r, true),
		}},
		{"admin scope makes an unknown user an admin", accessapi.Subject{Name: "ghost", Scope: scope(t, "applied-permissions/admin")}, false, true, []check{
			art(ext, "x", r|w|d|m, true), {accessapi.ResourceBuild, builds, "lib/1", false, d, true},
		}},
		{"a membership in a deleted group", accessapi.Subject{Name: "eve", Groups: []string{"ghosts", "devs"}}, false, false, []check{
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
		if dec.IsAdmin() != c.admin {
			t.Errorf("%s: admin %v, want %v", c.name, dec.IsAdmin(), c.admin)
		}
		for _, k := range c.checks {
			var got bool
			if k.folder {
				got = dec.AllowsFolder(k.rt, k.repo, k.path, k.expected)
			} else {
				got = dec.Allows(k.rt, k.repo, k.path, k.expected)
			}
			if got != k.ok {
				t.Errorf(
					"%s: %s %s %q folder=%v want %v: got %v (holds %v)",
					c.name,
					k.rt,
					k.repo.Key,
					k.path,
					k.folder,
					k.expected.AccessNames(),
					got,
					dec.Actions(k.rt, k.repo, k.path, k.folder).AccessNames(),
				)
			}
		}
	}
}

func TestUnitScopedConfigurationAndSystem(t *testing.T) {
	ctx := context.Background()
	db := world(t)
	src := Sources{Permissions: db.Permissions(), Groups: db.Groups()}
	mk := func(s string, admin bool) *Decider {
		t.Helper()
		sub := accessapi.Subject{Name: "bob", Groups: []string{"devs"}, Admin: admin}
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
		systemActions kernel.ScopeActions
	}{
		{"", false, "libs-local", true, "info", false, kernel.ScopeRead},
		{"", true, "libs-local", true, "metrics", true, kernel.ScopeRead},
		{"applied-permissions/user", false, "ext-local", true, "info", false, kernel.ScopeRead},
		{"repo:libs-local:r", false, "libs-local", true, "info", false, kernel.ScopeRead},
		{"repo:libs-local:r", false, "ext-local", false, "info", false, kernel.ScopeRead},
		{"applied-permissions/user repo:*:r", false, "ext-local", true, "info", false, kernel.ScopeRead},
		{"artifact:libs-local:r", false, "libs-local", false, "info", false, kernel.ScopeRead},
		{"system:info:r", false, "libs-local", false, "info/storage", true, kernel.ScopeRead},
		{"system:info:r", false, "libs-local", false, "info", false, kernel.ScopeWrite},
		{"system:info:r", false, "libs-local", false, "metrics", false, kernel.ScopeRead},
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

func TestUnitScopeLettersMapToActions(t *testing.T) {
	for _, c := range []struct {
		letters  string
		expected accessapi.Actions
	}{
		{"r", r},
		{"w", w},
		{"d", d},
		{"m", m},
		{"a", accessapi.ActionAnnotate},
		{"x", accessapi.ActionExecute},
		{"s", accessapi.ActionScan},
		{"*", allActions},
	} {
		sc, err := kernel.ParseScope("artifact:x:" + c.letters)
		if err != nil {
			t.Fatal(err)
		}
		if got := toActions(sc.Entries[0].Actions); got != c.expected {
			t.Errorf("%s: %v, want %v", c.letters, got.AccessNames(), c.expected.AccessNames())
		}
	}
}

var errBoom = errors.New("boom")

type failingPermissions struct{ port.PermissionStore }

func (failingPermissions) ForPrincipal(
	context.Context,
	string,
	[]string,
) ([]model.Permission, error) {
	return nil, errBoom
}

type failingGroups struct{ port.GroupStore }

func (failingGroups) Get(context.Context, string) (model.Group, error) {
	return model.Group{}, errBoom
}

func TestUnitNewReportsStoreFailures(t *testing.T) {
	ctx := context.Background()
	db := world(t)
	sub := accessapi.Subject{Name: "bob", Groups: []string{"devs"}}
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
func TestUnitWireFormsEvaluateAlike(t *testing.T) {
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
	decisions := map[string][]accessapi.Actions{}
	for name, convert := range map[string]func() (model.Permission, error){
		"v1": func() (model.Permission, error) {
			var w wire.PermissionTargetV1
			if err := json.Unmarshal([]byte(v1), &w); err != nil {
				return model.Permission{}, err
			}
			return model.PermissionFromV1(w)
		},
		"v2": func() (model.Permission, error) {
			var w wire.PermissionTargetV2
			if err := json.Unmarshal([]byte(v2), &w); err != nil {
				return model.Permission{}, err
			}
			return model.PermissionFromV2(w)
		},
		"access": func() (model.Permission, error) {
			var w wire.AccessPermission
			if err := json.Unmarshal([]byte(access), &w); err != nil {
				return model.Permission{}, err
			}
			return model.PermissionFromAccess(w)
		},
	} {
		p, err := convert()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		db := inmem.New(memtx.New())
		if err := db.Groups().Create(ctx, model.Group{Name: "devs"}); err != nil {
			t.Fatal(err)
		}
		if err := db.Permissions().Create(ctx, p); err != nil {
			t.Fatal(err)
		}
		for _, sub := range []accessapi.Subject{{Name: "bob"}, {Name: "carol", Groups: []string{"devs"}}} {
			dec, err := New(ctx, Sources{Permissions: db.Permissions(), Groups: db.Groups()}, sub)
			if err != nil {
				t.Fatal(err)
			}
			for _, k := range []struct {
				repo accessapi.Repo
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
					dec.Actions(accessapi.ResourceArtifact, k.repo, k.path, false),
				)
			}
		}
	}
	// bob: read and write on libs-local/org and the remote and its cache;
	// carol through devs: read on the same; nothing under org/secret, com
	// or in other repositories.
	want := []accessapi.Actions{r | w, 0, 0, r | w, r | w, 0, r, 0, 0, r, r, 0}
	for name, got := range decisions {
		if !slices.Equal(got, want) {
			t.Errorf("%s decides %v, want %v", name, got, want)
		}
	}
}

func TestUnitRepoVisibleAndGroups(t *testing.T) {
	ctx := context.Background()
	db := world(t)
	src := Sources{Permissions: db.Permissions(), Groups: db.Groups()}
	mk := func(s string, admin bool, groups ...string) *Decider {
		t.Helper()
		sub := accessapi.Subject{Name: "bob", Groups: groups, Admin: admin}
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
		repo    accessapi.Repo
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
		if got := mk(c.scope, c.admin, "devs").IsRepoVisible(c.repo); got != c.visible {
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

func TestUnitTargetAllows(t *testing.T) {
	tg := target([]string{"com/**"}, []string{"**/*.tmp"})
	for _, c := range []struct {
		path     string
		folder   bool
		expected bool
	}{
		{"com/a.jar", false, true},
		{"com/a.tmp", false, false},
		{"org/a.jar", false, false},
		{"", true, true},
		{"", false, false},
	} {
		if got := TargetAllows(tg, c.path, c.folder); got != c.expected {
			t.Errorf("TargetAllows(%q, folder=%v) = %v, want %v", c.path, c.folder, got, c.expected)
		}
	}
	if !TargetAllows(model.Target{}, "anything", false) {
		t.Error("an empty include list admits everything")
	}
}

func TestUnitHolds(t *testing.T) {
	ctx := context.Background()
	db := world(t)
	grants := model.Grants{
		Users:  map[string]accessapi.Actions{"bob": m},
		Groups: map[string]accessapi.Actions{"devs": r | m},
	}
	for _, c := range []struct {
		name     string
		sub      accessapi.Subject
		expected bool
	}{
		{"direct grant", accessapi.Subject{Name: "bob"}, true},
		{"through a group", accessapi.Subject{Name: "carol", Groups: []string{"devs"}}, true},
		{"no grant", accessapi.Subject{Name: "carol", Groups: []string{"readers"}}, false},
		{"a groups scope drops the user's own grant", accessapi.Subject{
			Name: "bob", Scope: scope(t, "applied-permissions/groups:readers"),
		}, false},
		{"anonymous while anonymous access is off", accessapi.Subject{Name: "bob", Anonymous: true}, false},
	} {
		dec, err := New(ctx, Sources{Permissions: db.Permissions(), Groups: db.Groups()}, c.sub)
		if err != nil {
			t.Fatal(err)
		}
		if got := dec.Holds(grants, m); got != c.expected {
			t.Errorf("%s: Holds = %v, want %v", c.name, got, c.expected)
		}
	}
}

// TestUnitGrantable checks the exclude-manage rule: a holder of MANAGE grants
// or revokes only the other actions it holds in the section, an
// administrator any, and anyone else none.
func TestUnitGrantable(t *testing.T) {
	ctx := context.Background()
	db := world(t)
	grants := model.Grants{
		Users:  map[string]accessapi.Actions{"bob": r | w | m, "dave": r | w | d},
		Groups: map[string]accessapi.Actions{"devs": r | m},
	}
	for _, c := range []struct {
		name     string
		sub      accessapi.Subject
		expected accessapi.Actions
	}{
		{"an administrator", accessapi.Subject{Name: "root", Admin: true}, allActions},
		{"an administrator through a group", accessapi.Subject{Name: "eve", Groups: []string{"admins"}}, allActions},
		{"a direct holder keeps what it holds but manage", accessapi.Subject{Name: "bob"}, r | w},
		{"a holder through a group", accessapi.Subject{Name: "carol", Groups: []string{"devs"}}, r},
		{"direct and group grants add up", accessapi.Subject{Name: "bob", Groups: []string{"devs"}}, r | w},
		{"actions without manage grant nothing", accessapi.Subject{Name: "dave"}, 0},
		{"a groups scope drops the user's own manage", accessapi.Subject{
			Name: "bob", Scope: scope(t, "applied-permissions/groups:readers"),
		}, 0},
		{"an administrator's token without applied permissions", accessapi.Subject{
			Name: "root", Admin: true, Scope: scope(t, "artifact:libs-local:*"),
		}, 0},
	} {
		dec, err := New(ctx, Sources{Permissions: db.Permissions(), Groups: db.Groups()}, c.sub)
		if err != nil {
			t.Fatal(err)
		}
		if got := dec.Grantable(grants); got != c.expected {
			t.Errorf(
				"%s: Grantable = %v, want %v",
				c.name,
				got.AccessNames(),
				c.expected.AccessNames(),
			)
		}
	}
}

// TestUnitSelf checks that Self names who the subject is, which a token scope
// does not narrow, unlike the identities it acts through.
func TestUnitSelf(t *testing.T) {
	ctx := context.Background()
	db := world(t)
	for _, c := range []struct {
		name   string
		sub    accessapi.Subject
		user   string
		groups []string
	}{
		{"a password caller", accessapi.Subject{Name: "bob", Groups: []string{"devs"}}, "bob", []string{"devs"}},
		{"a groups scope keeps the user and every group", accessapi.Subject{
			Name: "bob", Groups: []string{"devs", "readers"},
			Scope: scope(t, "applied-permissions/groups:readers"),
		}, "bob", []string{"devs", "readers"}},
		{"anonymous while anonymous access is off", accessapi.Subject{Name: "anonymous", Anonymous: true}, "", nil},
	} {
		dec, err := New(ctx, Sources{Permissions: db.Permissions(), Groups: db.Groups()}, c.sub)
		if err != nil {
			t.Fatal(err)
		}
		user, groups := dec.Self()
		if user != c.user || !slices.Equal(groups, c.groups) {
			t.Errorf("%s: Self = %q, %v, want %q, %v", c.name, user, groups, c.user, c.groups)
		}
		if c.sub.Scope != nil && slices.Equal(dec.Groups(), groups) {
			t.Errorf("%s: the scope did not narrow the groups acted through", c.name)
		}
	}
}

// failingUsers fails every read.
type failingUsers struct{ port.UserStore }

func (failingUsers) Get(context.Context, string) (model.User, error) {
	return model.User{}, errBoom
}

// TestUnitManagesResources checks the Manage Resources role follows the
// identities the subject acts as: the user's flag while a scope keeps the
// user, a group's while a scope keeps the group, and nothing under a scope
// of resource entries alone.
func TestUnitManagesResources(t *testing.T) {
	ctx := context.Background()
	db := world(t)
	for _, g := range []model.Group{
		{Name: "resmgrs", Roles: model.RoleFlags{ResourcesManager: true}},
	} {
		if err := db.Groups().Create(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []model.User{
		{Name: "rita", Roles: model.RoleFlags{ResourcesManager: true}},
		{Name: "gil", Groups: []string{"resmgrs", "devs"}},
		{Name: "bob", Groups: []string{"devs"}},
	} {
		if err := db.Users().Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	src := Sources{Permissions: db.Permissions(), Groups: db.Groups()}
	for _, c := range []struct {
		name     string
		sub      accessapi.Subject
		expected bool
	}{
		{"the user's flag", accessapi.Subject{Name: "rita"}, true},
		{"a group's flag", accessapi.Subject{Name: "gil", Groups: []string{"resmgrs", "devs"}}, true},
		{"neither", accessapi.Subject{Name: "bob", Groups: []string{"devs"}}, false},
		{"a user scope keeps the user's", accessapi.Subject{
			Name: "rita", Scope: scope(t, "applied-permissions/user"),
		}, true},
		{"a groups scope drops the user's", accessapi.Subject{
			Name: "rita", Groups: []string{"devs"}, Scope: scope(t, "applied-permissions/groups:devs"),
		}, false},
		{"a groups scope keeps a named group's", accessapi.Subject{
			Name:   "gil",
			Groups: []string{"resmgrs", "devs"},
			Scope:  scope(t, "applied-permissions/groups:resmgrs"),
		}, true},
		{"a groups scope drops an unnamed group's", accessapi.Subject{
			Name: "gil", Groups: []string{"resmgrs", "devs"}, Scope: scope(t, "applied-permissions/groups:devs"),
		}, false},
		{"resource entries keep no role", accessapi.Subject{
			Name: "rita", Scope: scope(t, "artifact:libs-local:r"),
		}, false},
		{"a system scope keeps no role", accessapi.Subject{
			Name: "rita", Scope: scope(t, "system:permissions:r"),
		}, false},
		{"a user that is not stored", accessapi.Subject{Name: "transient"}, false},
		{"a group that is gone", accessapi.Subject{Name: "bob", Groups: []string{"gone"}}, false},
	} {
		d, err := New(ctx, src, c.sub)
		if err != nil {
			t.Fatal(err)
		}
		got, err := d.ManagesResources(ctx, db.Users(), db.Groups())
		if err != nil || got != c.expected {
			t.Errorf("%s: %v, %v; want %v", c.name, got, err, c.expected)
		}
	}
	d, err := New(ctx, src, accessapi.Subject{Name: "bob", Groups: []string{"devs"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ManagesResources(ctx, failingUsers{}, db.Groups()); !errors.Is(err, errBoom) {
		t.Errorf("users failure: %v", err)
	}
	if _, err := d.ManagesResources(ctx, db.Users(), failingGroups{}); !errors.Is(err, errBoom) {
		t.Errorf("groups failure: %v", err)
	}
}
