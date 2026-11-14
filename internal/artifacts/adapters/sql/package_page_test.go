package sql_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	artsql "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

// TestAccPackagePagesAtScale pages late into 5,000 packages, 5,000
// releases across 1,000 modules and a module of 5,000 versions on each
// engine, as a whole-repository reader's npm and NuGet search, Puppet and
// Chef lists and Ansible and Puppet version lists do (#1425, #1426), and
// keeps the plans: each page walks packages_name or package_versions_order
// in its own order to the offset, sorting at most one module's versions or
// a short prefix's names, and each count reads those indexes, a NuGet one
// each version's metadata too.
func TestAccPackagePagesAtScale(t *testing.T) {
	sqltest.RunModule(t, mods, artsql.New,
		func(t *testing.T, newStore func(*testing.T) *artsql.Store) {
			packagePagesAtScale(t, newStore(t))
		}, sqltest.WithPostgres())
}

// seedVersions writes versions of pkg directly, version(i) with order key
// i+1, as PutVersion would key them in order, without its reorder of the
// package on each write.
func seedVersions(
	t *testing.T,
	s *artsql.Store,
	pkg []model.Package,
	n int,
	version func(int) string,
	metadata func(model.Package, int) string,
) {
	t.Helper()
	ctx := t.Context()
	tx, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	insert, err := tx.PrepareContext(ctx, s.Dialect().Rebind("INSERT INTO package_versions "+
		"(package_id, version, order_key, metadata) VALUES (?, ?, ?, ?)"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pkg {
		// Newest first, so the rows' own order is not the index's.
		for i := n - 1; i >= 0; i-- {
			if _, err := insert.ExecContext(ctx, p.ID, version(i), i+1, metadata(p, i)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := insert.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func ensureAll(t *testing.T, s *artsql.Store, key string, pt repo.PackageType, names []string) []model.Package {
	t.Helper()
	out := make([]model.Package, 0, len(names))
	err := s.Tx(t.Context(), func(ctx context.Context) error {
		for _, name := range names {
			p, err := s.Packages().Ensure(ctx, model.Package{RepoKey: key, Type: pt, Name: name})
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func packagePagesAtScale(t *testing.T, s *artsql.Store) {
	t.Helper()
	ctx := t.Context()
	const count = 5000
	name := func(i int) string { return fmt.Sprintf("pkg.%05d", i) }
	names := make([]string, count)
	for i := range names {
		names[i] = name(i)
	}
	// NuGet packages: every tenth version a pre-release, so the shown
	// range is not the whole one.
	pkgs := ensureAll(t, s, "pages", repo.NuGet, names)
	seedVersions(t, s, pkgs, 1, func(int) string { return "1.0.0" },
		func(p model.Package, _ int) string {
			version := "1.0.0"
			if p.ID%10 == 0 {
				version += "-beta"
			}
			return `{"id":"` + p.Name + `","version":"` + version + `","path":"` + p.Name +
				`/` + p.Name + `.1.0.0.nupkg","description":"` + strings.Repeat("x", 512) + `"}`
		})
	modules := make([]string, 1000)
	for i := range modules {
		modules[i] = fmt.Sprintf("acme-m%04d", i)
	}
	forge := ensureAll(t, s, "forge", repo.Puppet, modules)
	seedVersions(t, s, forge, 5, func(i int) string { return fmt.Sprintf("1.0.%d", i) },
		func(model.Package, int) string { return "{}" })
	widget := ensureAll(t, s, "widgets", repo.Puppet, []string{"acme-widget"})[0]
	seedVersions(t, s, []model.Package{widget}, count,
		func(i int) string { return fmt.Sprintf("1.0.%d", i) },
		func(model.Package, int) string { return "{}" })
	engine := s.Dialect().Name
	if s.Dialect().Dollar {
		// As autovacuum would after a load this size: statistics, and the
		// visibility map an index-only scan relies on.
		if _, err := s.DB().ExecContext(ctx, "VACUUM ANALYZE packages, package_versions"); err != nil {
			t.Fatal(err)
		}
	}
	timed := func(what string, f func() error) {
		t.Helper()
		start := time.Now()
		if err := f(); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		t.Logf("%s: %s: %s", engine, what, time.Since(start))
	}
	st := s.Packages()
	for _, c := range []struct {
		what  string
		r     port.PackageRange
		total int
		// few is a range PostgreSQL may read through a bitmap of
		// packages_name and sort, as cheaper than its walk: no more than
		// the range's own names.
		few bool
	}{
		{"npm or Chef range", port.PackageRange{RepoKey: "pages"}, count, false},
		{"NuGet range, no pre-releases", port.PackageRange{RepoKey: "pages", Shown: &port.NuGetShown{}}, count - count/10, false},
		{"NuGet prefix", port.PackageRange{RepoKey: "pages", Prefix: "pkg.049", Shown: &port.NuGetShown{Prerelease: true}}, 100, true},
	} {
		var total int
		var page []model.Package
		timed(c.what+" count", func() (err error) {
			total, err = st.CountPackages(ctx, c.r)
			return err
		})
		timed(c.what+" late page", func() (err error) {
			page, err = st.PackagesAt(ctx, c.r, c.total-20, 20)
			return err
		})
		if total != c.total || len(page) != 20 {
			t.Fatalf("%s: total %d, page of %d", c.what, total, len(page))
		}
		countQ, pageQ, countArgs, pageArgs := artsql.PackageRangeQueries(s, c.r, c.total-20, 20)
		plan := explain(t, s, countQ, countArgs...)
		t.Logf("%s: %s count:\n%s", engine, c.what, plan)
		noSort(t, plan)
		plan = explain(t, s, pageQ, pageArgs...)
		t.Logf("%s: %s late page:\n%s", engine, c.what, plan)
		if c.few && s.Dialect().Dollar {
			if !strings.Contains(plan, "packages_name") {
				t.Error("plan does not use packages_name")
			}
			continue
		}
		wantSeek(t, plan, "packages_name")
	}
	var tally port.VersionTally
	timed("tally of 1,000 modules", func() (err error) {
		tally, err = st.TallyVersions(ctx, "forge", 4097)
		return err
	})
	if tally != (port.VersionTally{Packages: 1000, Versions: 5000, MostVersions: 5}) {
		t.Fatalf("tally %+v", tally)
	}
	// The order picks which packages a tally counts only when there are
	// more than its limit, as a Puppet list's caps ask of 4,097 and then of
	// the first 4,000; the plan of those walks packages_name.
	for _, limit := range []int{4097, 4000} {
		timed(fmt.Sprintf("tally of the first %d of 5,000 packages", limit), func() (err error) {
			tally, err = st.TallyVersions(ctx, "pages", limit)
			return err
		})
		if tally != (port.VersionTally{Packages: limit, Versions: limit, MostVersions: 1}) {
			t.Fatalf("tally of %d: %+v", limit, tally)
		}
		plan := explain(t, s, artsql.TallyQuery, "pages", limit)
		t.Logf("%s: tally of the first %d:\n%s", engine, limit, plan)
		wantSeek(t, plan, "packages_name")
		if !strings.Contains(plan, "package_versions_order") {
			t.Error("the tally does not count through package_versions_order")
		}
	}
	var releases []port.ReleaseName
	timed("late page of 5,000 releases", func() (err error) {
		releases, err = st.ReleasesAt(ctx, "forge", 4980, 20)
		return err
	})
	if len(releases) != 20 || releases[0].Package != "acme-m0996" || releases[0].Version != "1.0.4" ||
		releases[19].Version != "1.0.0" {
		t.Fatalf("late releases %+v", releases)
	}
	plan := explain(t, s, artsql.ReleasesAtQuery(s), "forge", 20, 4980)
	t.Logf("%s: late page of releases:\n%s", engine, plan)
	if !strings.Contains(plan, "packages_name") || !strings.Contains(plan, "package_versions_order") {
		t.Error("the releases are not read through packages_name and package_versions_order")
	}
	if s.Dialect().Dollar {
		// PostgreSQL sorts each module's versions as they come, in name
		// order, rather than every release.
		if !strings.Contains(plan, "Presorted Key: p.name") {
			t.Error("the releases are not sorted within each module alone")
		}
	} else {
		noSort(t, plan)
	}
	var versions []string
	var n int
	timed("count of 5,000 versions", func() (err error) {
		n, err = st.CountVersions(ctx, widget.ID)
		return err
	})
	timed("late page of 5,000 versions", func() (err error) {
		versions, err = st.VersionNamesAt(ctx, widget.ID, true, 4980, 20)
		return err
	})
	want := make([]string, 0, 20)
	for i := 19; i >= 0; i-- {
		want = append(want, fmt.Sprintf("1.0.%d", i))
	}
	if n != count || !slices.Equal(versions, want) {
		t.Fatalf("count %d, late versions %q", n, versions)
	}
	for _, newest := range []bool{true, false} {
		plan = explain(t, s, artsql.VersionNamesAtQuery(newest), widget.ID, 20, 4980)
		t.Logf("%s: late page of versions, newest first %v:\n%s", engine, newest, plan)
		wantSeek(t, plan, "package_versions_order")
	}
	plan = explain(t, s, "SELECT COUNT(*) FROM package_versions WHERE package_id = ?", widget.ID)
	t.Logf("%s: count of versions:\n%s", engine, plan)
	wantSeek(t, plan, "package_versions_")
}

// noSort fails if plan sorts.
func noSort(t *testing.T, plan string) {
	t.Helper()
	for _, sort := range []string{"TEMP B-TREE", "Sort"} {
		if strings.Contains(plan, sort) {
			t.Errorf("plan sorts (%s)", sort)
		}
	}
}
