package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/sqlstore/postgres"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqltest"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var upgradeTime = seedTime.Add(24 * time.Hour)

// v1AnyRemote is Any Remote as seed version 1 wrote it: anonymous with read
// and deploy/cache on every remote repository.
func v1AnyRemote() types.Permission {
	return types.Permission{
		Name: AnyRemotePermission,
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{
				AnonymousUser: types.ActionRead | types.ActionWrite,
			}},
			Targets: map[string]types.Target{
				types.TargetAnyRemote: {Include: []string{"**"}, Exclude: []string{}},
			},
		}},
		Created: seedTime, Updated: seedTime,
	}
}

// seedV1 leaves st as seed version 1 left it.
func seedV1(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := Seed(ctx, st, "pw", seedTime); err != nil {
		t.Fatal(err)
	}
	if err := st.Permissions().Update(ctx, v1AnyRemote()); err != nil {
		t.Fatal(err)
	}
	if err := st.Settings().
		Put(ctx, types.SettingSeeded, seedMarker{Version: 1, At: seedTime}); err != nil {
		t.Fatal(err)
	}
}

func anonymousOnAnyRemote(t *testing.T, st store.Store) types.Actions {
	t.Helper()
	p, err := st.Permissions().Get(context.Background(), AnyRemotePermission)
	if err != nil {
		t.Fatal(err)
	}
	return p.Resources[types.ResourceArtifact].Actions.Users[AnonymousUser]
}

