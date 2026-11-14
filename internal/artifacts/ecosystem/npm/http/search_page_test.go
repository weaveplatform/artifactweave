package npmhttp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	npmfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// searchReads counts what a search reads of the package index: searches
// and the candidates they return, and pages, the packages they return and
// reads of a page's versions.
type searchReads struct {
	searches, candidates, finds, versions, lists int
	pages, paged, withVersions                   int
}

type countingStore struct {
	pkgroute.Store
	reads *searchReads
}

func (s countingStore) Packages() port.PackageStore {
	return countingPackages{PackageStore: s.Store.Packages(), reads: s.reads}
}

type countingPackages struct {
	port.PackageStore
	reads *searchReads
}

func (p countingPackages) SearchNpm(
	ctx context.Context, q port.NpmSearch, pg kernel.Page,
) ([]port.SearchCandidate, string, error) {
	found, next, err := p.PackageStore.SearchNpm(ctx, q, pg)
	p.reads.searches++
	p.reads.candidates += len(found)
	return found, next, err //nolint:wrapcheck // a test's pass-through
}

func (p countingPackages) CountPackages(ctx context.Context, r port.PackageRange) (int, error) {
	panic("npm search counts no packages")
}

func (p countingPackages) PackagesAt(
	ctx context.Context, r port.PackageRange, offset, limit int,
) ([]model.Package, error) {
	found, err := p.PackageStore.PackagesAt(ctx, r, offset, limit)
	p.reads.pages++
	p.reads.paged += len(found)
	return found, err //nolint:wrapcheck // a test's pass-through
}

func (p countingPackages) Candidates(
	ctx context.Context, packages []model.Package,
) ([]port.SearchCandidate, error) {
	p.reads.withVersions++
	return p.PackageStore.Candidates(ctx, packages) //nolint:wrapcheck // a test's pass-through
}

func (p countingPackages) Find(ctx context.Context, key, name string) (model.Package, error) {
	p.reads.finds++
	return p.PackageStore.Find(ctx, key, name) //nolint:wrapcheck // a test's pass-through
}

func (p countingPackages) Versions(ctx context.Context, id int64) ([]model.PackageVersion, error) {
	p.reads.versions++
	return p.PackageStore.Versions(ctx, id) //nolint:wrapcheck // a test's pass-through
}

func (p countingPackages) List(
	ctx context.Context, key string, pg kernel.Page,
) ([]model.Package, string, error) {
	p.reads.lists++
	return p.PackageStore.List(ctx, key, pg) //nolint:wrapcheck // a test's pass-through
}

// bulkName is the name of the i'th of the late-page test's packages.
func bulkName(i int) string { return fmt.Sprintf("bulk-%05d", i) }

// A late page of 5000 packages reads the index a page of candidates at a
// time, each with its versions, never a package or its versions one by
// one; a term names the packages it reads; and a search without text by a
// caller who may read the whole repository reads its page alone, at most
// 500 packages to a read (#1425).
func TestAccSearchLatePage(t *testing.T) {
	reads := &searchReads{}
	f := newFixture(t, func(st pkgroute.Store) pkgroute.Store {
		return countingStore{Store: st, reads: reads}
	})
	ctx := context.Background()
	const count = 5000
	for i := range count {
		p, err := f.db.Packages().Ensure(ctx, model.Package{
			RepoKey: "npm-local", Name: bulkName(i), Type: "npm",
		})
		if err != nil {
			t.Fatal(err)
		}
		meta, err := json.Marshal(npmfmt.Metadata{
			Manifest:  fmt.Appendf(nil, `{"name":%q,"version":"1.0.0"}`, bulkName(i)),
			Published: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Packages().PutVersion(ctx, model.PackageVersion{
			PackageID: p.ID, Version: "1.0.0", Metadata: meta,
		}); err != nil {
			t.Fatal(err)
		}
	}
	names := func(from, to int) []string {
		out := []string{}
		for i := from; i < to; i++ {
			out = append(out, bulkName(i))
		}
		return out
	}
	paged := func(pages, n int) searchReads {
		return searchReads{pages: pages, paged: n, withVersions: pages}
	}
	for _, c := range []struct {
		query, user string
		want        []string
		reads       searchReads
	}{
		{"from=4980&size=20", "reader", names(count-20, count), paged(1, 20)},
		{"size=20", "reader", names(0, 20), paged(1, 20)},
		// A page of more than 500 is read 500 at a time, and a read
		// short of what it asked for ends it.
		{"from=10&size=1200", "reader", names(10, 1210), paged(3, 1200)},
		{"from=4900&size=600", "reader", names(4900, count), paged(1, 100)},
		// A page past the end reads no versions.
		{"from=6000", "reader", []string{}, searchReads{pages: 1}},
		{"text=bulk-04990", "reader", []string{"bulk-04990"}, searchReads{searches: 1, candidates: 1}},
		// from is counted over what a caller whose permission names paths
		// may read, so the scan passes over every package, 500 to a read
		// of the index; it reads them in growing batches, 20, 40, 80,
		// 160, 320, then 500 at a time, when it may read none.
		{"from=4980&size=20", "partial", []string{}, searchReads{searches: 10, candidates: count}},
		{"size=20", "partial", []string{}, searchReads{searches: 14, candidates: count}},
	} {
		*reads = searchReads{}
		got := f.search(t, c.query, c.user)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s as %s: %v, want %v", c.query, c.user, got, c.want)
		}
		if *reads != c.reads {
			t.Errorf("%s as %s read %+v, want %+v", c.query, c.user, *reads, c.reads)
		}
	}
}
