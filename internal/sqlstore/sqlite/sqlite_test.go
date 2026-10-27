package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/crypt"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqltest"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/storetest"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// keyring seals secret columns in the contract runs, so the sealed paths are
// the ones under test.
func keyring(t *testing.T) *crypt.Keyring {
	t.Helper()
	k, err := crypt.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestAccContractFile(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store {
		s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "aw.db"),
			sqlite.Options{Options: sqlcommon.Options{Keyring: keyring(t)}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

func TestAccContractMemory(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store {
		s, err := sqlite.Open(context.Background(), sqlite.Memory,
			sqlite.Options{Options: sqlcommon.Options{Keyring: keyring(t)}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

func TestAccP9bPackageIndexesSurviveRestart(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "formats.db")
	open := func() *sqlcommon.Store {
		t.Helper()
		s, err := sqlite.Open(ctx, path, sqlite.Options{})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := open()
	formats := []repo.PackageType{
		repo.LuaRocks, repo.NIMModel, repo.Skills,
		repo.AgentPlugins, repo.AgentPackages,
	}
	for _, format := range formats {
		key := string(format) + "-repo"
		p, err := s.Packages().Ensure(ctx, types.Package{
			RepoKey: key, Type: format, Name: "example",
		})
		if err != nil {
			t.Fatalf("%s package: %v", format, err)
		}
		if _, err := s.Packages().PutVersion(ctx, types.PackageVersion{
			PackageID: p.ID, Version: "1.2.3", Metadata: []byte(`{"qualified":true}`),
		}); err != nil {
			t.Fatalf("%s version: %v", format, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = open()
	defer func() { _ = s.Close() }()
	for _, format := range formats {
		key := string(format) + "-repo"
		p, err := s.Packages().Find(ctx, key, "example")
		if err != nil || p.Type != format {
			t.Fatalf("%s package after restart: %+v, %v", format, p, err)
		}
		v, err := s.Packages().Version(ctx, p.ID, "1.2.3")
		if err != nil || string(v.Metadata) != `{"qualified":true}` {
			t.Fatalf("%s version after restart: %+v, %v", format, v, err)
		}
	}
}

func TestAccGitLFSLocksSurviveRestart(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "gitlfs-locks.db")
	open := func() *sqlcommon.Store {
		t.Helper()
		s, err := sqlite.Open(ctx, path, sqlite.Options{})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := open()
	if err := s.Repos().Create(ctx, repo.Config{
		Key: "lfs-local", Class: repo.Local, PackageType: repo.GitLFS,
	}); err != nil {
		t.Fatal(err)
	}
	lock := types.GitLFSLock{
		ID: "durable-1", RepoKey: "lfs-local", Path: "src/a.bin", Owner: "alice",
		LockedAt: time.Date(2026, 10, 26, 12, 0, 0, 123456000, time.UTC),
	}
	if err := s.GitLFSLocks().Create(ctx, lock); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = open()
	got, err := s.GitLFSLocks().Get(ctx, lock.RepoKey, lock.ID)
	if err != nil || got != lock {
		t.Fatalf("lock after restart = %+v, %v; want %+v", got, err, lock)
	}
	rows, next, err := s.GitLFSLocks().List(ctx,
		store.GitLFSLockFilter{RepoKey: lock.RepoKey}, store.Page{Limit: 1})
	if err != nil || next != "" || len(rows) != 1 || rows[0] != lock {
		t.Fatalf("lock list after restart = %+v, %q, %v", rows, next, err)
	}
	if _, err := s.GitLFSLocks().Delete(ctx, lock.RepoKey, lock.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = open()
	defer func() { _ = s.Close() }()
	_, err = s.GitLFSLocks().Get(ctx, lock.RepoKey, lock.ID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted lock after restart: %v, want not found", err)
	}
}

func TestAccMigrationsUpDownUp(t *testing.T) {
	ctx := context.Background()
	s, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "aw.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	cur, latest, err := sqlcommon.Version(ctx, s.DB(), sqlite.Dialect)
	if err != nil || cur != latest || latest == 0 {
		t.Fatalf("Version = %d, %d, %v", cur, latest, err)
	}
	// Versions may skip numbers (parallel branches reserve them), so count
	// the applied migrations rather than assuming 1..latest.
	var applied int
	if err := s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").
		Scan(&applied); err != nil || applied == 0 {
		t.Fatalf("applied migrations = %d, %v", applied, err)
	}
	down, err := sqlcommon.Rollback(ctx, s.DB(), sqlite.Dialect, 0)
	if err != nil || len(down) != applied {
		t.Fatalf("Rollback = %v, %v", down, err)
	}
	// Every table is gone, so nothing but the version table is left.
	var n int
	if err := s.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT IN ('schema_migrations', 'sqlite_sequence')",
	).
		Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d tables left after rolling back, %v", n, err)
	}
	up, err := sqlcommon.Migrate(ctx, s.DB(), sqlite.Dialect)
	if err != nil || len(up) != applied {
		t.Fatalf("Migrate = %v, %v", up, err)
	}
}

func TestAccOpenErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := sqlite.Open(ctx, "", sqlite.Options{}); !errors.Is(err, sqlite.ErrPathRequired) {
		t.Fatalf("empty path: %v", err)
	}
	if _, err := sqlite.Open(
		ctx,
		filepath.Join(t.TempDir(), "missing", "aw.db"),
		sqlite.Options{},
	); err == nil {
		t.Fatal("a database in a missing directory opened")
	}
	s, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "raw.db"),
		sqlite.Options{Options: sqlcommon.Options{SkipMigrate: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.Blobs().Get(ctx, "x"); err == nil || errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unmigrated database: want a driver error, got %v", err)
	}
}

func TestAccIsUniqueViolation(t *testing.T) {
	if sqlite.IsUniqueViolation(errors.New("plain")) {
		t.Fatal("a plain error is not a unique violation")
	}
}

func TestAccKeyRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aw.db")
	sqltest.Rotation(t, func(t *testing.T, k *crypt.Keyring) *sqlcommon.Store {
		s, err := sqlite.Open(
			context.Background(),
			path,
			sqlite.Options{Options: sqlcommon.Options{Keyring: k}},
		)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

func TestAccRemoteCachePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote.db")
	open := func() *sqlcommon.Store {
		s, err := sqlite.Open(context.Background(), path, sqlite.Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	sqltest.RemoteCachePersistence(t, open(), open)
}

func TestAccRepositoryHeaderSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "headers.db")
	sqltest.RepositoryHeaders(t, func(t *testing.T, k *crypt.Keyring) *sqlcommon.Store {
		s, err := sqlite.Open(
			t.Context(),
			path,
			sqlite.Options{Options: sqlcommon.Options{Keyring: k}},
		)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

func TestAccRepositoryHeaderMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "headers.db")
	sqltest.RepositoryHeadersMigration(t, func(t *testing.T, k *crypt.Keyring) *sqlcommon.Store {
		s, err := sqlite.Open(
			t.Context(),
			path,
			sqlite.Options{Options: sqlcommon.Options{Keyring: k}},
		)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}
