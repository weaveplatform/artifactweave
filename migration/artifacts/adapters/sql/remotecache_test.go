package sql_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/sqltest"
)

func TestAccRemoteCacheDatabaseFaults(t *testing.T) {
	for _, fault := range []string{"begin-write", "source-read", "entry-read", "publish", "reference", "release", "invalidate-write", "invalidate-release", "state-write", "source-corrupt", "entry-corrupt", "key-corrupt", "list-corrupt", "list-table", "missing-checkpoint"} {
		t.Run(fault, func(t *testing.T) {
			s := mustOpenFile(t, filepath.Join(t.TempDir(), "cache.db"), nil)
			ctx := context.Background()
			if err := s.Repos().
				Create(ctx, repo.Config{Key: "r", Class: repo.Remote, URL: "https://source.example"}); err != nil {
				t.Fatal(err)
			}
			cache := s.RemoteCache()
			key := model.RemoteCacheKey{RepoKey: "r", Path: "index.json"}
			token, err := cache.Begin(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			old, err := cache.Put(
				ctx,
				token,
				model.RemoteCacheEntry{
					Kind: model.RemoteMetadata,
					Blob: blob.Blob{
						Checksums: kernel.Checksums{SHA256: strings.Repeat("a", 64)},
						Size:      1,
					},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			token = old.Token
			next := old
			next.Blob.Checksums.SHA256 = strings.Repeat("b", 64)
			switch fault {
			case "begin-write":
				sqltest.Exec(t, s.DB(), sqltest.FailTrigger("INSERT", "remote_cache"))
				key.Path = "new"
				_, err = cache.Begin(ctx, key)
			case "source-read":
				sqltest.Exec(t, s.DB(), "DROP TABLE remote_sources")
				err = cache.Check(ctx, token)
			case "entry-read":
				sqltest.Exec(t, s.DB(), "DROP TABLE remote_cache")
				_, err = cache.Begin(ctx, key)
				if err == nil {
					t.Fatal("unreadable checkpoint ignored")
				}
				err = cache.Check(ctx, token)
			case "publish":
				sqltest.Exec(t, s.DB(), sqltest.FailTrigger("UPDATE", "remote_cache"))
				_, err = cache.Put(ctx, token, next)
			case "reference":
				sqltest.Exec(t, s.DB(), sqltest.FailTrigger("INSERT", "blobs"))
				_, err = cache.Put(ctx, token, next)
			case "release":
				sqltest.Exec(
					t,
					s.DB(),
					"CREATE TRIGGER fail_release BEFORE UPDATE ON blobs WHEN NEW.ref_count < OLD.ref_count BEGIN SELECT RAISE(ABORT,'injected'); END",
				)
				_, err = cache.Put(ctx, token, next)
			case "invalidate-write":
				sqltest.Exec(t, s.DB(), sqltest.FailTrigger("UPDATE", "remote_cache"))
				_, err = cache.Invalidate(ctx, "r", "", false)
			case "invalidate-release":
				sqltest.Exec(t, s.DB(), sqltest.FailTrigger("UPDATE", "blobs"))
				_, err = cache.Invalidate(ctx, "r", "", false)
			case "state-write":
				state, e := cache.State(ctx, "r")
				if e != nil {
					t.Fatal(e)
				}
				sqltest.Exec(t, s.DB(), sqltest.FailTrigger("UPDATE", "remote_sources"))
				err = cache.PutState(ctx, "r", state)
			case "source-corrupt":
				sqltest.Exec(t, s.DB(), "UPDATE remote_sources SET doc='invalid'")
				err = cache.Check(ctx, token)
			case "entry-corrupt":
				sqltest.Exec(t, s.DB(), "UPDATE remote_cache SET doc='invalid'")
				_, err = cache.Read(ctx, key)
			case "key-corrupt":
				sqltest.Exec(
					t,
					s.DB(),
					"UPDATE remote_cache SET doc=json_set(doc,'$.token.key.path','other')",
				)
				_, err = cache.Read(ctx, key)
				if !errors.Is(err, kernel.ErrConflict) {
					t.Fatal("key collision reused representation", err)
				}
				return
			case "list-corrupt":
				sqltest.Exec(t, s.DB(), "UPDATE remote_cache SET doc='invalid'")
				_, _, err = cache.List(ctx, "r", "", kernel.Page{})
			case "list-table":
				sqltest.Exec(t, s.DB(), "DROP TABLE remote_cache")
				_, err = cache.Invalidate(ctx, "r", "", false)
			case "missing-checkpoint":
				sqltest.Exec(t, s.DB(), "DELETE FROM remote_cache")
				err = cache.Check(ctx, token)
				if !errors.Is(err, kernel.ErrConflict) {
					t.Fatal("lost checkpoint accepted", err)
				}
				return
			}
			wantDriverError(t, fault, err)
			switch fault {
			case "publish", "reference", "release", "invalidate-write", "invalidate-release":
				saved, e := cache.Read(ctx, key)
				if e != nil || saved.Blob.Checksums != old.Blob.Checksums ||
					saved.Token != old.Token {
					t.Fatal("failed write replaced old state", e)
				}
				ref, e := s.Blobs().Get(ctx, old.Blob.Checksums.SHA256)
				if e != nil || ref.Refs != 1 {
					t.Fatal("failed write changed old reference", e)
				}
				if _, e = s.Blobs().
					Get(ctx, next.Blob.Checksums.SHA256); !errors.Is(
					e,
					kernel.ErrNotFound,
				) {
					t.Fatal("failed write leaked new reference", e)
				}
			}
		})
	}
}

func TestAccRemoteCacheClosedAndBadPage(t *testing.T) {
	s := mustOpenFile(t, filepath.Join(t.TempDir(), "cache.db"), nil)
	ctx := context.Background()
	c := s.RemoteCache()
	if _, _, err := c.List(
		ctx,
		"r",
		"",
		kernel.Page{After: kernel.Cursor("a", "b")},
	); !errors.Is(
		err,
		kernel.ErrInvalidCursor,
	) {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := c.Source(ctx, "r")
	wantDriverError(t, "source", err)
	_, err = c.Read(ctx, model.RemoteCacheKey{RepoKey: "r", Path: "a"})
	wantDriverError(t, "read", err)
	_, err = c.State(ctx, "r")
	wantDriverError(t, "state", err)
	err = c.Revalidate(ctx, model.RemoteCacheToken{}, time.Now())
	wantDriverError(t, "revalidate", err)
}
