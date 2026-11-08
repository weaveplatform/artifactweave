package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/platform/testkit/storekit"
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
	storekit.WantErr(t, ss.Get(ctx, types.SettingUserLockPolicy, &lock), store.ErrNotFound)

	storekit.Must(
		t,
		ss.Put(
			ctx,
			types.SettingUserLockPolicy,
			types.UserLockPolicy{Enabled: true, LoginAttempts: 5},
		),
	)
	storekit.Must(
		t,
		ss.Put(
			ctx,
			types.SettingTrashcan,
			types.TrashcanSettings{Enabled: true, RetentionDays: 14},
		),
	)
	storekit.Must(t, ss.Put(ctx, types.SettingBasicAuthEnabled, true))
	storekit.Must(t, ss.Get(ctx, types.SettingUserLockPolicy, &lock))
	storekit.Same(t, "Get", types.UserLockPolicy{Enabled: true, LoginAttempts: 5}, lock)

	storekit.Must(t, ss.Put(ctx, types.SettingTrashcan, types.TrashcanSettings{RetentionDays: 7}))
	var trash types.TrashcanSettings
	storekit.Must(t, ss.Get(ctx, types.SettingTrashcan, &trash))
	storekit.Same(t, "replaced", types.TrashcanSettings{RetentionDays: 7}, trash)

	if err := ss.Put(ctx, "bad", make(chan int)); err == nil {
		t.Fatal("Put of a value JSON cannot encode succeeded")
	}
	var n int
	if err := ss.Get(ctx, types.SettingTrashcan, &n); err == nil {
		t.Fatal("Get into a mismatched type succeeded")
	}
	storekit.Same(
		t,
		"Keys",
		[]string{types.SettingBasicAuthEnabled, types.SettingUserLockPolicy, types.SettingTrashcan},
		storekit.Get[[]string](t)(ss.Keys(ctx)),
	)

	storekit.Must(t, ss.Delete(ctx, types.SettingTrashcan))
	storekit.WantErr(t, ss.Delete(ctx, types.SettingTrashcan), store.ErrNotFound)
	storekit.WantErr(t, ss.Get(ctx, types.SettingTrashcan, &trash), store.ErrNotFound)
	storekit.Same(
		t,
		"Keys after delete",
		[]string{types.SettingBasicAuthEnabled, types.SettingUserLockPolicy},
		storekit.Get[[]string](t)(ss.Keys(ctx)),
	)
}

func testLeases(t *testing.T, s store.Store) {
	ctx := context.Background()
	ls := s.Leases()
	acquire := func(l types.Lease, now time.Time) bool {
		t.Helper()
		return storekit.Get[bool](t)(ls.Acquire(ctx, l, now))
	}
	_, err := ls.Acquire(ctx, types.Lease{Name: "gc", Expires: storekit.At(10)}, storekit.At(0))
	storekit.WantErr(t, err, store.ErrInvalid)
	_, err = ls.Acquire(ctx, types.Lease{Name: "gc", Holder: "n1"}, storekit.At(0))
	storekit.WantErr(t, err, store.ErrInvalid)

	n1 := types.Lease{
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
		types.Lease{Name: "gc", Holder: "n2", Acquired: storekit.At(5), Expires: storekit.At(15)},
		storekit.At(5),
	) {
		t.Fatal("a held lease was taken by another holder")
	}
	renewed := n1
	renewed.Expires = storekit.At(20)
	if !acquire(renewed, storekit.At(5)) {
		t.Fatal("the holder could not renew")
	}
	other := types.Lease{
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
		[]types.Lease{other, renewed},
		storekit.Get[[]types.Lease](t)(ls.List(ctx)),
	)

	// A lease is free once now reaches its expiry.
	n2 := types.Lease{Name: "gc", Holder: "n2", Acquired: storekit.At(20), Expires: storekit.At(40)}
	if !acquire(n2, storekit.At(20)) {
		t.Fatal("an expired lease was not taken over")
	}
	storekit.Must(t, ls.Release(ctx, "gc", "n1"))
	storekit.Same(
		t,
		"release by a non-holder",
		[]types.Lease{other, n2},
		storekit.Get[[]types.Lease](t)(ls.List(ctx)),
	)
	storekit.Must(t, ls.Release(ctx, "gc", "n2"))
	storekit.Must(t, ls.Release(ctx, "gc", "n2"))
	storekit.Same(
		t,
		"after release",
		[]types.Lease{other},
		storekit.Get[[]types.Lease](t)(ls.List(ctx)),
	)
}

