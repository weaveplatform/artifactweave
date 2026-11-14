package nugethttp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	nugetfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/nuget"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// searchReads counts what a search reads of the package index: searches
// and the candidates they return, and counts, pages and the packages they
// return, and reads of a page's versions.
type searchReads struct {
	searches, candidates, finds, versions, lists int
	counts, pages, paged, withVersions           int
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

func (p countingPackages) SearchNuGet(
	ctx context.Context, q port.NuGetSearch, pg kernel.Page,
) ([]port.SearchCandidate, string, error) {
	found, next, err := p.PackageStore.SearchNuGet(ctx, q, pg)
	p.reads.searches++
	p.reads.candidates += len(found)
	return found, next, err //nolint:wrapcheck // a test's pass-through
}

func (p countingPackages) CountPackages(ctx context.Context, r port.PackageRange) (int, error) {
	p.reads.counts++
	return p.PackageStore.CountPackages(ctx, r) //nolint:wrapcheck // a test's pass-through
}

func (p countingPackages) PackagesAt(
	ctx context.Context, r port.PackageRange, offset, limit int,
) ([]artmodel.Package, error) {
	found, err := p.PackageStore.PackagesAt(ctx, r, offset, limit)
	p.reads.pages++
	p.reads.paged += len(found)
	return found, err //nolint:wrapcheck // a test's pass-through
}

func (p countingPackages) Candidates(
	ctx context.Context, packages []artmodel.Package,
) ([]port.SearchCandidate, error) {
	p.reads.withVersions++
	return p.PackageStore.Candidates(ctx, packages) //nolint:wrapcheck // a test's pass-through
}

func (p countingPackages) Find(ctx context.Context, key, name string) (artmodel.Package, error) {
	p.reads.finds++
	return p.PackageStore.Find(ctx, key, name) //nolint:wrapcheck // a test's pass-through
}

func (p countingPackages) Versions(
	ctx context.Context, id int64,
) ([]artmodel.PackageVersion, error) {
	p.reads.versions++
	return p.PackageStore.Versions(ctx, id) //nolint:wrapcheck // a test's pass-through
}

func (p countingPackages) List(
	ctx context.Context, key string, pg kernel.Page,
) ([]artmodel.Package, string, error) {
	p.reads.lists++
	return p.PackageStore.List(ctx, key, pg) //nolint:wrapcheck // a test's pass-through
}

// bulkID is the id of the i'th of the late-page test's packages.
func bulkID(i int) string { return fmt.Sprintf("Bulk.%05d", i) }

// Over 5000 packages, a search reads the index a page of candidates at a
// time, each with its versions, never a package or its versions one by
// one. For a caller who may read the whole repository a search without
// terms, and an autocomplete, count in the store and read their page alone
// (#1425). For anyone else totalHits counts every package the caller may
// read, so a late page reads them all; a term, and a feed of one id's
// versions, read only the packages they name.
func TestAccSearchLatePage(t *testing.T) {
	reads := &searchReads{}
	f := newFixture(t, func(st pkgroute.Store) pkgroute.Store {
		return countingStore{Store: st, reads: reads}
	})
	ctx := context.Background()
	const count, late = 5000, 4980
	// The early packages go straight into the index, as a push would put
	// them; the late ones, which the pages answer with their download
	// counts, are pushed.
	for i := range late {
		id := bulkID(i)
		p, err := f.db.Packages().Ensure(ctx, artmodel.Package{
			RepoKey: "nuget-local", Name: nugetfmt.PackageName(id), Type: "nuget",
		})
		if err != nil {
			t.Fatal(err)
		}
		meta, err := json.Marshal(nugetfmt.Metadata{
			ID: id, Version: "1.0.0", Description: "The " + id + " package",
			Path: id + ".1.0.0.nupkg",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Packages().PutVersion(ctx, artmodel.PackageVersion{
			PackageID: p.ID, Version: "1.0.0", Metadata: meta,
		}); err != nil {
			t.Fatal(err)
		}
	}
	lateIDs := []string{}
	for i := late; i < count; i++ {
		f.push(bulkID(i), "1.0.0", "")
		lateIDs = append(lateIDs, bulkID(i))
	}
	paged := func(n int) searchReads {
		return searchReads{counts: 1, pages: 1, paged: n, withVersions: 1}
	}
	for _, c := range []struct {
		path, user string
		total      int
		want       []string
		reads      searchReads
	}{
		{v3 + "/query?skip=4980&take=20", "reader", count, lateIDs, paged(20)},
		{v3 + "/query?q=bulk.04990", "reader", 1, []string{"Bulk.04990"}, searchReads{searches: 1, candidates: 1}},
		{v3 + "/autocomplete?q=bulk.0499", "reader", 10, lateIDs[10:], paged(10)},
		{v3 + "/autocomplete?q=bulk.0499&skip=8", "reader", 10, lateIDs[18:], paged(2)},
		// A caller whose permission names paths has every package
		// checked, 500 to a read.
		{v3 + "/query?skip=4980&take=20", "partial", 0, []string{}, searchReads{searches: 10, candidates: count}},
	} {
		*reads = searchReads{}
		rec := f.do(http.MethodGet, c.path, c.user)
		wantStatus(t, rec, http.StatusOK)
		var doc struct {
			TotalHits int               `json:"totalHits"`
			Data      []json.RawMessage `json:"data"`
		}
		decodeJSON(t, rec, &doc)
		got := []string{}
		for _, d := range doc.Data {
			var hit struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(d, &hit) != nil {
				var id string
				if err := json.Unmarshal(d, &id); err != nil {
					t.Fatal(err)
				}
				hit.ID = id
			}
			got = append(got, hit.ID)
		}
		if doc.TotalHits != c.total || !slices.Equal(got, c.want) {
			t.Errorf("%s as %s: totalHits %d, %v; want %d, %v",
				c.path, c.user, doc.TotalHits, got, c.total, c.want)
		}
		if *reads != c.reads {
			t.Errorf("%s as %s read %+v, want %+v", c.path, c.user, *reads, c.reads)
		}
	}
	// A feed of one id's versions reads that package alone.
	*reads = searchReads{}
	feed := f.feed(legacyV2URL("FindPackagesById()", url.Values{"id": {"'Bulk.04990'"}}), "reader")
	if got := feedVersions(t, feed); !slices.Equal(got, []string{"1.0.0"}) {
		t.Errorf("FindPackagesById(): %v", got)
	}
	if want := (searchReads{searches: 1, candidates: 1}); *reads != want {
		t.Errorf("FindPackagesById() read %+v, want %+v", *reads, want)
	}
}
