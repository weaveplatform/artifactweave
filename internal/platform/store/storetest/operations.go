package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// lockPolicy and trashcan have the JSON form of two settings that modules
// keep, access's user lock policy and the artifacts trash can. The suite
// declares its own because the platform imports no module, and the
// settings store keeps any value that JSON can encode.
type (
	lockPolicy struct {
		Enabled       bool `json:"enabled"`
		LoginAttempts int  `json:"login_attempts"`
	}
	trashcan struct {
		Enabled       bool `json:"enabled"`
		RetentionDays int  `json:"retention_days"`
	}
)

// runPlatformOperations covers settings, leases and configuration documents.
func runPlatformOperations(t *testing.T, newStore func(*testing.T) Store) {
	t.Run("Settings", func(t *testing.T) { testSettings(t, newStore(t)) })
	t.Run("Leases", func(t *testing.T) { testLeases(t, newStore(t)) })
	t.Run("Configs", func(t *testing.T) { testConfigs(t, newStore(t)) })
}

func testSettings(t *testing.T, s Store) {
	ctx := context.Background()
	ss := s.Settings()
	var lock lockPolicy
	storekit.WantErr(t, ss.Get(ctx, settings.SettingUserLockPolicy, &lock), kernel.ErrNotFound)

	storekit.Must(
		t,
		ss.Put(
			ctx,
			settings.SettingUserLockPolicy,
			lockPolicy{Enabled: true, LoginAttempts: 5},
		),
	)
	storekit.Must(
		t,
		ss.Put(
			ctx,
			settings.SettingTrashcan,
			trashcan{Enabled: true, RetentionDays: 14},
		),
	)
	storekit.Must(t, ss.Put(ctx, settings.SettingBasicAuthEnabled, true))
	storekit.Must(t, ss.Get(ctx, settings.SettingUserLockPolicy, &lock))
	storekit.Same(t, "Get", lockPolicy{Enabled: true, LoginAttempts: 5}, lock)

	storekit.Must(t, ss.Put(ctx, settings.SettingTrashcan, trashcan{RetentionDays: 7}))
	var trash trashcan
	storekit.Must(t, ss.Get(ctx, settings.SettingTrashcan, &trash))
	storekit.Same(t, "replaced", trashcan{RetentionDays: 7}, trash)

	if err := ss.Put(ctx, "bad", make(chan int)); err == nil {
		t.Fatal("Put of a value JSON cannot encode succeeded")
	}
	var n int
	if err := ss.Get(ctx, settings.SettingTrashcan, &n); err == nil {
		t.Fatal("Get into a mismatched type succeeded")
	}
	storekit.Same(
		t,
		"Keys",
		[]string{
			settings.SettingBasicAuthEnabled,
			settings.SettingUserLockPolicy,
			settings.SettingTrashcan,
		},
		storekit.Get[[]string](t)(ss.Keys(ctx)),
	)

	storekit.Must(t, ss.Delete(ctx, settings.SettingTrashcan))
	storekit.WantErr(t, ss.Delete(ctx, settings.SettingTrashcan), kernel.ErrNotFound)
	storekit.WantErr(t, ss.Get(ctx, settings.SettingTrashcan, &trash), kernel.ErrNotFound)
	storekit.Same(
		t,
		"Keys after delete",
		[]string{settings.SettingBasicAuthEnabled, settings.SettingUserLockPolicy},
		storekit.Get[[]string](t)(ss.Keys(ctx)),
	)
}

