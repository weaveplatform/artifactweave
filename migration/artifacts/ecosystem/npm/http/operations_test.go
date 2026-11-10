package npmhttp

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	npmfmt "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/npm"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	artgen "github.com/weaveplatform/artifactweave/migration/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// reindexFaults fails one store call a JFrog operation makes: "settings"
// the anonymous-access read behind a caller's rights, "enqueue" queueing
// work, and "get" reading it.
type reindexFaults struct {
	*inmem.Store
	fail string
}

func (s reindexFaults) Settings() store.SettingsStore {
	if s.fail == "settings" {
		return settingsDown{s.Store.Settings()}
	}
	return s.Store.Settings()
}

func (s reindexFaults) WorkQueue() store.WorkQueue {
	return queueDown{s.Store.WorkQueue(), s.fail}
}

type settingsDown struct{ store.SettingsStore }

func (settingsDown) Get(context.Context, string, any) error { return errBoom }

type queueDown struct {
	store.WorkQueue
	fail string
}

func (q queueDown) Enqueue(
	ctx context.Context,
	kind, key string,
	payload []byte,
	now time.Time,
) error {
	if q.fail == "enqueue" {
		return errBoom
	}
	return q.WorkQueue.Enqueue(ctx, kind, key, payload, now) //nolint:wrapcheck // test double
}

func (q queueDown) Get(ctx context.Context, kind, key string) (types.WorkItem, error) {
	if q.fail == "get" {
		return types.WorkItem{}, errBoom
	}
	return q.WorkQueue.Get(ctx, kind, key) //nolint:wrapcheck // test double
}

// A JFrog operation without a store answers 501, as the stub does; without
// a caller, 401; and a store that fails under it, 500.
func TestAccReindexFaults(t *testing.T) {
	ctx := context.Background()
	f := &reindexRecorder{}
	db := inmem.New()
	for _, c := range []repo.Config{
		{Key: "nuget-local", Class: repo.Local, PackageType: repo.NuGet},
		{Key: "npm-local", Class: repo.Local, PackageType: repo.NPM},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	on := func(st store.Store) NPMAPI {
		cfg := Config{Formats: recording(f)}
		if st != nil {
			cfg.Store = st
		}
		return NPMAPI{newKit(withAccess(t, cfg))}
	}
	status := func(err error) int {
		var e *apierror.Error
		if !errors.As(err, &e) {
			return 0
		}
		return e.Status
	}
	if _, err := on(nil).Reindex(ctx, repo.NuGet, "nuget-local", "", false); status(err) !=
		http.StatusNotImplemented {
		t.Errorf("no store: %v", err)
	}
	if _, err := on(db).Reindex(ctx, repo.NuGet, "nuget-local", "", false); status(err) !=
		http.StatusUnauthorized {
		t.Errorf("no caller: %v", err)
	}
	admin := kernel.WithPrincipal(ctx, kernel.Principal{Name: "admin", Admin: true})
	for _, c := range []struct {
		fail string
		call func(NPMAPI) error
	}{
		{"settings", func(s NPMAPI) error {
			_, err := s.Reindex(kernel.WithPrincipal(ctx, kernel.Principal{Name: "deployer"}),
				repo.NuGet, "nuget-local", "", false)
			return err
		}},
		{"enqueue", func(s NPMAPI) error {
			_, err := s.Reindex(admin, repo.NuGet, "nuget-local", "", false)
			return err
		}},
		{"enqueue", func(s NPMAPI) error {
			_, err := s.MigrateNpmRepositoryToStrictLayout(admin,
				artgen.MigrateNpmRepositoryToStrictLayoutRequestObject{RepoKey: "npm-local"})
			return err
		}},
		{"get", func(s NPMAPI) error {
			_, err := s.ResumeNpmRepositoryMigration(admin,
				artgen.ResumeNpmRepositoryMigrationRequestObject{RepoKey: "npm-local"})
			return err
		}},
	} {
		if err := c.call(on(reindexFaults{db, c.fail})); status(err) !=
			http.StatusInternalServerError {
			t.Errorf("%s: %v", c.fail, err)
		}
	}
	// A parked conversion whose resume cannot be queued.
	park(t, db, npmfmt.KindConvert, "npm-local")
	if _, err := on(reindexFaults{db, "enqueue"}).ResumeNpmRepositoryMigration(admin,
		artgen.ResumeNpmRepositoryMigrationRequestObject{RepoKey: "npm-local"}); status(err) !=
		http.StatusInternalServerError {
		t.Errorf("resume: %v", err)
	}
}

// park queues kind for key on st's work queue and parks it, as the queue
// parks work whose retries have run out.
func park(t *testing.T, st store.Store, kind, key string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	q := st.WorkQueue()
	if err := q.Enqueue(ctx, kind, key, nil, now); err != nil {
		t.Fatal(err)
	}
	it, took, err := q.LeaseKey(ctx, kind, key, store.WorkLease{
		Holder: "test", Until: now.Add(time.Minute), Now: now,
	})
	if err != nil || !took {
		t.Fatalf("lease %v %v", took, err)
	}
	if err := q.Fail(ctx, kind, key, it.Generation, store.WorkFailure{
		Error: "spent", Park: true, RetryAt: now, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
}