func testConfigs(t *testing.T, s store.Store) {
	ctx := context.Background()
	cs := s.Configs()
	storekit.WantErr(t, cs.Put(ctx, types.ConfigDocument{Key: "x"}), store.ErrInvalid)
	storekit.WantErr(t, cs.Put(ctx, types.ConfigDocument{Kind: types.ConfigOIDC}), store.ErrInvalid)

	github := types.ConfigDocument{
		Kind:    types.ConfigOIDC,
		Key:     "github",
		Enabled: true,
		Document: []byte(
			`{"issuer_url":"https://token.actions.githubusercontent.com","provider_type":"GitHub"}`,
		),
		Secrets: map[string]string{"client_secret": "cs", "unset": ""},
		Updated: storekit.At(1),
	}
	azure := types.ConfigDocument{
		Kind:     types.ConfigOIDC,
		Key:      "azure",
		Document: []byte(`{}`),
		Updated:  storekit.At(2),
	}
	corp := types.ConfigDocument{
		Kind:     types.ConfigLDAP,
		Key:      "corp",
		Enabled:  true,
		Document: []byte(`{"ldap_url":"ldaps://corp"}`),
		Secrets:  map[string]string{"manager_password": "mp"},
	}
	for _, d := range []types.ConfigDocument{github, azure, corp} {
		storekit.Must(t, cs.Put(ctx, d))
	}
	// An empty secret is not stored.
	github.Secrets = map[string]string{"client_secret": "cs"}
	storekit.Same(
		t,
		"Get",
		github,
		storekit.Get[types.ConfigDocument](t)(cs.Get(ctx, types.ConfigOIDC, "github")),
	)
	_, err := cs.Get(ctx, types.ConfigLDAP, "github")
	storekit.WantErr(t, err, store.ErrNotFound)

	storekit.Same(t, "List oidc", []types.ConfigDocument{azure, github},
		storekit.Get[[]types.ConfigDocument](t)(cs.List(ctx, types.ConfigOIDC)))
	storekit.Same(
		t,
		"List ldap",
		[]types.ConfigDocument{corp},
		storekit.Get[[]types.ConfigDocument](t)(cs.List(ctx, types.ConfigLDAP)),
	)
	if got := storekit.Get[[]types.ConfigDocument](
		t,
	)(
		cs.List(ctx, types.ConfigSAML),
	); len(
		got,
	) != 0 {
		t.Fatalf("List saml = %v", got)
	}

	got := storekit.Get[types.ConfigDocument](t)(cs.Get(ctx, types.ConfigOIDC, "github"))
	got.Secrets["client_secret"] = "mutated"
	got.Document[0] = '['
	storekit.Same(
		t,
		"Get is a copy",
		github,
		storekit.Get[types.ConfigDocument](t)(cs.Get(ctx, types.ConfigOIDC, "github")),
	)

	github.Secrets, github.Enabled = nil, false
	storekit.Must(t, cs.Put(ctx, github))
	storekit.Same(
		t,
		"replaced",
		github,
		storekit.Get[types.ConfigDocument](t)(cs.Get(ctx, types.ConfigOIDC, "github")),
	)

	storekit.Must(t, cs.Delete(ctx, types.ConfigLDAP, "corp"))
	storekit.WantErr(t, cs.Delete(ctx, types.ConfigLDAP, "corp"), store.ErrNotFound)
	_, err = cs.Get(ctx, types.ConfigLDAP, "corp")
	storekit.WantErr(t, err, store.ErrNotFound)
	// Re-creating the key starts without the deleted document's secrets.
	storekit.Must(t, cs.Put(ctx, types.ConfigDocument{Kind: types.ConfigLDAP, Key: "corp"}))
	storekit.Same(t, "re-created", types.ConfigDocument{Kind: types.ConfigLDAP, Key: "corp"},
		storekit.Get[types.ConfigDocument](t)(cs.Get(ctx, types.ConfigLDAP, "corp")))
}