func markerVersion(t *testing.T, st store.Store) seedMarker {
	t.Helper()
	var m seedMarker
	if err := st.Settings().Get(context.Background(), types.SettingSeeded, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// upgradeV1 upgrades a store version 1 seeded, checks Any Remote now grants
// nothing, and that a second upgrade changes nothing.
func upgradeV1(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	seedV1(t, st)
	if removed, err := UpgradeSeed(ctx, st, upgradeTime); !removed || err != nil {
		t.Fatalf("UpgradeSeed = %v, %v", removed, err)
	}
	// The whole seed now reads as this version writes it.
	checkSeeded(t, st, "pw")
	p, err := st.Permissions().Get(ctx, AnyRemotePermission)
	if err != nil || !p.Updated.Equal(upgradeTime) || !p.Created.Equal(seedTime) {
		t.Fatalf("Any Remote = %+v, %v", p, err)
	}
	// The marker keeps when the store was first seeded.
	if m := markerVersion(t, st); !m.At.Equal(seedTime) {
		t.Fatalf("marker = %+v", m)
	}
	if removed, err := UpgradeSeed(ctx, st, upgradeTime.Add(time.Hour)); removed || err != nil {
		t.Fatalf("second UpgradeSeed = %v, %v", removed, err)
	}
	if p, _ := st.Permissions().Get(ctx, AnyRemotePermission); !p.Updated.Equal(upgradeTime) {
		t.Fatalf("the second upgrade rewrote Any Remote: %+v", p)
	}
}

func TestUpgradeSeedInMemory(t *testing.T) { upgradeV1(t, inmem.New()) }

func TestUpgradeSeedSQLite(t *testing.T) {
	st, err := sqlite.Open(
		context.Background(),
		filepath.Join(t.TempDir(), "aw.db"),
		sqlite.Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	upgradeV1(t, st)
}

// TestUpgradeSeedPostgres needs Docker and skips without it; CI runs it on
// Linux.
func TestUpgradeSeedPostgres(t *testing.T) {
	dsn := sqltest.PostgresDSN(t)
	st, err := postgres.Open(context.Background(), dsn, postgres.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sqltest.Reset(t, st.DB(), st.Dialect())
	upgradeV1(t, st)
}

// TestUpgradeSeedLeavesAdministratorsChoices: only the grant version 1
// seeded goes; what an administrator changed stays as they left it.
func TestUpgradeSeedLeavesAdministratorsChoices(t *testing.T) {
	ctx := context.Background()

	edited := inmem.New()
	seedV1(t, edited)
	p := v1AnyRemote()
	r := p.Resources[types.ResourceArtifact]
	r.Actions.Groups = map[string]types.Actions{ReadersGroup: types.ActionRead}
	p.Resources[types.ResourceArtifact] = r
	if err := edited.Permissions().Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	if removed, err := UpgradeSeed(ctx, edited, upgradeTime); removed || err != nil {
		t.Fatalf("edited: UpgradeSeed = %v, %v", removed, err)
	}
	if got := anonymousOnAnyRemote(t, edited); got != types.ActionRead|types.ActionWrite {
		t.Fatalf("edited: anonymous now holds %v", got)
	}
	if m := markerVersion(t, edited); m.Version != seedVersion {
		t.Fatalf("edited: marker = %+v", m)
	}

	deleted := inmem.New()
	seedV1(t, deleted)
	if err := deleted.Permissions().Delete(ctx, AnyRemotePermission); err != nil {
		t.Fatal(err)
	}
	if removed, err := UpgradeSeed(ctx, deleted, upgradeTime); removed || err != nil {
		t.Fatalf("deleted: UpgradeSeed = %v, %v", removed, err)
	}
	if _, err := deleted.Permissions().Get(ctx, AnyRemotePermission); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("deleted: Any Remote came back: %v", err)
	}
	if m := markerVersion(t, deleted); m.Version != seedVersion {
		t.Fatalf("deleted: marker = %+v", m)
	}

	// Version 1 never seeded anonymous into Anything, so one there was an
	// administrator's.
	anything := inmem.New()
	seedV1(t, anything)
	a, err := anything.Permissions().Get(ctx, AnythingPermission)
	if err != nil {
		t.Fatal(err)
	}
	r = a.Resources[types.ResourceArtifact]
	r.Actions.Users = map[string]types.Actions{AnonymousUser: types.ActionRead}
	a.Resources[types.ResourceArtifact] = r
	if err := anything.Permissions().Update(ctx, a); err != nil {
		t.Fatal(err)
	}
	if removed, err := UpgradeSeed(ctx, anything, upgradeTime); !removed || err != nil {
		t.Fatalf("Anything: UpgradeSeed = %v, %v", removed, err)
	}
	a, err = anything.Permissions().Get(ctx, AnythingPermission)
	if err != nil ||
		a.Resources[types.ResourceArtifact].Actions.Users[AnonymousUser] != types.ActionRead {
		t.Fatalf("Anything lost the administrator's grant: %+v, %v", a, err)
	}
}

func TestUpgradeSeedNothingToDo(t *testing.T) {
	ctx := context.Background()
	empty := inmem.New()
	if removed, err := UpgradeSeed(ctx, empty, upgradeTime); removed || err != nil {
		t.Fatalf("unseeded: UpgradeSeed = %v, %v", removed, err)
	}
	if seeded, _ := Seeded(ctx, empty); seeded {
		t.Fatal("upgrading an empty store seeded it")
	}
	// A store this version seeded is current, even with an anonymous grant
	// an administrator then put on Any Remote.
	current := inmem.New()
	if _, err := Seed(ctx, current, "pw", seedTime); err != nil {
		t.Fatal(err)
	}
	if err := current.Permissions().Update(ctx, v1AnyRemote()); err != nil {
		t.Fatal(err)
	}
	if removed, err := UpgradeSeed(ctx, current, upgradeTime); removed || err != nil {
		t.Fatalf("current: UpgradeSeed = %v, %v", removed, err)
	}
	if got := anonymousOnAnyRemote(t, current); got != types.ActionRead|types.ActionWrite {
		t.Fatalf("current: anonymous now holds %v", got)
	}
	// Another instance upgraded between the first marker read and the
	// transaction: the read inside it sees version 2 and stops.
	raced := &staleMarker{Store: current}
	if removed, err := UpgradeSeed(ctx, raced, upgradeTime); removed || err != nil {
		t.Fatalf("raced: UpgradeSeed = %v, %v", removed, err)
	}
	if got := anonymousOnAnyRemote(t, current); got != types.ActionRead|types.ActionWrite {
		t.Fatalf("raced: anonymous now holds %v", got)
	}
}

func TestUpgradeSeedErrors(t *testing.T) {
	ctx := context.Background()
	v1 := func() store.Store {
		db := inmem.New()
		seedV1(t, db)
		return db
	}
	boom := errors.New("database down")
	for name, st := range map[string]store.Store{
		"marker":       failingSettingsStore{v1()},
		"marker in tx": &racingErr{Store: v1()},
		"get":          failingPerms{Store: v1(), get: boom},
		"update":       failingPerms{Store: v1(), update: boom},
		"marker write": failingPut{v1()},
	} {
		if removed, err := UpgradeSeed(ctx, st, upgradeTime); removed || err == nil {
			t.Errorf("%s: UpgradeSeed = %v, %v", name, removed, err)
		}
	}
	// A failed marker write rolls the permission back too, so the next
	// start tries again.
	db := v1()
	if _, err := UpgradeSeed(ctx, failingPut{db}, upgradeTime); err == nil {
		t.Fatal("a failed marker write was not reported")
	}
	if got := anonymousOnAnyRemote(t, db); got != types.ActionRead|types.ActionWrite {
		t.Fatalf("the failed upgrade was not rolled back: anonymous holds %v", got)
	}
	if removed, err := UpgradeSeed(ctx, db, upgradeTime); !removed || err != nil {
		t.Fatalf("retry: UpgradeSeed = %v, %v", removed, err)
	}
}

func TestSeededV1(t *testing.T) {
	if !seededV1(v1AnyRemote()) {
		t.Fatal("version 1's own Any Remote does not match")
	}
	for name, edit := range map[string]func(p *types.Permission){
		"description": func(p *types.Permission) { p.Description = "mine" },
		"no artifact section": func(p *types.Permission) {
			p.Resources = map[types.ResourceType]types.Resource{}
		},
		"build section": func(p *types.Permission) {
			p.Resources[types.ResourceBuild] = types.Resource{}
		},
		"another target": func(p *types.Permission) {
			p.Resources[types.ResourceArtifact].Targets["libs-remote"] = types.Target{}
		},
		"another target only": func(p *types.Permission) {
			r := p.Resources[types.ResourceArtifact]
			r.Targets = map[string]types.Target{"libs-remote": {Include: []string{"**"}}}
			p.Resources[types.ResourceArtifact] = r
		},
		"a group": func(p *types.Permission) {
			r := p.Resources[types.ResourceArtifact]
			r.Actions.Groups = map[string]types.Actions{ReadersGroup: types.ActionRead}
			p.Resources[types.ResourceArtifact] = r
		},
		"another user": func(p *types.Permission) {
			p.Resources[types.ResourceArtifact].Actions.Users["ci"] = types.ActionRead
		},
		"read only": func(p *types.Permission) {
			p.Resources[types.ResourceArtifact].Actions.Users[AnonymousUser] = types.ActionRead
		},
		"include": func(p *types.Permission) {
			p.Resources[types.ResourceArtifact].Targets[types.TargetAnyRemote] = types.Target{
				Include: []string{"org/**"},
			}
		},
		"exclude": func(p *types.Permission) {
			p.Resources[types.ResourceArtifact].Targets[types.TargetAnyRemote] = types.Target{
				Include: []string{"**"}, Exclude: []string{"secret/**"},
			}
		},
	} {
		p := v1AnyRemote()
		edit(&p)
		if seededV1(p) {
			t.Errorf("%s: an edited Any Remote matches version 1", name)
		}
	}
}

// staleMarker answers the first marker read with version 1, as if another
// instance upgraded the store just after it.
type staleMarker struct {
	store.Store
	read bool
}

func (s *staleMarker) Settings() store.SettingsStore {
	return staleSettings{s.Store.Settings(), s}
}

type staleSettings struct {
	store.SettingsStore
	s *staleMarker
}

func (s staleSettings) Get(ctx context.Context, key string, into any) error {
	if key == types.SettingSeeded && !s.s.read {
		s.s.read = true
		*into.(*seedMarker) = seedMarker{Version: 1, At: seedTime}
		return nil
	}
	return s.SettingsStore.Get(ctx, key, into) //nolint:wrapcheck // test double
}

// failingPerms fails permission reads or writes with the errors it holds.
type failingPerms struct {
	store.Store
	get, update error
}

func (f failingPerms) Permissions() store.PermissionStore {
	return failingPermStore{f.Store.Permissions(), f}
}

type failingPermStore struct {
	store.PermissionStore
	f failingPerms
}

func (p failingPermStore) Get(ctx context.Context, name string) (types.Permission, error) {
	if p.f.get != nil {
		return types.Permission{}, p.f.get
	}
	return p.PermissionStore.Get(ctx, name) //nolint:wrapcheck // test double
}

func (p failingPermStore) Update(ctx context.Context, perm types.Permission) error {
	if p.f.update != nil {
		return p.f.update
	}
	return p.PermissionStore.Update(ctx, perm) //nolint:wrapcheck // test double
}
