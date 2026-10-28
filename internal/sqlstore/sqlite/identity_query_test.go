package sqlite_test

import (
	"path/filepath"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqltest"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

func TestAccIdentityPagingAtScale(t *testing.T) {
	s, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "paging.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	sqltest.IdentityPaging(t, s)
}

func TestAccIdentitySearchUpgrade(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sqlcommon.Rollback(ctx, s.DB(), sqlite.Dialect, 17); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"INSERT INTO users (name) VALUES ('ÉLODIE_%')",
		"INSERT INTO user_groups (name) VALUES ('ÉQUIPE_%')",
	} {
		if _, err := s.DB().ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// A second open also proves completed backfills are repeatable.
	for range 2 {
		s, err = sqlite.Open(ctx, path, sqlite.Options{})
		if err != nil {
			t.Fatal(err)
		}
		users, _, err := s.Users().ListSummaries(ctx, store.UserFilter{
			IdentityFilter: store.IdentityFilter{NameContains: "élodie_%"},
		}, store.Page{Limit: 1})
		if err != nil || len(users) != 1 || users[0].Name != "ÉLODIE_%" {
			t.Fatalf("upgraded users = %v, %v", users, err)
		}
		groups, _, err := s.Groups().
			ListNames(ctx, store.IdentityFilter{NameContains: "équipe_%"}, store.Page{Limit: 1})
		if err != nil || len(groups) != 1 || groups[0] != "ÉQUIPE_%" {
			t.Fatalf("upgraded groups = %v, %v", groups, err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
