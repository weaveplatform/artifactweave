package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	appsql "github.com/weaveplatform/artifactweave/migration/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/migration/app/adapters/sql/sqlite"
	appstoretest "github.com/weaveplatform/artifactweave/migration/app/storetest"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/crypt"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
	sqlitedrv "github.com/weaveplatform/artifactweave/migration/platform/sql/sqlite"
	platsqltest "github.com/weaveplatform/artifactweave/migration/platform/sql/sqltest"
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
	appstoretest.Run(t, func(t *testing.T) appstoretest.Store {
		s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "aw.db"),
			sqlite.Options{Options: appsql.Options{Keyring: keyring(t)}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

func TestAccContractMemory(t *testing.T) {
	appstoretest.Run(t, func(t *testing.T) appstoretest.Store {
		s, err := sqlite.Open(context.Background(), sqlitedrv.Memory,
			sqlite.Options{Options: appsql.Options{Keyring: keyring(t)}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

func TestAccMigrationsUpDownUp(t *testing.T) {
	ctx := context.Background()
	s, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "aw.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	mods := appsql.Modules()
	streams, err := dialect.Version(ctx, s.DB(), sqlitedrv.Dialect, mods)
	if err != nil || len(streams) != len(mods) {
		t.Fatalf("Version = %v, %v", streams, err)
	}
	for _, v := range streams {
		if v.Current != v.Latest || v.Latest == 0 {
			t.Errorf("stream %s at version %d of %d after Open", v.Module, v.Current, v.Latest)
		}
	}
	// Versions may skip numbers (parallel branches reserve them), so count
	// the applied migrations rather than assuming 1..latest.
	var applied int
	if err := s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").
		Scan(&applied); err != nil || applied < len(mods) {
		t.Fatalf("applied migrations = %d, %v", applied, err)
	}
	down, err := dialect.Rollback(ctx, s.DB(), sqlitedrv.Dialect, mods, "", 0)
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
	up, err := dialect.Migrate(ctx, s.DB(), sqlitedrv.Dialect, mods, "")
	if err != nil || len(up) != applied {
		t.Fatalf("Migrate = %v, %v", up, err)
	}
	// The streams applied again build the schema a fresh database has.
	golden := filepath.Join("testdata", "schema.golden")
	platsqltest.SchemaGolden(t, s.DB(), sqlitedrv.Dialect.Name, golden, false)
}

// TestAccLegacyHistoryRefused opens a database whose history predates the
// per-module migration streams: the store and the migrator refuse it and
// write nothing.
func TestAccLegacyHistoryRefused(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := sqlite.Open(ctx, path, sqlite.Options{Options: appsql.Options{SkipMigrate: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	appstoretest.LegacyHistoryRefused(t, s.DB(), sqlitedrv.Dialect, func() error {
		refused, err := sqlite.Open(ctx, path, sqlite.Options{})
		if err == nil {
			_ = refused.Close()
		}
		return err
	})
}

func TestAccOpenErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := sqlite.Open(
		ctx,
		"",
		sqlite.Options{},
	); !errors.Is(
		err,
		sqlitedrv.ErrPathRequired,
	) {
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
		sqlite.Options{Options: appsql.Options{SkipMigrate: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.Blobs().Get(ctx, "x"); err == nil || errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("unmigrated database: want a driver error, got %v", err)
	}
}

func TestAccKeyRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aw.db")
	appstoretest.Rotation(t, func(t *testing.T, k *crypt.Keyring) *appsql.Store {
		s, err := sqlite.Open(
			context.Background(),
			path,
			sqlite.Options{Options: appsql.Options{Keyring: k}},
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
	open := func() *appsql.Store {
		s, err := sqlite.Open(context.Background(), path, sqlite.Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	appstoretest.RemoteLayoutPersistence(t, open(), open)
}

func TestAccRepositoryHeaderSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "headers.db")
	appstoretest.RepositoryHeaderEnvironmentRename(
		t,
		func(t *testing.T, k *crypt.Keyring) *appsql.Store {
			s, err := sqlite.Open(
				t.Context(),
				path,
				sqlite.Options{Options: appsql.Options{Keyring: k}},
			)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			return s
		},
	)
}

func TestAccRepositoryHeaderMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "headers.db")
	appstoretest.RepositoryHeadersOpen(t, func(t *testing.T, k *crypt.Keyring) *appsql.Store {
		s, err := sqlite.Open(
			t.Context(),
			path,
			sqlite.Options{Options: appsql.Options{Keyring: k}},
		)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}
