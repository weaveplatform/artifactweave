package sqltest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// RemoteCachePersistence exercises upgrading real pre-cache repositories and
// reopening the backend with completed and in-flight retrieval records.
func RemoteCachePersistence(t *testing.T, s *sqlcommon.Store, reopen func() *sqlcommon.Store) {
	t.Helper()
	ctx := context.Background()
	Reset(t, s.DB(), s.Dialect())
	if _, err := sqlcommon.Rollback(ctx, s.DB(), s.Dialect(), 14); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"legacy", "second"} {
		c := repo.Config{
			Key:         key,
			Class:       repo.Remote,
			PackageType: repo.Generic,
			URL:         "https://upstream.example",
		}
		doc, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.DB().
			ExecContext(ctx, s.Dialect().Rebind("INSERT INTO repositories (key,rclass,package_type,project_key,created_at,updated_at,doc) VALUES (?,?,?,?,?,?,?)"), key, "remote", "generic", "", 1, 1, string(doc))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sqlcommon.Migrate(ctx, s.DB(), s.Dialect()); err != nil {
		t.Fatal(err)
	}
	source, err := s.RemoteCache().Source(ctx, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.RemoteCache().Source(ctx, "second")
	if err != nil {
		t.Fatal(err)
	}
	if source.Repository == "" || source.Repository == other.Repository || source.Generation == "" {
		t.Fatal("upgrade did not allocate distinct durable lifetimes")
	}
	k := types.RemoteCacheKey{RepoKey: "legacy", Path: "index.json", Variant: "raw"}
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
	got, err := s.RemoteCache().Source(ctx, "legacy")
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
	if _, err = s.RemoteCache().Invalidate(ctx, "legacy", "pending", false); err != nil {
		t.Fatal(err)
	}
	if err = s.RemoteCache().Check(ctx, pending); !errors.Is(err, store.ErrConflict) {
		t.Fatal("restart invalidation did not fence", err)
	}
	if _, err = sqlcommon.Rollback(ctx, s.DB(), s.Dialect(), 14); err != nil {
		t.Fatal(err)
	}
	b, err := s.Blobs().Get(ctx, strings.Repeat("a", 64))
	if err != nil || b.Refs != 0 {
		t.Fatal("down migration leaked metadata reference", b, err)
	}
	if _, err = sqlcommon.Migrate(ctx, s.DB(), s.Dialect()); err != nil {
		t.Fatal(err)
	}
	got, err = s.RemoteCache().Source(ctx, "legacy")
	if err != nil || got.Repository == source.Repository {
		t.Fatal("fresh cache schema reused old lifetime", got, err)
	}
}
