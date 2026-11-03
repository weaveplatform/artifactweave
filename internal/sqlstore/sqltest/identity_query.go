package sqltest

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// IdentityPaging exercises small first/late pages over a larger data set and
// retains the query plan for the name continuation used by ListSummaries.
func IdentityPaging(t *testing.T, s *sqlcommon.Store) {
	t.Helper()
	ctx := t.Context()
	const count = 5000
	err := s.Tx(ctx, func(ctx context.Context) error {
		for i := range count {
			name := fmt.Sprintf("user-%05d", i)
			if err := s.Users().Create(ctx, types.User{Name: name}); err != nil {
				return fmt.Errorf("seed identity page: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, first := range []int{0, count - 11} {
		page := store.Page{Limit: 10}
		if first > 0 {
			page.After = store.Cursor(fmt.Sprintf("user-%05d", first-1))
		}
		start := time.Now()
		rows, next, err := s.Users().ListSummaries(ctx, store.UserFilter{}, page)
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
