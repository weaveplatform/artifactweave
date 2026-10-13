package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/crypt"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqltest"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/storetest"
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

func TestContractFile(t *testing.T) {
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

func TestContractMemory(t *testing.T) {
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

func TestMigrationsUpDownUp(t *testing.T) {
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

func TestOpenErrors(t *testing.T) {
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

func TestIsUniqueViolation(t *testing.T) {
	if sqlite.IsUniqueViolation(errors.New("plain")) {
		t.Fatal("a plain error is not a unique violation")
	}
}

func TestKeyRotation(t *testing.T) {
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
