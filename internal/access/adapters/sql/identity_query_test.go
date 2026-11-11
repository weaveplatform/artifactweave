package sql_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	accsql "github.com/weaveplatform/artifactweave/internal/access/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

// TestAccIdentityPagingAtScale runs identityPaging on SQLite and, unlike the
// module's contract run, on PostgreSQL too: each engine plans the late
// page's seek in its own way.
func TestAccIdentityPagingAtScale(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{accsql.Module}, accsql.New,
		func(t *testing.T, newStore func(*testing.T) *accsql.Store) {
			identityPaging(t, newStore(t))
		}, sqltest.WithPostgres())
}

// identityPaging exercises small first/late pages over a larger data set and
// retains the query plan for the name continuation used by ListSummaries.
func identityPaging(t *testing.T, s *accsql.Store) {
	t.Helper()
	ctx := t.Context()
	const count = 5000
	err := s.Tx(ctx, func(ctx context.Context) error {
		for i := range count {
			name := fmt.Sprintf("user-%05d", i)
			if err := s.Users().Create(ctx, model.User{Name: name}); err != nil {
				return fmt.Errorf("seed identity page: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, first := range []int{0, count - 11} {
		page := kernel.Page{Limit: 10}
		if first > 0 {
			page.After = kernel.Cursor(fmt.Sprintf("user-%05d", first-1))
		}
		start := time.Now()
		rows, next, err := s.Users().ListSummaries(ctx, port.UserFilter{}, page)
		if err != nil || len(rows) != 10 || next == "" {
			t.Fatalf("page at %d = %v, %q, %v", first, rows, next, err)
		}
		for i, row := range rows {
			if row.Name != fmt.Sprintf("user-%05d", first+i) {
				t.Fatalf("page at %d has wrong identity %q", first, row.Name)
			}
		}
		t.Logf(
			"%s: 10 of %d users, start %d: %s",
			s.Dialect().Name,
			count,
			first,
			time.Since(start),
		)
	}
	query := "SELECT i.name, i.realm, COALESCE(NULLIF(i.status, ''), 'enabled') " +
		"FROM users i WHERE i.name > ? ORDER BY i.name ASC LIMIT ?"
	explain := "EXPLAIN QUERY PLAN "
	if s.Dialect().Dollar {
		explain = "EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT) "
	}
	rows, err := s.DB().QueryContext(ctx, s.Dialect().Rebind(explain+query), "user-04988", 11)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if s.Dialect().Dollar {
			err = rows.Scan(&line)
		} else {
			var id, parent, unused int
			err = rows.Scan(&id, &parent, &unused, &line)
		}
		if err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Logf("late-page query plan:\n%s", plan.String())
	if !strings.Contains(strings.ToLower(plan.String()), "index") {
		t.Fatal("late page did not seek using an index")
	}
}

// TestAccIdentitySearchUpgrade checks that the access module's AfterMigrate
// hook fills the search names of rows written without one, so the name
// filters find them.
func TestAccIdentitySearchUpgrade(t *testing.T) {
	ctx := t.Context()
	db := sqltest.OpenSQLite(t, accsql.Module)
	// Rows with no search name, which the hook fills when it next runs, as
	// every open runs it.
	sqltest.Exec(t, db.DB(),
		"INSERT INTO users (name) VALUES ('ÉLODIE_%')",
		"INSERT INTO user_groups (name) VALUES ('ÉQUIPE_%')",
	)
	s := accsql.New(db)
	// A second run also proves completed backfills are repeatable.
	for range 2 {
		if err := accsql.Module.AfterMigrate(ctx, db); err != nil {
			t.Fatal(err)
		}
		users, _, err := s.Users().ListSummaries(ctx, port.UserFilter{
			IdentityFilter: port.IdentityFilter{NameContains: "élodie_%"},
		}, kernel.Page{Limit: 1})
		if err != nil || len(users) != 1 || users[0].Name != "ÉLODIE_%" {
			t.Fatalf("upgraded users = %v, %v", users, err)
		}
		groups, _, err := s.Groups().
			ListNames(ctx, port.IdentityFilter{NameContains: "équipe_%"}, kernel.Page{Limit: 1})
		if err != nil || len(groups) != 1 || groups[0] != "ÉQUIPE_%" {
			t.Fatalf("upgraded groups = %v, %v", groups, err)
		}
	}
}
