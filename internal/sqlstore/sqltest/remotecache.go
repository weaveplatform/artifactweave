package sqltest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// RemoteCachePersistence checks that each repository keeps a durable
// lifetime of its own, and that the lifetime and the completed and in-flight
// retrieval records survive reopening the backend.
func RemoteCachePersistence(t *testing.T, s *sqlcommon.Store, reopen func() *sqlcommon.Store) {
	t.Helper()
	ctx := context.Background()
	Reset(t, s.DB(), s.Dialect())
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
	k := types.RemoteCacheKey{RepoKey: "first", Path: "index.json", Variant: "raw"}
	token, err := s.RemoteCache().Begin(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	e, err := s.RemoteCache().
		Put(ctx, token, types.RemoteCacheEntry{Kind: types.RemoteMetadata, Blob: types.Blob{Checksums: types.Checksums{SHA256: strings.Repeat("a", 64)}, Size: 123, Created: now}, ETag: "v1", FetchedAt: now, ValidatedAt: now})
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
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = reopen()
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
	if err = s.RemoteCache().Check(ctx, pending); !errors.Is(err, store.ErrConflict) {
		t.Fatal("restart invalidation did not fence", err)
	}
	remoteLayoutPersistence(t, s, reopen)
}

func remoteLayoutPersistence(t *testing.T, s *sqlcommon.Store, reopen func() *sqlcommon.Store) {
	t.Helper()
	ctx := t.Context()
	c, err := s.Repos().Get(ctx, "first")
	if err != nil {
		t.Fatal(err)
	}
	c.RepoLayoutRef = "durable-layout"
	if err = s.Repos().Update(ctx, c); err != nil {
		t.Fatal(err)
	}
	token, err := s.RemoteCache().
		Begin(ctx, types.RemoteCacheKey{RepoKey: c.Key, Path: "layout-pending", Variant: "raw"})
	if err != nil {
		t.Fatal(err)
	}
	doc := types.ConfigDocument{
		Kind: types.ConfigLayout,
		Key:  c.RepoLayoutRef,
		Document: json.RawMessage(
			`{"artifactPathPattern":"[org]/[module]/[baseRev]/[module]-[baseRev].[ext]"}`,
		),
	}
	if err = s.Configs().Put(ctx, doc); err != nil {
		t.Fatal(err)
	}
	current, err := s.RemoteCache().Source(ctx, c.Key)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = reopen()
	got, err := s.RemoteCache().Source(ctx, c.Key)
	if err != nil || got != current {
		t.Fatal("restart lost layout source generation", got, current, err)
	}
	if err = s.RemoteCache().Check(ctx, token); !errors.Is(err, store.ErrConflict) {
		t.Fatal("restart restored stale layout publication authority", err)
	}
	stored, err := s.Configs().Get(ctx, doc.Kind, doc.Key)
	if err != nil || string(stored.Document) != string(doc.Document) {
		t.Fatal("layout and generation did not persist together", stored, err)
	}
}
