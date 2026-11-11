package authn

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appstoretest "github.com/weaveplatform/artifactweave/internal/app/storetest"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	platsqltest "github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

var seedTime = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

// seedRepositories stands in for the artifacts module's Repositories, of
// which the seed calls only Create: it keeps each configuration it is given
// and refuses a key it already holds with kernel.ErrConflict, as the
// repository store does, and Get reads one back. It takes no part in the
// seed's transaction, which the artifacts binding that app passes does;
// app's seed tests check that binding.
type seedRepositories struct {
	artifactsapi.Repositories
	configs map[string]repo.Config
}

func newSeedRepositories() *seedRepositories {
	return &seedRepositories{configs: map[string]repo.Config{}}
}

func (r *seedRepositories) Create(_ context.Context, c repo.Config) error {
	if _, ok := r.configs[c.Key]; ok {
		return kernel.ErrConflict
	}
	r.configs[c.Key] = c
	return nil
}

func (r *seedRepositories) Get(_ context.Context, key string) (repo.Config, error) {
	c, ok := r.configs[key]
	if !ok {
		return repo.Config{}, kernel.ErrNotFound
	}
	return c, nil
}

// checkSeeded asserts every built-in exists as seeded, and that admin can
// log in with password.
func checkSeeded(t *testing.T, st SeedStore, repos *seedRepositories, password string) {
	t.Helper()
	ctx := context.Background()
	admin, err := st.Users().Get(ctx, AdminUser)
	if err != nil || !admin.Admin || admin.Status != model.UserEnabled {
		t.Fatalf("admin = %+v, %v", admin, err)
	}
	if ok, err := CheckPassword(admin.PasswordHash, password); !ok || err != nil {
		t.Fatalf("admin password: %v %v", ok, err)
	}
	if anon, err := st.Users().
		Get(ctx, kernel.AnonymousUser); err != nil ||
		len(anon.PasswordHash) != 0 {
		t.Fatalf("anonymous = %+v, %v", anon, err)
	}
	if g, err := st.Groups().Get(ctx, ReadersGroup); err != nil || !g.AutoJoin {
		t.Fatalf("readers = %+v, %v", g, err)
	}
	// Since 7.84.3 neither seeded permission grants anonymous (#383):
	// Anything grants readers alone, and Any Remote no one.
	anything, err := st.Permissions().Get(ctx, AnythingPermission)
	if a := anything.Resources[accessapi.ResourceArtifact]; err != nil ||
		len(a.Actions.Users) != 0 || len(a.Actions.Groups) != 1 ||
		a.Actions.Groups[ReadersGroup] != accessapi.ActionRead ||
		len(a.Targets[accessapi.TargetAny].Include) != 1 {
		t.Fatalf("Anything = %+v, %v", anything, err)
	}
	remote, err := st.Permissions().Get(ctx, AnyRemotePermission)
	if r := remote.Resources[accessapi.ResourceArtifact]; err != nil ||
		len(remote.Resources) != 1 ||
		len(r.Actions.Users) != 0 ||
		len(r.Actions.Groups) != 0 ||
		!slices.Equal(r.Targets[accessapi.TargetAnyRemote].Include, []string{"**"}) {
		t.Fatalf("Any Remote = %+v, %v", remote, err)
	}
	bi, err := repos.Get(ctx, accessapi.BuildInfoRepository)
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
func seedTwice(t *testing.T, st SeedStore) {
	t.Helper()
	ctx := context.Background()
	repos := newSeedRepositories()
	if now, err := Seed(ctx, st, repos, "first", seedTime); !now || err != nil {
		t.Fatalf("first Seed = %v, %v", now, err)
	}
	checkSeeded(t, st, repos, "first")
	if now, err := Seed(ctx, st, repos, "second", seedTime.Add(time.Hour)); now || err != nil {
		t.Fatalf("second Seed = %v, %v", now, err)
	}
	checkSeeded(t, st, repos, "first")
}

func TestUnitSeedInMemory(t *testing.T) { seedTwice(t, inmem.New()) }

func TestAccSeedSQLite(t *testing.T) {
	ctx := context.Background()
	st, err := appstoretest.OpenSQLite(
		ctx,
		filepath.Join(t.TempDir(), "aw.db"),
		appstoretest.SQLiteOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	seedTwice(t, st)
}

// TestAccSeedPostgres needs Docker and skips without it; CI runs it on Linux.
func TestAccSeedPostgres(t *testing.T) {
	dsn := platsqltest.PostgresDSN(t)
	ctx := context.Background()
	st, err := appstoretest.OpenPostgres(ctx, dsn, appstoretest.PostgresOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	appstoretest.Reset(t, st.DB(), st.Dialect())
	seedTwice(t, st)
}

// racing hides the seeded marker from its first hide reads, as if another
// instance committed its seed just after each of them.
type racing struct {
	SeedStore
	hide atomic.Int32
}

func (r *racing) Settings() settings.SettingsStore {
	return racingSettings{r.SeedStore.Settings(), r}
}

type racingSettings struct {
	settings.SettingsStore
	r *racing
}

func (s racingSettings) Get(ctx context.Context, key string, into any) error {
	if key == settings.SettingSeeded && s.r.hide.Add(-1) >= 0 {
		return kernel.ErrNotFound
	}
	return s.SettingsStore.Get(ctx, key, into) //nolint:wrapcheck // test double
}

func TestUnitSeedLosesARace(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	repos := newSeedRepositories()
	if _, err := Seed(ctx, db, repos, "winner", seedTime); err != nil {
		t.Fatal(err)
	}
	// The marker shows up inside the transaction (one hidden read), or only
	// after the creates conflict (two hidden reads). Either way this call
	// did not seed and is not an error.
	for hide := int32(1); hide <= 2; hide++ {
		r := &racing{SeedStore: db}
		r.hide.Store(hide)
		if now, err := Seed(ctx, r, repos, "loser", seedTime); now || err != nil {
			t.Fatalf("hide %d: Seed = %v, %v", hide, now, err)
		}
	}
	checkSeeded(t, db, repos, "winner")
}

// failingPut fails every settings write.
type failingPut struct{ SeedStore }

func (f failingPut) Settings() settings.SettingsStore {
	return putFails{f.SeedStore.Settings()}
}

type putFails struct{ settings.SettingsStore }

func (putFails) Put(context.Context, string, any) error { return errors.New("disk full") }

func TestUnitSeedErrors(t *testing.T) {
	ctx := context.Background()
	// Each built-in already present (without the marker) conflicts, and
	// the whole seed rolls back.
	for name, pre := range map[string]func(SeedStore, *seedRepositories) error{
		"group": func(st SeedStore, _ *seedRepositories) error {
			return st.Groups().Create(ctx, model.Group{Name: ReadersGroup})
		},
		"user": func(st SeedStore, _ *seedRepositories) error {
			return st.Users().Create(ctx, model.User{Name: AdminUser})
		},
		"permission": func(st SeedStore, _ *seedRepositories) error {
			return st.Permissions().Create(ctx, model.Permission{Name: AnythingPermission})
		},
		"repository": func(_ SeedStore, repos *seedRepositories) error {
			return repos.Create(ctx, repo.Config{
				Key: accessapi.BuildInfoRepository, Class: repo.Local, PackageType: repo.Generic,
			})
		},
	} {
		db, repos := inmem.New(), newSeedRepositories()
		if err := pre(db, repos); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if now, err := Seed(
			ctx,
			db,
			repos,
			"pw",
			seedTime,
		); now ||
			!errors.Is(err, kernel.ErrConflict) {
			t.Errorf("%s: Seed = %v, %v", name, now, err)
		}
		if _, err := db.Users().
			Get(ctx, kernel.AnonymousUser); !errors.Is(
			err,
			kernel.ErrNotFound,
		) &&
			name != "user" {
			t.Errorf("%s: the failed seed was not rolled back", name)
		}
	}
	if _, err := Seed(
		ctx,
		failingPut{inmem.New()},
		newSeedRepositories(),
		"pw",
		seedTime,
	); err == nil {
		t.Fatal("a failed marker write was not reported")
	}
	if _, err := Seeded(ctx, failingSettingsStore{inmem.New()}); err == nil {
		t.Fatal("Seeded hid a settings failure")
	}
	if _, err := Seed(
		ctx,
		failingSettingsStore{inmem.New()},
		newSeedRepositories(),
		"pw",
		seedTime,
	); err == nil {
		t.Fatal("Seed hid a settings failure")
	}
	// The marker read inside the transaction can fail on its own too.
	r := &racingErr{SeedStore: inmem.New()}
	if _, err := Seed(ctx, r, newSeedRepositories(), "pw", seedTime); err == nil {
		t.Fatal("Seed hid a settings failure inside its transaction")
	}
}

// failingSettingsStore fails every settings read.
type failingSettingsStore struct{ SeedStore }

func (f failingSettingsStore) Settings() settings.SettingsStore {
	return failingSettings{f.SeedStore.Settings()}
}

// racingErr answers the first marker read and fails the second.
type racingErr struct {
	SeedStore
	n atomic.Int32
}

func (r *racingErr) Settings() settings.SettingsStore {
	return racingErrSettings{r.SeedStore.Settings(), r}
}

type racingErrSettings struct {
	settings.SettingsStore
	r *racingErr
}

func (s racingErrSettings) Get(ctx context.Context, key string, into any) error {
	if s.r.n.Add(1) == 2 {
		return errors.New("database down")
	}
	return s.SettingsStore.Get(ctx, key, into) //nolint:wrapcheck // test double
}
