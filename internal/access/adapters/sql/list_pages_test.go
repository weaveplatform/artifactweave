package sql_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	accsql "github.com/weaveplatform/artifactweave/internal/access/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

// listScale is how many rows each list holds, the size the identity test
// pages over, so a late page's cost is comparable across the lists.
const listScale = 5000

// TestAccAccessListsAtScale reads the first and a late page of the Access
// lists that page in the store (#1408): the v2 permissions, every one and
// those granting to a filter's principals, the group names and the API
// restriction audit, each over 5000 rows, on SQLite and PostgreSQL. It logs
// each page's timing and the late page's plan, and checks that the seeks the
// late pages rely on read through an index rather than every row.
func TestAccAccessListsAtScale(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{accsql.Module}, accsql.New,
		func(t *testing.T, newStore func(*testing.T) *accsql.Store) {
			s := newStore(t)
			seedAccessLists(t, s)
			permissionPagesAtScale(t, s)
			groupPagesAtScale(t, s)
			auditPagesAtScale(t, s)
		}, sqltest.WithPostgres())
}

// seedAccessLists stores listScale permissions, every one granting the
// group devs and every seventh the user bob, listScale groups, and
// listScale restriction changes alternating between bob and carol.
func seedAccessLists(t *testing.T, s *accsql.Store) {
	t.Helper()
	ctx := t.Context()
	changed := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	err := s.Tx(ctx, func(ctx context.Context) error {
		for _, u := range []string{"bob", "carol"} {
			if err := s.Users().Create(ctx, model.User{Name: u}); err != nil {
				return fmt.Errorf("seed %s: %w", u, err)
			}
		}
		for i := range listScale {
			users := map[string]api.Actions{}
			if i%7 == 0 {
				users["bob"] = api.ActionRead
			}
			p := model.Permission{
				Name: fmt.Sprintf("perm-%05d", i),
				Resources: map[api.ResourceType]model.Resource{api.ResourceArtifact: {
					Actions: model.Grants{Users: users, Groups: map[string]api.Actions{"devs": api.ActionRead}},
					Targets: map[string]model.Target{"libs-local": {Include: []string{"**"}}},
				}},
			}
			if err := s.Permissions().Create(ctx, p); err != nil {
				return fmt.Errorf("seed %s: %w", p.Name, err)
			}
			if err := s.Groups().Create(ctx, model.Group{Name: fmt.Sprintf("group-%05d", i)}); err != nil {
				return fmt.Errorf("seed group %d: %w", i, err)
			}
			user := "bob"
			if i%2 == 1 {
				user = "carol"
			}
			if _, err := s.APIRestrictions().Put(ctx, port.APIRestriction{
				User: user, Actor: "admin", Reason: "scale",
				Changed: changed.Add(time.Duration(i) * time.Second),
			}); err != nil {
				return fmt.Errorf("seed restriction %d: %w", i, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, "ANALYZE"); err != nil {
		t.Fatal(err)
	}
}

func permissionPagesAtScale(t *testing.T, s *accsql.Store) {
	t.Helper()
	ctx := t.Context()
	// bob's grants are perm-00000, perm-00007, ... perm-04998: 715 of them.
	bob := port.PermissionQuery{Granting: true, Users: []string{"bob"}}
	for _, c := range []struct {
		who            string
		q              port.PermissionQuery
		after          string
		first, lastKey string
	}{
		{"every permission", port.PermissionQuery{}, "", "perm-00000", "perm-00009"},
		{"every permission", port.PermissionQuery{}, "perm-04988", "perm-04989", "perm-04998"},
		{"bob's", bob, "", "perm-00000", "perm-00063"},
		{"bob's", bob, "perm-04920", "perm-04921", "perm-04984"},
	} {
		q := c.q
		q.After, q.Limit = c.after, 10
		start := time.Now()
		ps, more, err := s.Permissions().Page(ctx, q)
		if err != nil || len(ps) != 10 || !more || ps[0].Name != c.first || ps[9].Name != c.lastKey {
			t.Fatalf("%s after %q = %d, more %v, %v", c.who, c.after, len(ps), more, err)
		}
		t.Logf("%s: %s, 10 permissions after %q: %s", s.Dialect().Name, c.who, c.after, time.Since(start))
		if c.after == "" {
			continue
		}
		query, args := accsql.PermissionPageQuery(q)
		plan := explainAccess(t, s, query, args...)
		t.Logf("%s: %s late page plan:\n%s", s.Dialect().Name, c.who, plan)
		if !q.Granting {
			requireIndex(t, plan)
		}
	}
}

func groupPagesAtScale(t *testing.T, s *accsql.Store) {
	t.Helper()
	ctx := t.Context()
	for _, after := range []string{"", "group-04988"} {
		page := kernel.Page{Limit: 10}
		if after != "" {
			page.After = kernel.Cursor(after)
		}
		start := time.Now()
		names, next, err := s.Groups().ListNames(ctx, port.IdentityFilter{}, page)
		if err != nil || len(names) != 10 || next == "" {
			t.Fatalf("groups after %q = %v, %q, %v", after, names, next, err)
		}
		t.Logf("%s: 10 of %d groups after %q: %s", s.Dialect().Name, listScale, after, time.Since(start))
	}
	plan := explainAccess(t, s,
		"SELECT i.name FROM user_groups i WHERE i.name > ? ORDER BY i.name ASC LIMIT ?", "group-04988", 11)
	t.Logf("%s: groups late page plan:\n%s", s.Dialect().Name, plan)
	requireIndex(t, plan)
}

func auditPagesAtScale(t *testing.T, s *accsql.Store) {
	t.Helper()
	ctx := t.Context()
	for _, user := range []string{"", "bob"} {
		var after int64
		for _, late := range []bool{false, true} {
			page := kernel.Page{Limit: 10}
			if late {
				// Below the 24th change, which the newest-first walk
				// reaches last; bob made every other one, so twelve of
				// his remain.
				after = 24
				page.After = kernel.Cursor(strconv.FormatInt(after, 10))
			}
			start := time.Now()
			rows, next, err := s.APIRestrictions().History(ctx, user, page)
			if err != nil || len(rows) != 10 || next == "" {
				t.Fatalf("audit of %q after %d = %d, %q, %v", user, after, len(rows), next, err)
			}
			if late && rows[0].Sequence >= after {
				t.Fatalf("audit of %q after %d starts at %d", user, after, rows[0].Sequence)
			}
			t.Logf("%s: 10 of the audit of %q after %d: %s", s.Dialect().Name, user, after, time.Since(start))
		}
	}
	for _, c := range []struct {
		who   string
		query string
		args  []any
	}{
		{"every user's", "SELECT id FROM api_restriction_audit WHERE 1 = 1 AND id < ? ORDER BY id DESC LIMIT ?",
			[]any{24, 11}},
		{"bob's", "SELECT id FROM api_restriction_audit WHERE 1 = 1 AND user_name = ? AND id < ? " +
			"ORDER BY id DESC LIMIT ?", []any{"bob", 24, 11}},
	} {
		plan := explainAccess(t, s, c.query, c.args...)
		t.Logf("%s: %s audit late page plan:\n%s", s.Dialect().Name, c.who, plan)
		requireIndex(t, plan)
	}
}

// explainAccess returns q's plan: SQLite's query plan, or PostgreSQL's
// analysed plan, which also shows the rows each step read.
func explainAccess(t *testing.T, s *accsql.Store, q string, args ...any) string {
	t.Helper()
	prefix := "EXPLAIN QUERY PLAN "
	if s.Dialect().Dollar {
		prefix = "EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT) "
	}
	rows, err := s.DB().QueryContext(t.Context(), s.Dialect().Rebind(prefix+q), args...)
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
	return plan.String()
}

// requireIndex fails unless plan reads through an index. SQLite names the
// rowid seek "INTEGER PRIMARY KEY"; both engines name every other index.
func requireIndex(t *testing.T, plan string) {
	t.Helper()
	p := strings.ToLower(plan)
	if !strings.Contains(p, "index") && !strings.Contains(p, "integer primary key") {
		t.Fatalf("the late page reads no index:\n%s", plan)
	}
}
