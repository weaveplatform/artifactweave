package auth

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/sqlstore/postgres"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqltest"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var seedTime = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

// checkSeeded asserts every built-in exists as seeded, and that admin can
// log in with password.
func checkSeeded(t *testing.T, st store.Store, password string) {
	t.Helper()
	ctx := context.Background()
	admin, err := st.Users().Get(ctx, AdminUser)
	if err != nil || !admin.Admin || admin.Status != types.UserEnabled {
		t.Fatalf("admin = %+v, %v", admin, err)
	}
	if ok, err := CheckPassword(admin.PasswordHash, password); !ok || err != nil {
		t.Fatalf("admin password: %v %v", ok, err)
	}
	if anon, err := st.Users().Get(ctx, AnonymousUser); err != nil || len(anon.PasswordHash) != 0 {
		t.Fatalf("anonymous = %+v, %v", anon, err)
	}
	if g, err := st.Groups().Get(ctx, ReadersGroup); err != nil || !g.AutoJoin {
		t.Fatalf("readers = %+v, %v", g, err)
	}
	anything, err := st.Permissions().Get(ctx, AnythingPermission)
	if err != nil ||
		anything.Resources[types.ResourceArtifact].Actions.Groups[ReadersGroup] != types.ActionRead ||
		len(anything.Resources[types.ResourceArtifact].Targets[types.TargetAny].Include) != 1 {
		t.Fatalf("Anything = %+v, %v", anything, err)
	}
	remote, err := st.Permissions().Get(ctx, AnyRemotePermission)
	if err != nil || remote.Resources[types.ResourceArtifact].Actions.Users[AnonymousUser] !=
		types.ActionRead|types.ActionWrite {
		t.Fatalf("Any Remote = %+v, %v", remote, err)
	}
	bi, err := st.Repos().Get(ctx, types.BuildInfoRepository)
	if err != nil || bi.Class != repo.Local || bi.PackageType != repo.BuildInfo ||
		bi.RepoLayoutRef != repo.LayoutSimple {
		t.Fatalf("build-info repository = %+v, %v", bi, err)
	}
	if seeded, err := Seeded(ctx, st); !seeded || err != nil {
		t.Fatalf("Seeded = %v, %v", seeded, err)
	}
}

// seedTwice seeds st, checks the result, then seeds again with another
// password and checks nothing changed.
func seedTwice(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	if now, err := Seed(ctx, st, "first", seedTime); !now || err != nil {
		t.Fatalf("first Seed = %v, %v", now, err)
	}
	checkSeeded(t, st, "first")
	if now, err := Seed(ctx, st, "second", seedTime.Add(time.Hour)); now || err != nil {
		t.Fatalf("second Seed = %v, %v", now, err)
	}
	checkSeeded(t, st, "first")
}

func TestSeedInMemory(t *testing.T) { seedTwice(t, inmem.New()) }

func TestSeedSQLite(t *testing.T) {
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "aw.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	seedTwice(t, st)
}

// TestSeedPostgres needs Docker and skips without it; CI runs it on Linux.
func TestSeedPostgres(t *testing.T) {
	dsn := sqltest.PostgresDSN(t)
	ctx := context.Background()
	st, err := postgres.Open(ctx, dsn, postgres.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sqltest.Reset(t, st.DB(), st.Dialect())
	seedTwice(t, st)
}

// racing hides the seeded marker from its first hide reads, as if another
// instance committed its seed just after each of them.
type racing struct {
	store.Store
	hide atomic.Int32
}

func (r *racing) Settings() store.SettingsStore { return racingSettings{r.Store.Settings(), r} }

type racingSettings struct {
	store.SettingsStore
	r *racing
}

func (s racingSettings) Get(ctx context.Context, key string, into any) error {
	if key == types.SettingSeeded && s.r.hide.Add(-1) >= 0 {
		return store.ErrNotFound
	}
	return s.SettingsStore.Get(ctx, key, into) //nolint:wrapcheck // test double
}

func TestSeedLosesARace(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	if _, err := Seed(ctx, db, "winner", seedTime); err != nil {
		t.Fatal(err)
	}
	// The marker shows up inside the transaction (one hidden read), or only
	// after the creates conflict (two hidden reads). Either way this call
	// did not seed and is not an error.
	for hide := int32(1); hide <= 2; hide++ {
		r := &racing{Store: db}
		r.hide.Store(hide)
		if now, err := Seed(ctx, r, "loser", seedTime); now || err != nil {
			t.Fatalf("hide %d: Seed = %v, %v", hide, now, err)
		}
	}
	checkSeeded(t, db, "winner")
}

// failingPut fails every settings write.
type failingPut struct{ store.Store }

func (f failingPut) Settings() store.SettingsStore { return putFails{f.Store.Settings()} }

type putFails struct{ store.SettingsStore }

func (putFails) Put(context.Context, string, any) error { return errors.New("disk full") }

func TestSeedErrors(t *testing.T) {
	ctx := context.Background()
	// Each built-in already present (without the marker) conflicts, and
	// the whole seed rolls back.
	for name, pre := range map[string]func(store.Store) error{
		"group": func(st store.Store) error {
			return st.Groups().Create(ctx, types.Group{Name: ReadersGroup})
		},
		"user": func(st store.Store) error {
			return st.Users().Create(ctx, types.User{Name: AdminUser})
		},
		"permission": func(st store.Store) error {
			return st.Permissions().Create(ctx, types.Permission{Name: AnythingPermission})
		},
		"repository": func(st store.Store) error {
			return st.Repos().Create(ctx, repo.Config{
				Key: types.BuildInfoRepository, Class: repo.Local, PackageType: repo.Generic,
			})
		},
	} {
		db := inmem.New()
		if err := pre(db); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if now, err := Seed(ctx, db, "pw", seedTime); now || !errors.Is(err, store.ErrConflict) {
			t.Errorf("%s: Seed = %v, %v", name, now, err)
		}
		if _, err := db.Users().Get(ctx, AnonymousUser); !errors.Is(err, store.ErrNotFound) &&
			name != "user" {
			t.Errorf("%s: the failed seed was not rolled back", name)
		}
	}
	if _, err := Seed(ctx, failingPut{inmem.New()}, "pw", seedTime); err == nil {
		t.Fatal("a failed marker write was not reported")
	}
	if _, err := Seeded(ctx, failingSettingsStore{inmem.New()}); err == nil {
		t.Fatal("Seeded hid a settings failure")
	}
	if _, err := Seed(ctx, failingSettingsStore{inmem.New()}, "pw", seedTime); err == nil {
		t.Fatal("Seed hid a settings failure")
	}
	// The marker read inside the transaction can fail on its own too.
	r := &racingErr{Store: inmem.New()}
	if _, err := Seed(ctx, r, "pw", seedTime); err == nil {
		t.Fatal("Seed hid a settings failure inside its transaction")
	}
}

// failingSettingsStore fails every settings read.
type failingSettingsStore struct{ store.Store }

func (f failingSettingsStore) Settings() store.SettingsStore {
	return failingSettings{f.Store.Settings()}
}

// racingErr answers the first marker read and fails the second.
type racingErr struct {
	store.Store
	n atomic.Int32
}

func (r *racingErr) Settings() store.SettingsStore { return racingErrSettings{r.Store.Settings(), r} }

type racingErrSettings struct {
	store.SettingsStore
	r *racingErr
}

func (s racingErrSettings) Get(ctx context.Context, key string, into any) error {
	if s.r.n.Add(1) == 2 {
		return errors.New("database down")
	}
	return s.SettingsStore.Get(ctx, key, into) //nolint:wrapcheck // test double
}
