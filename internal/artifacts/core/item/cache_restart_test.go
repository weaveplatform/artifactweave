package item_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appstoretest "github.com/weaveplatform/artifactweave/internal/app/storetest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

func TestAccCacheCleanupSQLiteRestart(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	open := func() *appstoretest.SQLStore {
		s, err := appstoretest.OpenSQLite(ctx, dbPath, appstoretest.SQLiteOptions{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	db := open()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := item.At
	svc := &item.Service{Store: db, Blobs: blobs, Now: func() time.Time { return now }}
	hours := 1
	c := *repo.Defaults(&repo.Config{Key: "remote", Class: repo.Remote, PackageType: repo.Generic, URL: "https://upstream.example", UnusedArtifactsCleanupPeriodHours: &hours})
	if err = db.Repos().Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	for n := range item.CacheCleanupBatch + 2 {
		key := model.RemoteCacheKey{RepoKey: c.Key, Path: fmt.Sprintf("metadata/%03d", n)}
		token, err := db.RemoteCache().Begin(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = svc.Cache(
			ctx,
			item.CacheUpload{
				Token:  token,
				Repo:   c,
				Body:   strings.NewReader("versions"),
				Length: 8,
				Kind:   model.RemoteMetadata,
			},
		); err != nil {
			t.Fatal(err)
		}
	}
	now = item.At.Add(2 * time.Hour)
	q := item.NewQueue(t, db, work.Options{Now: svc.Now})
	if err = svc.RegisterCacheMaintenance(ctx, q); err != nil {
		t.Fatal(err)
	}
	if _, err = q.RunDue(ctx, item.KindCacheCleanupSchedule); err != nil {
		t.Fatal(err)
	}
	if _, err = q.RunDue(ctx, item.KindCacheCleanup); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := db.WorkQueue().Get(ctx, item.KindCacheCleanup, c.Key)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open()
	svc.Store = db
	q = item.NewQueue(t, db, work.Options{Now: svc.Now})
	if err = svc.RegisterCacheMaintenance(ctx, q); err != nil {
		t.Fatal(err)
	}
	resumed, err := db.WorkQueue().Get(ctx, item.KindCacheCleanup, c.Key)
	if err != nil || string(resumed.Payload) != string(checkpoint.Payload) {
		t.Fatal("restart reset durable cursor", err)
	}
	if n := item.RunCleanup(t, q); n != 2 {
		t.Fatal("restart did not drain remaining pages", n)
	}
	ref, err := db.Blobs().Get(ctx, item.Sums("versions").SHA256)
	if err != nil || ref.Refs != 0 {
		t.Fatal("restart leaked raw metadata", ref, err)
	}
	if _, err = db.WorkQueue().
		Get(ctx, item.KindCacheCleanup, c.Key); !errors.Is(
		err,
		kernel.ErrNotFound,
	) {
		t.Fatal("cleanup job remains", err)
	}
}

func TestAccCacheCleanupConcurrentDownload(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var db *appstoretest.SQLStore
			var err error
			if backend == "postgres" {
				dsn := os.Getenv("ARTIFACTWEAVE_TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("ARTIFACTWEAVE_TEST_POSTGRES_DSN not set")
				}
				db, err = appstoretest.OpenPostgres(
					ctx,
					dsn,
					appstoretest.PostgresOptions{MaxOpenConns: 4},
				)
			} else {
				db, err = appstoretest.OpenSQLite(
					ctx,
					filepath.Join(t.TempDir(), "race.db"),
					appstoretest.SQLiteOptions{},
				)
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			blobs, err := fs.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			now := item.At
			svc := &item.Service{Store: db, Blobs: blobs, Now: func() time.Time { return now }}
			hours := 1
			c := *repo.Defaults(&repo.Config{Key: fmt.Sprintf("cleanup-%d", time.Now().UnixNano()), Class: repo.Remote, PackageType: repo.Generic, URL: "https://upstream.example", UnusedArtifactsCleanupPeriodHours: &hours})
			if err = db.Repos().Create(ctx, c); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Repos().Delete(context.Background(), c.Key) })
			token, err := db.RemoteCache().
				Begin(ctx, model.RemoteCacheKey{RepoKey: c.Key, Path: "artifact.bin"})
			if err != nil {
				t.Fatal(err)
			}
			cached, err := svc.Cache(
				ctx,
				item.CacheUpload{
					Repo:   c,
					Token:  token,
					Kind:   model.RemoteUnique,
					Body:   strings.NewReader("binary"),
					Length: 6,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			now = item.At.Add(2 * time.Hour)
			identity, err := db.RemoteCache().Source(ctx, c.Key)
			if err != nil {
				t.Fatal(err)
			}
			recorded, release, downloaded := make(
				chan struct{},
			), make(
				chan struct{},
			), make(
				chan error,
				1,
			)
			go func() {
				downloaded <- db.Tx(ctx, func(ctx context.Context) error {
					if err := db.Items().RecordDownload(ctx, cached.Item.ID, port.Download{At: now}); err != nil {
						return err
					}
					close(recorded)
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				})
			}()
			select {
			case <-recorded:
			case err := <-downloaded:
				t.Fatal("record failed", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			cleaned := make(chan error, 1)
			go func() {
				cleaned <- svc.CleanupCachedItem(ctx, c.Key, identity.Repository, cached.Item)
			}()
			// The downloader holds the item's usage lock. Cleanup must wait until
			// that transaction decides whether the successful read is durable.
			select {
			case err := <-cleaned:
				close(release)
				t.Fatal("cleanup crossed active download transaction", err)
			case <-time.After(30 * time.Millisecond):
			}
			close(release)
			if err = <-downloaded; err != nil {
				t.Fatal(err)
			}
			if err = <-cleaned; err != nil {
				t.Fatal(err)
			}
			if _, err = db.Items().GetByID(ctx, cached.Item.ID); err != nil {
				t.Fatal("cleanup erased recent download", err)
			}
		})
	}
}
