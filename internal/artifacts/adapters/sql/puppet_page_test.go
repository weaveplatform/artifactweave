package sql_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	artsql "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/sql"
	artstoretest "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/storetest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

// TestAccVersionNamesContract runs the version names contract on SQLite,
// in memory and in a file, and on PostgreSQL, whose collation could order
// the names apart from Versions if the two queries differed.
func TestAccVersionNamesContract(t *testing.T) {
	sqltest.RunModule(t, mods, artsql.New,
		func(t *testing.T, newStore func(*testing.T) *artsql.Store) {
			artstoretest.RunVersionNames(t, func(t *testing.T) port.Store { return newStore(t) })
		}, sqltest.WithPostgres())
}

// TestAccVersionNamesAtScale reads the names of a module of 5,000 versions
// beside another as large, as a late page of a Puppet, Ansible or Chef list
// does before it reads its page's records (#1404), on SQLite and on
// PostgreSQL, and keeps the plans of the two queries such a page makes:
// the names, answered from package_versions_order in its own order with no
// sort, and each page row's record, one seek on package_versions_version.
func TestAccVersionNamesAtScale(t *testing.T) {
	sqltest.RunModule(t, mods, artsql.New,
		func(t *testing.T, newStore func(*testing.T) *artsql.Store) {
			versionNamesAtScale(t, newStore(t))
		}, sqltest.WithPostgres())
}

func versionNamesAtScale(t *testing.T, s *artsql.Store) {
	t.Helper()
	ctx := t.Context()
	const count = 5000
	var target model.Package
	for _, name := range []string{"acme-other", "acme-widget"} {
		p, err := s.Packages().Ensure(ctx, model.Package{
			RepoKey: "puppet-local", Type: repo.Puppet, Name: name,
		})
		if err != nil {
			t.Fatal(err)
		}
		target = p
		// The rows are written directly, with the order keys PutVersion
		// would give them, as 5,000 PutVersion calls would reorder the
		// module on each.
		tx, err := s.DB().BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		insert, err := tx.PrepareContext(ctx, s.Dialect().Rebind("INSERT INTO package_versions "+
			"(package_id, version, order_key, metadata) VALUES (?, ?, ?, ?)"))
		if err != nil {
			t.Fatal(err)
		}
		// Written newest first, so the rows' own order is not the
		// names' order.
		for i := count - 1; i >= 0; i-- {
			version := fmt.Sprintf("1.0.%d", i)
			if _, err := insert.ExecContext(ctx, p.ID, version, i+1,
				`{"name":"`+name+`","version":"`+version+`"}`); err != nil {
				t.Fatal(err)
			}
		}
		if err := insert.Close(); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	// What autovacuum does for a table this size on its own; it leaves the
	// visibility map set, which an index-only scan relies on.
	if s.Dialect().Dollar {
		if _, err := s.DB().ExecContext(ctx, "VACUUM ANALYZE package_versions"); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	names, err := s.Packages().VersionNames(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: %d version names: %s", s.Dialect().Name, len(names), time.Since(start))
	versions, err := s.Packages().Versions(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := make([]string, 0, count)
	for i := range count {
		want = append(want, fmt.Sprintf("1.0.%d", i))
	}
	got := make([]string, 0, len(versions))
	for _, v := range versions {
		got = append(got, v.Version)
	}
	if !slices.Equal(names, want) || !slices.Equal(got, want) {
		t.Fatalf("names %d, versions %d, not in order", len(names), len(versions))
	}
	// The plan of the names, with no sort: no temporary B-tree on SQLite,
	// no Sort node on PostgreSQL.
	plan := explain(t, s, "SELECT version FROM package_versions WHERE package_id = ? "+
		"ORDER BY order_key, version", target.ID)
	t.Logf("version names plan:\n%s", plan)
	if !strings.Contains(plan, "package_versions_order") ||
		strings.Contains(strings.ToLower(plan), "temp b-tree") ||
		strings.Contains(plan, "Sort") {
		t.Fatal("the version names were not read from package_versions_order in its order")
	}
	// The plan of one page row's record, read by its name, late in the
	// module.
	plan = explain(t, s, "SELECT id, package_id, version, order_key, created_at, "+
		"last_downloaded_at, metadata FROM package_versions WHERE package_id = ? AND version = ?",
		target.ID, "1.0.4990")
	t.Logf("page row plan:\n%s", plan)
	if !strings.Contains(plan, "package_versions_version") {
		t.Fatal("a page row's record was not sought on package_versions_version")
	}
}

// explain returns the engine's plan for q with args, which on PostgreSQL
// is run, so the plan shows the rows each step read.
func explain(t *testing.T, s *artsql.Store, q string, args ...any) string {
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
