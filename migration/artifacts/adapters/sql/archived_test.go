package sql_test

import (
	"context"
	"testing"
	"time"

	artsql "github.com/weaveplatform/artifactweave/migration/artifacts/adapters/sql"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/sqltest"
)

// The contract suite covers the archived item store's behaviour; these
// drive its driver-failure paths, which a healthy database never takes: a
// closed pool and rows corrupted behind the store's back. They use SQLite
// because its dynamic typing lets a test store a value the column type
// forbids.

// seedArchived stores an archived item and returns the ID the store
// assigned.
func seedArchived(t *testing.T, s *artsql.Store) int64 {
	t.Helper()
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	it, err := s.ArchivedItems().AddItem(context.Background(), model.ArchivedItem{
		Item: model.Item{RepoKey: "libs", Path: ".", Name: "a.jar"}, ExpiresAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return it.ID
}

func TestAccArchivedItemsClosedDatabase(t *testing.T) {
	s := open(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ai := s.ArchivedItems()
	item := model.ArchivedItem{Item: model.Item{RepoKey: "r", Path: ".", Name: "n"}}
	sqltest.Fails(t, map[string]error{
		"Archive.AddItem":      sqltest.Err(ai.AddItem(ctx, item)),
		"Archive.Item":         sqltest.Err(ai.Item(ctx, 1)),
		"Archive.FindItems":    sqltest.Err(ai.FindItems(ctx, "r", "n")),
		"Archive.Items":        sqltest.Err3(ai.Items(ctx, port.ArchivedFilter{}, kernel.Page{})),
		"Archive.DeleteItem":   ai.DeleteItem(ctx, 1),
		"Archive.ExpiredItems": sqltest.Err(ai.ExpiredItems(ctx, time.Now(), 0)),
	})
}

func TestAccArchivedItemsCorruptRows(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	archived := seedArchived(t, s)
	ai := s.ArchivedItems()
	// Text where a JSON document belongs.
	sqltest.Exec(t, s.DB(), "UPDATE archived_items SET item = '{'")
	later := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	sqltest.Fails(t, map[string]error{
		"Archive.Item":         sqltest.Err(ai.Item(ctx, archived)),
		"Archive.Items":        sqltest.Err3(ai.Items(ctx, port.ArchivedFilter{}, kernel.Page{})),
		"Archive.ExpiredItems": sqltest.Err(ai.ExpiredItems(ctx, later, 0)),
	})
	// A second round reaches the checks the first round's failures stop
	// short of.
	sqltest.Exec(t, s.DB(), "UPDATE archived_items SET item = '{}', properties = '{'")
	sqltest.Fails(t, map[string]error{
		"Archive.FindItems": sqltest.Err(ai.FindItems(ctx, "libs", "a.jar")),
	})
}
