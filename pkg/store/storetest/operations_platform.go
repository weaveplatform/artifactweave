package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// runPlatformOperations covers settings, leases and configuration documents.
func runPlatformOperations(t *testing.T, newStore Factory) {
	t.Run("Settings", func(t *testing.T) { testSettings(t, newStore(t)) })
	t.Run("Leases", func(t *testing.T) { testLeases(t, newStore(t)) })
	t.Run("Configs", func(t *testing.T) { testConfigs(t, newStore(t)) })
}

func testSettings(t *testing.T, s store.Store) {
	ctx := context.Background()
	ss := s.Settings()
	var lock types.UserLockPolicy
	wantErr(t, ss.Get(ctx, types.SettingUserLockPolicy, &lock), store.ErrNotFound)

	must(
		t,
		ss.Put(
			ctx,
			types.SettingUserLockPolicy,
			types.UserLockPolicy{Enabled: true, LoginAttempts: 5},
		),
	)
	must(
		t,
		ss.Put(
			ctx,
			types.SettingTrashcan,
			types.TrashcanSettings{Enabled: true, RetentionDays: 14},
		),
	)
	must(t, ss.Put(ctx, types.SettingBasicAuthEnabled, true))
	must(t, ss.Get(ctx, types.SettingUserLockPolicy, &lock))
	same(t, "Get", types.UserLockPolicy{Enabled: true, LoginAttempts: 5}, lock)

	must(t, ss.Put(ctx, types.SettingTrashcan, types.TrashcanSettings{RetentionDays: 7}))
	var trash types.TrashcanSettings
	must(t, ss.Get(ctx, types.SettingTrashcan, &trash))
	same(t, "replaced", types.TrashcanSettings{RetentionDays: 7}, trash)

	if err := ss.Put(ctx, "bad", make(chan int)); err == nil {
		t.Fatal("Put of a value JSON cannot encode succeeded")
	}
	var n int
	if err := ss.Get(ctx, types.SettingTrashcan, &n); err == nil {
		t.Fatal("Get into a mismatched type succeeded")
	}
	same(
		t,
		"Keys",
		[]string{types.SettingBasicAuthEnabled, types.SettingUserLockPolicy, types.SettingTrashcan},
		get[[]string](t)(ss.Keys(ctx)),
	)

	must(t, ss.Delete(ctx, types.SettingTrashcan))
	wantErr(t, ss.Delete(ctx, types.SettingTrashcan), store.ErrNotFound)
	wantErr(t, ss.Get(ctx, types.SettingTrashcan, &trash), store.ErrNotFound)
	same(
		t,
		"Keys after delete",
		[]string{types.SettingBasicAuthEnabled, types.SettingUserLockPolicy},
		get[[]string](t)(ss.Keys(ctx)),
	)
}

func testLeases(t *testing.T, s store.Store) {
	ctx := context.Background()
	ls := s.Leases()
	acquire := func(l types.Lease, now time.Time) bool {
		t.Helper()
		return get[bool](t)(ls.Acquire(ctx, l, now))
	}
	_, err := ls.Acquire(ctx, types.Lease{Name: "gc", Expires: at(10)}, at(0))
	wantErr(t, err, store.ErrInvalid)
	_, err = ls.Acquire(ctx, types.Lease{Name: "gc", Holder: "n1"}, at(0))
	wantErr(t, err, store.ErrInvalid)

	n1 := types.Lease{
		Name:     "gc",
		Holder:   "n1",
		Task:     "garbage collection",
		Acquired: at(0),
		Expires:  at(10),
	}
	if !acquire(n1, at(0)) {
		t.Fatal("a free lease was not taken")
	}
	if acquire(types.Lease{Name: "gc", Holder: "n2", Acquired: at(5), Expires: at(15)}, at(5)) {
		t.Fatal("a held lease was taken by another holder")
	}
	renewed := n1
	renewed.Expires = at(20)
	if !acquire(renewed, at(5)) {
		t.Fatal("the holder could not renew")
	}
	other := types.Lease{Name: "cleanup", Holder: "n3", Acquired: at(1), Expires: at(30)}
	if !acquire(other, at(1)) {
		t.Fatal("a second lease was not taken")
	}
	same(t, "List", []types.Lease{other, renewed}, get[[]types.Lease](t)(ls.List(ctx)))

	// A lease is free once now reaches its expiry.
	n2 := types.Lease{Name: "gc", Holder: "n2", Acquired: at(20), Expires: at(40)}
	if !acquire(n2, at(20)) {
		t.Fatal("an expired lease was not taken over")
	}
	must(t, ls.Release(ctx, "gc", "n1"))
	same(
		t,
		"release by a non-holder",
		[]types.Lease{other, n2},
		get[[]types.Lease](t)(ls.List(ctx)),
	)
	must(t, ls.Release(ctx, "gc", "n2"))
	must(t, ls.Release(ctx, "gc", "n2"))
	same(t, "after release", []types.Lease{other}, get[[]types.Lease](t)(ls.List(ctx)))
}

