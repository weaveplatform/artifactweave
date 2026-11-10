package sql_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	artsql "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

// TestAccRemoteCachePersistence runs remoteCachePersistence on SQLite in
// memory and in a file and on PostgreSQL, each over the artifacts stream
// alone and, as the metadata store's run did, without a keyring. A layout
// edit, which fences sources through the platform's config store, is the
// metadata store's half: its dialect tests of this name run it.
func TestAccRemoteCachePersistence(t *testing.T) {
	sqltest.RunModule(t, mods, artsql.New,
		func(t *testing.T, newStore func(*testing.T) *artsql.Store) {
			remoteCachePersistence(t, newStore(t))
		}, sqltest.WithPostgres(), sqltest.WithKeyring(nil))
}

// remoteCachePersistence checks that each repository keeps a durable
// lifetime of its own, and that the lifetime and the completed and in-flight
// retrieval records survive reopening the backend. A reopen is a second
// store over s's database (reopen).
func remoteCachePersistence(t *testing.T, s *artsql.Store) {
	t.Helper()
	ctx := context.Background()
	for _, key := range []string{"first", "second"} {
		c := repo.Config{
			Key:         key,
			Class:       repo.Remote,
			PackageType: repo.Generic,
			URL:         "https://upstream.example",
		}
		if err := s.Repos().Create(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	source, err := s.RemoteCache().Source(ctx, "first")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.RemoteCache().Source(ctx, "second")
	if err != nil {
		t.Fatal(err)
	}
	if source.Repository == "" || source.Repository == other.Repository || source.Generation == "" {
		t.Fatal("the repositories did not get distinct durable lifetimes")
	}
	k := model.RemoteCacheKey{RepoKey: "first", Path: "index.json", Variant: "raw"}
	token, err := s.RemoteCache().Begin(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	e, err := s.RemoteCache().
		Put(ctx, token, model.RemoteCacheEntry{Kind: model.RemoteMetadata, Blob: blob.Blob{Checksums: kernel.Checksums{SHA256: strings.Repeat("a", 64)}, Size: 123, Created: now}, ETag: "v1", FetchedAt: now, ValidatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RemoteCache().Negative(ctx, e.Token, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	pendingKey := k
	pendingKey.Path = "pending"
	pending, err := s.RemoteCache().Begin(ctx, pendingKey)
	if err != nil {
		t.Fatal(err)
	}
	s = reopen(t, s, nil)
	got, err := s.RemoteCache().Source(ctx, "first")
	if err != nil || got != source {
		t.Fatal("restart changed lifetime", got, err)
	}
	saved, err := s.RemoteCache().Read(ctx, k)
	if err != nil || saved.ETag != "v1" || !saved.NegativeUntil.Equal(now.Add(time.Minute)) {
		t.Fatal("restart lost cache state", saved, err)
	}
	if err = s.RemoteCache().Check(ctx, pending); err != nil {
		t.Fatal("restart lost in-flight fence", err)
	}
	if _, err = s.RemoteCache().Invalidate(ctx, "first", "pending", false); err != nil {
		t.Fatal(err)
	}
	if err = s.RemoteCache().Check(ctx, pending); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("restart invalidation did not fence", err)
	}
}