func testLeases(t *testing.T, s Store) {
	ctx := context.Background()
	ls := s.Leases()
	acquire := func(l work.Lease, now time.Time) bool {
		t.Helper()
		return storekit.Get[bool](t)(ls.Acquire(ctx, l, now))
	}
	_, err := ls.Acquire(ctx, work.Lease{Name: "gc", Expires: storekit.At(10)}, storekit.At(0))
	storekit.WantErr(t, err, kernel.ErrInvalid)
	_, err = ls.Acquire(ctx, work.Lease{Name: "gc", Holder: "n1"}, storekit.At(0))
	storekit.WantErr(t, err, kernel.ErrInvalid)

	n1 := work.Lease{
		Name:     "gc",
		Holder:   "n1",
		Task:     "garbage collection",
		Acquired: storekit.At(0),
		Expires:  storekit.At(10),
	}
	if !acquire(n1, storekit.At(0)) {
		t.Fatal("a free lease was not taken")
	}
	if acquire(
		work.Lease{Name: "gc", Holder: "n2", Acquired: storekit.At(5), Expires: storekit.At(15)},
		storekit.At(5),
	) {
		t.Fatal("a held lease was taken by another holder")
	}
	renewed := n1
	renewed.Expires = storekit.At(20)
	if !acquire(renewed, storekit.At(5)) {
		t.Fatal("the holder could not renew")
	}
	other := work.Lease{
		Name:     "cleanup",
		Holder:   "n3",
		Acquired: storekit.At(1),
		Expires:  storekit.At(30),
	}
	if !acquire(other, storekit.At(1)) {
		t.Fatal("a second lease was not taken")
	}
	storekit.Same(
		t,
		"List",
		[]work.Lease{other, renewed},
		storekit.Get[[]work.Lease](t)(ls.List(ctx)),
	)

	// A lease is free once now reaches its expiry.
	n2 := work.Lease{Name: "gc", Holder: "n2", Acquired: storekit.At(20), Expires: storekit.At(40)}
	if !acquire(n2, storekit.At(20)) {
		t.Fatal("an expired lease was not taken over")
	}
	storekit.Must(t, ls.Release(ctx, "gc", "n1"))
	storekit.Same(
		t,
		"release by a non-holder",
		[]work.Lease{other, n2},
		storekit.Get[[]work.Lease](t)(ls.List(ctx)),
	)
	storekit.Must(t, ls.Release(ctx, "gc", "n2"))
	storekit.Must(t, ls.Release(ctx, "gc", "n2"))
	storekit.Same(
		t,
		"after release",
		[]work.Lease{other},
		storekit.Get[[]work.Lease](t)(ls.List(ctx)),
	)
}

func testConfigs(t *testing.T, s Store) {
	ctx := context.Background()
	cs := s.Configs()
	storekit.WantErr(t, cs.Put(ctx, configdoc.ConfigDocument{Key: "x"}), kernel.ErrInvalid)
	storekit.WantErr(
		t,
		cs.Put(ctx, configdoc.ConfigDocument{Kind: configdoc.ConfigOIDC}),
		kernel.ErrInvalid,
	)

	github := configdoc.ConfigDocument{
		Kind:    configdoc.ConfigOIDC,
		Key:     "github",
		Enabled: true,
		Document: []byte(
			`{"issuer_url":"https://token.actions.githubusercontent.com","provider_type":"GitHub"}`,
		),
		Secrets: map[string]string{"client_secret": "cs", "unset": ""},
		Updated: storekit.At(1),
	}
	azure := configdoc.ConfigDocument{
		Kind:     configdoc.ConfigOIDC,
		Key:      "azure",
		Document: []byte(`{}`),
		Updated:  storekit.At(2),
	}
	corp := configdoc.ConfigDocument{
		Kind:     configdoc.ConfigLDAP,
		Key:      "corp",
		Enabled:  true,
		Document: []byte(`{"ldap_url":"ldaps://corp"}`),
		Secrets:  map[string]string{"manager_password": "mp"},
	}
	for _, d := range []configdoc.ConfigDocument{github, azure, corp} {
		storekit.Must(t, cs.Put(ctx, d))
	}
	// An empty secret is not stored.
	github.Secrets = map[string]string{"client_secret": "cs"}
	storekit.Same(
		t,
		"Get",
		github,
		storekit.Get[configdoc.ConfigDocument](t)(cs.Get(ctx, configdoc.ConfigOIDC, "github")),
	)
	_, err := cs.Get(ctx, configdoc.ConfigLDAP, "github")
	storekit.WantErr(t, err, kernel.ErrNotFound)

	storekit.Same(t, "List oidc", []configdoc.ConfigDocument{azure, github},
		storekit.Get[[]configdoc.ConfigDocument](t)(cs.List(ctx, configdoc.ConfigOIDC)))
	storekit.Same(
		t,
		"List ldap",
		[]configdoc.ConfigDocument{corp},
		storekit.Get[[]configdoc.ConfigDocument](t)(cs.List(ctx, configdoc.ConfigLDAP)),
	)
	if got := storekit.Get[[]configdoc.ConfigDocument](
		t,
	)(
		cs.List(ctx, configdoc.ConfigSAML),
	); len(
		got,
	) != 0 {
		t.Fatalf("List saml = %v", got)
	}

	got := storekit.Get[configdoc.ConfigDocument](t)(cs.Get(ctx, configdoc.ConfigOIDC, "github"))
	got.Secrets["client_secret"] = "mutated"
	got.Document[0] = '['
	storekit.Same(
		t,
		"Get is a copy",
		github,
		storekit.Get[configdoc.ConfigDocument](t)(cs.Get(ctx, configdoc.ConfigOIDC, "github")),
	)

	github.Secrets, github.Enabled = nil, false
	storekit.Must(t, cs.Put(ctx, github))
	storekit.Same(
		t,
		"replaced",
		github,
		storekit.Get[configdoc.ConfigDocument](t)(cs.Get(ctx, configdoc.ConfigOIDC, "github")),
	)

	storekit.Must(t, cs.Delete(ctx, configdoc.ConfigLDAP, "corp"))
	storekit.WantErr(t, cs.Delete(ctx, configdoc.ConfigLDAP, "corp"), kernel.ErrNotFound)
	_, err = cs.Get(ctx, configdoc.ConfigLDAP, "corp")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	// Re-creating the key starts without the deleted document's secrets.
	storekit.Must(t, cs.Put(ctx, configdoc.ConfigDocument{Kind: configdoc.ConfigLDAP, Key: "corp"}))
	storekit.Same(
		t,
		"re-created",
		configdoc.ConfigDocument{Kind: configdoc.ConfigLDAP, Key: "corp"},
		storekit.Get[configdoc.ConfigDocument](t)(cs.Get(ctx, configdoc.ConfigLDAP, "corp")),
	)
}

