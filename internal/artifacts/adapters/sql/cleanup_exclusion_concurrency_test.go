package sql

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/storekit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	testkit "github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

type pausedCleanupRecords struct {
	storekit.CleanupRecords
	before, after func()
}

func (r pausedCleanupRecords) PutBarrier(ctx context.Context, key string, ref api.CleanupExclusion) error {
	if r.before != nil {
		r.before()
	}
	if err := r.CleanupRecords.PutBarrier(ctx, key, ref); err != nil {
		return err
	}
	if r.after != nil {
		r.after()
	}
	return nil
}

func cleanupRaceFixture(t *testing.T, db *Store, key string) (api.CleanupExclusion, model.Item) {
	t.Helper()
	ctx := t.Context()
	testkit.Must(t, db.Repos().Create(ctx, *repo.Defaults(&repo.Config{Key: key, Class: repo.Federated, PackageType: repo.NPM})))
	pkg := testkit.Get[model.Package](t)(db.Packages().Ensure(ctx, model.Package{RepoKey: key, Type: repo.NPM, Name: "widget"}))
	version := testkit.Get[model.PackageVersion](t)(db.Packages().PutVersion(ctx, model.PackageVersion{PackageID: pkg.ID, Version: "1"}))
	item := testkit.Get[model.Item](t)(db.Items().Put(ctx, model.Item{RepoKey: key, Name: "package.tgz", Size: 1}))
	testkit.Must(t, db.Packages().LinkFile(ctx, model.VersionFile{VersionID: version.ID, ItemID: item.ID}))
	source := testkit.Get[model.SourceIdentity](t)(db.RemoteCache().Source(ctx, key))
	return api.CleanupExclusion{
		ID:          "race",
		Owner:       "coordinator",
		Repository:  key,
		Incarnation: string(source.Repository),
		Generation:  string(source.Generation),
		Versions:    []api.CleanupVersionIdentity{{DownloadVersion: api.DownloadVersion{Package: pkg.Name, Version: version.Version}, ID: version.ID}},
	}, item
}

func awaitCleanupStage(t *testing.T, ctx context.Context, stage <-chan struct{}) {
	t.Helper()
	select {
	case <-stage:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func resumeCleanupStage(t *testing.T, ctx context.Context, stage chan<- struct{}) {
	t.Helper()
	select {
	case stage <- struct{}{}:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestAccCleanupReservationPublicationRaces(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{Module}, New, func(t *testing.T, newStore func(*testing.T) *Store) {
		if newStore(t).Dialect().Name != "postgres" {
			t.Skip("PostgreSQL independently locked metadata rows")
		}
		t.Run("release closes a delayed prepare without acquiring its repository lock", func(t *testing.T) {
			db := newStore(t)
			ref, item := cleanupRaceFixture(t, db, "delayed")
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			ready := make(chan struct{})
			resume := make(chan struct{})
			defer close(resume)
			records := pausedCleanupRecords{CleanupRecords: cleanupRecords{db}, before: func() {
				close(ready)
				select {
				case <-resume:
				case <-ctx.Done():
				}
			}}
			delayed := storekit.NewCleanupExclusions(db, records)
			result := make(chan error, 1)
			go func() { result <- delayed.Prepare(ctx, ref) }()
			awaitCleanupStage(t, ctx, ready)
			// Prepare owns the repository row but has not bound its identity. Release
			// must finish now and leave a closed row the delayed publication observes.
			testkit.Must(t, db.CleanupExclusions().Release(ctx, ref, func(context.Context) error { return nil }))
			// Deliver the release acknowledgment before permitting old publication.
			resumeCleanupStage(t, ctx, resume)
			if err := <-result; !errors.Is(err, kernel.ErrConflict) {
				t.Fatalf("delayed prepare resurrected terminal identity: %v", err)
			}
			token := testkit.Get[api.CleanupAdmission](t)(db.CleanupExclusions().Admit(ctx, ref.Repository, item.ID))
			testkit.Must(t, db.CleanupExclusions().Complete(ctx, token))
		})
		t.Run("release waits for open identity publication", func(t *testing.T) {
			db := newStore(t)
			ref, _ := cleanupRaceFixture(t, db, "publishing")
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			ready := make(chan struct{})
			resume := make(chan struct{})
			defer close(resume)
			records := pausedCleanupRecords{CleanupRecords: cleanupRecords{db}, after: func() {
				close(ready)
				select {
				case <-resume:
				case <-ctx.Done():
				}
			}}
			delayed := storekit.NewCleanupExclusions(db, records)
			prepared := make(chan error, 1)
			go func() { prepared <- delayed.Prepare(ctx, ref) }()
			awaitCleanupStage(t, ctx, ready)
			authorized := make(chan struct{})
			released := make(chan error, 1)
			go func() {
				released <- db.CleanupExclusions().Release(ctx, ref, func(context.Context) error { close(authorized); return nil })
			}()
			// The callback has no dependency on the repository row held by Prepare.
			awaitCleanupStage(t, ctx, authorized)
			resumeCleanupStage(t, ctx, resume)
			testkit.Must(t, <-prepared)
			testkit.Must(t, <-released)
			if err := db.CleanupExclusions().Prepare(ctx, ref); !errors.Is(err, kernel.ErrConflict) {
				t.Fatalf("closed identity reopened: %v", err)
			}
		})
		t.Run("close validates binding created after its initial lookup", func(t *testing.T) {
			db := newStore(t)
			old, _ := cleanupRaceFixture(t, db, "old")
			foreign, _ := cleanupRaceFixture(t, db, "foreign")
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			ready := make(chan struct{})
			resume := make(chan struct{})
			defer close(resume)
			released := make(chan error, 1)
			go func() {
				released <- db.CleanupExclusions().Release(ctx, old, func(context.Context) error {
					close(ready)
					select {
					case <-resume:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				})
			}()
			awaitCleanupStage(t, ctx, ready)
			testkit.Must(t, db.CleanupExclusions().Prepare(ctx, foreign))
			resumeCleanupStage(t, ctx, resume)
			if err := <-released; !errors.Is(err, kernel.ErrConflict) {
				t.Fatalf("foreign binding was closed after lookup: %v", err)
			}
			testkit.Must(t, db.CleanupExclusions().Guard(ctx, foreign))
			testkit.Must(t, db.CleanupExclusions().Prepare(ctx, foreign))
			testkit.Must(t, db.CleanupExclusions().Release(ctx, foreign, func(context.Context) error { return nil }))
		})
	}, sqltest.WithPostgres())
}
