package sql_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	artsql "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/sql"
	artstoretest "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/storetest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

// TestAccPackageSearchContract runs the npm and NuGet search contracts on
// SQLite and PostgreSQL: the fold and the run tests are written per
// engine, and must find the same candidates on both.
func TestAccPackageSearchContract(t *testing.T) {
	sqltest.RunModule(t, mods, artsql.New,
		func(t *testing.T, newStore func(*testing.T) *artsql.Store) {
			factory := func(t *testing.T) port.Store { return newStore(t) }
			artstoretest.RunNpmSearch(t, factory)
			artstoretest.RunNuGetSearch(t, factory)
		}, sqltest.WithPostgres())
}

// TestAccPackageSearchAtScale pages late into 5000 packages on each
// engine and keeps the query plans: a page of candidates is a seek of
// packages_name in name order, which needs no sort, each package's
// versions tested through an index on package_id; the page's versions are
// read through an index too.
func TestAccPackageSearchAtScale(t *testing.T) {
	sqltest.RunModule(t, mods, artsql.New,
		func(t *testing.T, newStore func(*testing.T) *artsql.Store) {
			searchAtScale(t, newStore(t))
		}, sqltest.WithPostgres())
}

func searchAtScale(t *testing.T, s *artsql.Store) {
	t.Helper()
	ctx := t.Context()
	const count = 5000
	name := func(i int) string { return fmt.Sprintf("pkg-%05d", i) }
	err := s.Tx(ctx, func(ctx context.Context) error {
		for i := range count {
			p, err := s.Packages().Ensure(ctx,
				model.Package{RepoKey: "npm", Type: repo.NPM, Name: name(i)})
			if err != nil {
				return err
			}
			if _, err := s.Packages().PutVersion(ctx, model.PackageVersion{
				PackageID: p.ID, Version: "1.0.0",
				Metadata: fmt.Appendf(nil, `{"manifest":{"description":"Package number %d"}}`, i),
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := s.Dialect().Name
	if s.Dialect().Dollar {
		// As autovacuum would after a load this size: without statistics
		// the planner takes the table for a few rows.
		if _, err := s.DB().ExecContext(ctx, "ANALYZE packages, package_versions"); err != nil {
			t.Fatal(err)
		}
	}
	late := kernel.Page{After: kernel.Cursor(name(count - 21)), Limit: 20}
	for _, c := range []struct {
		what  string
		terms []string
		page  kernel.Page
		first int
		n     int
	}{
		{"late page", nil, late, count - 20, 20},
		{"late page of a term", []string{"number"}, late, count - 20, 20},
		{"a late package's term", []string{"04990"}, kernel.Page{Limit: 20}, 4990, 1},
	} {
		start := time.Now()
		q := port.NpmSearch{RepoKey: "npm", Terms: c.terms}
		found, next, err := s.Packages().SearchNpm(ctx, q, c.page)
		if err != nil || len(found) != c.n || next != "" {
			t.Fatalf("%s: %d found, next %q, %v", c.what, len(found), next, err)
		}
		for i, f := range found {
			if f.Package.Name != name(c.first+i) || len(f.Versions) != 1 {
				t.Fatalf("%s: %d is %s with %d versions",
					c.what, i, f.Package.Name, len(f.Versions))
			}
		}
		t.Logf("%s: %s: %d of %d packages: %s", engine, c.what, c.n, count, time.Since(start))
	}
	for _, c := range []struct {
		what   string
		prefix string
		page   kernel.Page
	}{
		{"first page", "", kernel.Page{Limit: 20}},
		{"late page", "", late},
		// An autocomplete's, or a feed of one id's versions.
		{"late prefix", name(count - 10)[:8], kernel.Page{Limit: 20}},
	} {
		q, args, err := artsql.SearchQuery(s, "npm", []string{"number"}, c.prefix, c.page)
		if err != nil {
			t.Fatal(err)
		}
		plan := explain(t, s, q, args)
		t.Logf("%s: %s of candidates:\n%s", engine, c.what, plan)
		wantSeek(t, plan, "packages_name")
	}
	ids := make([]any, 20)
	for i := range ids {
		ids[i] = int64(count - 20 + i + 1)
	}
	plan := explain(t, s, artsql.SearchVersionsQuery(len(ids)), ids)
	t.Logf("%s: the page's versions:\n%s", engine, plan)
	// SQLite reads them in order through package_versions_order;
	// PostgreSQL may read package_versions_version, already in package_id
	// order, and sort each package's own versions (an incremental sort),
	// which bounds the sort by the page's versions.
	if !strings.Contains(plan, "package_versions_order") &&
		!strings.Contains(plan, "Presorted Key: package_id") {
		t.Errorf("the page's versions are not read in package order")
	}
	if strings.Contains(plan, "TEMP B-TREE") {
		t.Errorf("the page's versions are sorted whole")
	}
}

// explain is the engine's plan of q: SQLite's EXPLAIN QUERY PLAN, and
// PostgreSQL's EXPLAIN ANALYZE, which runs it.
func explain(t *testing.T, s *artsql.Store, q string, args []any) string {
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

// wantSeek fails unless plan reads through index, and sorts nothing: the
// index gives the order.
func wantSeek(t *testing.T, plan, index string) {
	t.Helper()
	if !strings.Contains(plan, index) {
		t.Errorf("plan does not use %s", index)
	}
	for _, sort := range []string{"TEMP B-TREE", "Sort"} {
		if strings.Contains(plan, sort) {
			t.Errorf("plan sorts (%s)", sort)
		}
	}
}