// runConfigWatches covers configdoc.Watch. Only Run registers it: each
// case needs a store whose only watches are the case's own, which a
// composite store, with the watches it registers itself, cannot give.
func runConfigWatches(t *testing.T, newStore Factory) {
	t.Run("ConfigWatches", func(t *testing.T) { testConfigWatches(t, newStore) })
}

// errWatch is the error a watch returns to make its write fail.
var errWatch = errors.New("watch failed")

func testConfigWatches(t *testing.T, newStore Factory) {
	ctx := context.Background()
	var (
		calls []string
		fail  error
	)
	watch := func(kind configdoc.ConfigKind, name string) configdoc.Watch {
		return configdoc.Watch{Kind: kind, Changed: func(_ context.Context, key string) error {
			calls = append(calls, name+" "+key)
			return fail
		}}
	}
	cs := newStore(t,
		watch(configdoc.ConfigLayout, "first"),
		watch(configdoc.ConfigProxy, "proxy"),
		watch(configdoc.ConfigLayout, "second"),
	).Configs()
	called := func(what string, want ...string) {
		t.Helper()
		storekit.Same(t, what, want, calls)
		calls = nil
	}
	read := func(key string) configdoc.ConfigDocument {
		t.Helper()
		return storekit.Get[configdoc.ConfigDocument](t)(cs.Get(ctx, configdoc.ConfigLayout, key))
	}

	maven := configdoc.ConfigDocument{
		Kind:     configdoc.ConfigLayout,
		Key:      "maven",
		Document: []byte(`{"pattern":"a"}`),
		Updated:  storekit.At(1),
	}
	storekit.Must(t, cs.Put(ctx, maven))
	called("create", "first maven", "second maven")
	// Enabled, Updated and Secrets are not part of the definition.
	maven.Enabled, maven.Updated, maven.Secrets = true, storekit.At(2), map[string]string{"s": "v"}
	storekit.Must(t, cs.Put(ctx, maven))
	called("same bytes")
	maven.Document = []byte(`{"pattern":"b"}`)
	storekit.Must(t, cs.Put(ctx, maven))
	called("changed bytes", "first maven", "second maven")
	corp := configdoc.ConfigDocument{
		Kind:     configdoc.ConfigLDAP,
		Key:      "corp",
		Document: []byte(`{}`),
	}
	storekit.Must(t, cs.Put(ctx, corp))
	storekit.Must(t, cs.Delete(ctx, configdoc.ConfigLDAP, "corp"))
	called("another kind")

	// A watch's error fails the write, rolls it back and stops the watches
	// after it.
	fail = errWatch
	changed := maven
	changed.Document = []byte(`{"pattern":"c"}`)
	storekit.WantErr(t, cs.Put(ctx, changed), fail)
	called("failed put", "first maven")
	storekit.Same(t, "after a failed put", maven, read("maven"))
	npm := configdoc.ConfigDocument{Kind: configdoc.ConfigLayout, Key: "npm"}
	storekit.WantErr(t, cs.Put(ctx, npm), fail)
	called("failed create", "first npm")
	_, err := cs.Get(ctx, configdoc.ConfigLayout, "npm")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.WantErr(t, cs.Delete(ctx, configdoc.ConfigLayout, "maven"), fail)
	called("failed delete", "first maven")
	storekit.Same(t, "after a failed delete", maven, read("maven"))

	fail = nil
	storekit.Must(t, cs.Delete(ctx, configdoc.ConfigLayout, "maven"))
	called("delete", "first maven", "second maven")
}