func testConfigs(t *testing.T, s store.Store) {
	ctx := context.Background()
	cs := s.Configs()
	wantErr(t, cs.Put(ctx, types.ConfigDocument{Key: "x"}), store.ErrInvalid)
	wantErr(t, cs.Put(ctx, types.ConfigDocument{Kind: types.ConfigOIDC}), store.ErrInvalid)

	github := types.ConfigDocument{
		Kind:    types.ConfigOIDC,
		Key:     "github",
		Enabled: true,
		Document: []byte(
			`{"issuer_url":"https://token.actions.githubusercontent.com","provider_type":"GitHub"}`,
		),
		Secrets: map[string]string{"client_secret": "cs", "unset": ""},
		Updated: at(1),
	}
	azure := types.ConfigDocument{
		Kind:     types.ConfigOIDC,
		Key:      "azure",
		Document: []byte(`{}`),
		Updated:  at(2),
	}
	corp := types.ConfigDocument{
		Kind:     types.ConfigLDAP,
		Key:      "corp",
		Enabled:  true,
		Document: []byte(`{"ldap_url":"ldaps://corp"}`),
		Secrets:  map[string]string{"manager_password": "mp"},
	}
	for _, d := range []types.ConfigDocument{github, azure, corp} {
		must(t, cs.Put(ctx, d))
	}
	// An empty secret is not stored.
	github.Secrets = map[string]string{"client_secret": "cs"}
	same(t, "Get", github, get[types.ConfigDocument](t)(cs.Get(ctx, types.ConfigOIDC, "github")))
	_, err := cs.Get(ctx, types.ConfigLDAP, "github")
	wantErr(t, err, store.ErrNotFound)

	same(t, "List oidc", []types.ConfigDocument{azure, github},
		get[[]types.ConfigDocument](t)(cs.List(ctx, types.ConfigOIDC)))
	same(
		t,
		"List ldap",
		[]types.ConfigDocument{corp},
		get[[]types.ConfigDocument](t)(cs.List(ctx, types.ConfigLDAP)),
	)
	if got := get[[]types.ConfigDocument](t)(cs.List(ctx, types.ConfigSAML)); len(got) != 0 {
		t.Fatalf("List saml = %v", got)
	}

	got := get[types.ConfigDocument](t)(cs.Get(ctx, types.ConfigOIDC, "github"))
	got.Secrets["client_secret"] = "mutated"
	got.Document[0] = '['
	same(
		t,
		"Get is a copy",
		github,
		get[types.ConfigDocument](t)(cs.Get(ctx, types.ConfigOIDC, "github")),
	)

	github.Secrets, github.Enabled = nil, false
	must(t, cs.Put(ctx, github))
	same(
		t,
		"replaced",
		github,
		get[types.ConfigDocument](t)(cs.Get(ctx, types.ConfigOIDC, "github")),
	)

	must(t, cs.Delete(ctx, types.ConfigLDAP, "corp"))
	wantErr(t, cs.Delete(ctx, types.ConfigLDAP, "corp"), store.ErrNotFound)
	_, err = cs.Get(ctx, types.ConfigLDAP, "corp")
	wantErr(t, err, store.ErrNotFound)
	// Re-creating the key starts without the deleted document's secrets.
	must(t, cs.Put(ctx, types.ConfigDocument{Kind: types.ConfigLDAP, Key: "corp"}))
	same(t, "re-created", types.ConfigDocument{Kind: types.ConfigLDAP, Key: "corp"},
		get[types.ConfigDocument](t)(cs.Get(ctx, types.ConfigLDAP, "corp")))
}
