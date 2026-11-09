package sqlcommon_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	"github.com/weaveplatform/artifactweave/migration/platform/crypt"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The contract suite covers behaviour; these tests drive the archived item
// store's driver-failure paths, which a healthy database never takes: a
// closed pool and rows corrupted behind the store's back. They use SQLite
// because its dynamic typing lets a test store a value the column type
// forbids.

// entOpen opens a migrated SQLite store with a keyring.
func entOpen(t *testing.T) *sqlcommon.Store {
	t.Helper()
	k, err := crypt.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "aw.db"),
		sqlite.Options{Options: sqlcommon.Options{Keyring: k}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// entExec runs raw SQL against the store's database.
func entExec(t *testing.T, s *sqlcommon.Store, stmts ...string) {
	t.Helper()
	for _, q := range stmts {
		if _, err := s.DB().ExecContext(context.Background(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// entFails asserts that every call returned an error.
func entFails(t *testing.T, calls map[string]error) {
	t.Helper()
	for name, err := range calls {
		if err == nil {
			t.Errorf("%s succeeded", name)
		}
	}
}

func entErr[V any](_ V, err error) error { return err }

func entErr3[V any](_ V, _ string, err error) error { return err }

// entSeed stores an archived item and returns the ID the store assigned.
func entSeed(t *testing.T, s store.Store) int64 {
	t.Helper()
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	it, err := s.ArchivedItems().AddItem(context.Background(), types.ArchivedItem{
		Item: types.Item{RepoKey: "libs", Path: ".", Name: "a.jar"}, ExpiresAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return it.ID
}

func TestAccEnterpriseClosedDatabase(t *testing.T) {
	s := entOpen(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ai := s.ArchivedItems()
	item := types.ArchivedItem{Item: types.Item{RepoKey: "r", Path: ".", Name: "n"}}
	entFails(t, map[string]error{
		"Archive.AddItem":      entErr(ai.AddItem(ctx, item)),
		"Archive.Item":         entErr(ai.Item(ctx, 1)),
		"Archive.FindItems":    entErr(ai.FindItems(ctx, "r", "n")),
		"Archive.Items":        entErr3(ai.Items(ctx, store.ArchivedFilter{}, store.Page{})),
		"Archive.DeleteItem":   ai.DeleteItem(ctx, 1),
		"Archive.ExpiredItems": entErr(ai.ExpiredItems(ctx, time.Now(), 0)),
	})
}

func TestAccEnterpriseCorruptRows(t *testing.T) {
	s := entOpen(t)
	ctx := context.Background()
	archived := entSeed(t, s)
	ai := s.ArchivedItems()
	// Text where a JSON document belongs.
	entExec(t, s, "UPDATE archived_items SET item = '{'")
	later := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	entFails(t, map[string]error{
		"Archive.Item":         entErr(ai.Item(ctx, archived)),
		"Archive.Items":        entErr3(ai.Items(ctx, store.ArchivedFilter{}, store.Page{})),
		"Archive.ExpiredItems": entErr(ai.ExpiredItems(ctx, later, 0)),
	})
	// A second round reaches the checks the first round's failures stop
	// short of.
	entExec(t, s, "UPDATE archived_items SET item = '{}', properties = '{'")
	entFails(t, map[string]error{
		"Archive.FindItems": entErr(ai.FindItems(ctx, "libs", "a.jar")),
	})
}
