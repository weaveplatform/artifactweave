package sql_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	artsql "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

func TestAccPackageVersionRevisionGuardsLinkedMutations(t *testing.T) {
	sqltest.RunModule(t, mods, artsql.New, func(t *testing.T, newStore func(*testing.T) *artsql.Store) {
		db := newStore(t)
		if db.Dialect().Name != "postgres" {
			t.Skip("PostgreSQL version row fence; SQLite serializes cleanup writers")
		}
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		if err := db.Repos().Create(ctx, *repo.Defaults(&repo.Config{Key: "fence", Class: repo.Local, PackageType: repo.Generic})); err != nil {
			t.Fatal(err)
		}
		p, err := db.Packages().Ensure(ctx, model.Package{RepoKey: "fence", Type: repo.Generic, Name: "package"})
		if err != nil {
			t.Fatal(err)
		}
		v, err := db.Packages().PutVersion(ctx, model.PackageVersion{PackageID: p.ID, Version: "1"})
		if err != nil {
			t.Fatal(err)
		}
		it, err := db.Items().Put(ctx, model.Item{RepoKey: "fence", Name: "file.bin"})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Packages().LinkFile(ctx, model.VersionFile{VersionID: v.ID, ItemID: it.ID}); err != nil {
			t.Fatal(err)
		}
		reader := pkgindex.NewReader(db, nil)
		ref := api.IndexVersion{
			ID:      v.ID,
			Version: v.Version,
			Package: api.IndexPackage{ID: p.ID, Name: p.Name, Repository: api.IndexRepository{Key: "fence", Class: repo.Local, PackageType: repo.Generic}},
		}
		before, err := reader.Version(ctx, ref, 0)
		if err != nil {
			t.Fatal(err)
		}
		held, started := make(chan int, 1), make(chan int, 1)
		release := make(chan struct{})
		guardDone, writerDone := make(chan error, 1), make(chan error, 1)
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		defer unblock()
		go func() {
			guardDone <- db.Tx(ctx, func(ctx context.Context) error {
				current, err := reader.Version(ctx, ref, 0)
				if err != nil {
					return err
				}
				if current.MutationRevision != before.MutationRevision {
					return fmt.Errorf("version changed before the guard")
				}
				var pid int
				if err := db.Q(ctx).QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
					return fmt.Errorf("guard pid: %w", err)
				}
				held <- pid
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		}()
		var holder int
		select {
		case holder = <-held:
		case err := <-guardDone:
			t.Fatal(err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		go func() {
			writerDone <- db.Tx(ctx, func(ctx context.Context) error {
				var pid int
				if err := db.Q(ctx).QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
					return fmt.Errorf("writer pid: %w", err)
				}
				started <- pid
				return db.Items().RecordDownload(ctx, it.ID, port.Download{})
			})
		}()
		var waiter int
		select {
		case waiter = <-started:
		case err := <-writerDone:
			t.Fatal(err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			var blocked bool
			if err := db.DB().QueryRowContext(ctx, "SELECT $1::integer = ANY(pg_blocking_pids($2::integer))", holder, waiter).Scan(&blocked); err != nil {
				t.Fatal(err)
			}
			if blocked {
				break
			}
			select {
			case err := <-writerDone:
				t.Fatalf("linked mutation escaped the publication fence: %v", err)
			case <-ticker.C:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
		unblock()
		for _, done := range []chan error{guardDone, writerDone} {
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
		after, err := reader.Version(ctx, ref, 0)
		if err != nil || after.MutationRevision == before.MutationRevision {
			t.Fatal("released mutation did not replace the guarded revision", err)
		}
	}, sqltest.WithPostgres())
}
