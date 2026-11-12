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

// searchReads counts what a search reads of the package index.
type searchReads struct {
	searches, candidates, finds, versions, lists int
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
// one; a term names the packages it reads; and a page a caller may read
// all of reads only what the page needs.
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
	late := []string{}
	for i := count - 20; i < count; i++ {
		late = append(late, bulkName(i))
	}
	for _, c := range []struct {
		query, user        string
		want               []string
		searches, inspects int
	}{
		// from is counted over what the caller may read, so the scan
		// passes over 4980 packages, 500 to a read of the index.
		{"from=4980&size=20", "reader", late, 10, count},
		{"text=bulk-04990", "reader", []string{"bulk-04990"}, 1, 1},
		{"size=20", "reader", late[:0:0], 1, 20},
		// A caller who may read none of them reads them in growing
		// batches: 20, 40, 80, 160, 320, then 500 at a time.
		{"size=20", "partial", []string{}, 14, count},
	} {
		*reads = searchReads{}
		got := f.search(t, c.query, c.user)
		if c.query == "size=20" && c.user == "reader" {
			c.want = []string{}
			for i := range 20 {
				c.want = append(c.want, bulkName(i))
			}
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s as %s: %v, want %v", c.query, c.user, got, c.want)
		}
		want := searchReads{searches: c.searches, candidates: c.inspects}
		if *reads != want {
			t.Errorf("%s as %s read %+v, want %+v", c.query, c.user, *reads, want)
		}
	}
}
