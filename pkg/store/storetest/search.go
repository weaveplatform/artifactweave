package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/aql/aqltest"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// testItemSearch runs the shared AQL cases (pkg/aql/aqltest): every backend
// must return the reference evaluator's rows in the reference order, with the
// properties and statistics the query includes, which is what keeps the SQL
// dialects' globbing, collation and NULL handling honest. The build runs
// the relation-path cases join to are stored too, and the builds and
// build.promotions cases run through the build store.
func testItemSearch(t *testing.T, st store.Store) {
	ctx := context.Background()
	s := st.Items()
	props := map[string]types.Properties{}
	stats := map[string]*types.ItemStats{}
	for _, seed := range aqltest.Seeds() {
		put := s.Put
		if seed.Item.IsRoot() {
			// A root's record is made as setting a property on it makes it.
			put = s.EnsureRoot
		}
		it := get[types.Item](t)(put(ctx, seed.Item))
		key := aqltest.Paths([]aql.Hit{{Item: it}})[0]
		if seed.Properties != nil {
			must(t, s.SetProperties(ctx, it.ID, seed.Properties, base))
			props[key] = seed.Properties
		}
		if seed.Stats != nil {
			must(t, s.PutStats(ctx, it.ID, *seed.Stats))
			stats[key] = seed.Stats
		}
	}
	seedBuilds(t, st.Builds())
	for _, c := range aqltest.Cases() {
		t.Run(c.Name, func(t *testing.T) {
			q, err := c.Parse()
			must(t, err)
			hits, err := s.Search(ctx, q, c.Options)
			must(t, err)
			if got := aqltest.Paths(hits); !slices.Equal(got, c.Want) {
				t.Fatalf("got  %q\nwant %q", got, c.Want)
			}
			for _, h := range hits {
				key := aqltest.Paths([]aql.Hit{h})[0]
				var (
					wantProps types.Properties
					wantStats *types.ItemStats
				)
				if q.Properties() {
					wantProps = props[key]
				}
				if len(q.Stats()) > 0 {
					wantStats = stats[key]
				}
				same(t, "properties of "+h.Item.FullPath(), wantProps, h.Properties)
				same(t, "statistics of "+h.Item.FullPath(), wantStats, h.Stats)
			}
		})
	}
	testRelatedSearch(t, st)
	// A query that cannot run reports the store's error, not a panic.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	q, err := aql.Parse(`items.find()`)
	must(t, err)
	if _, err := s.Search(cancelled, q, aql.Options{}); err == nil {
		t.Fatal("Search with a cancelled context succeeded")
	}
	testBuildSearch(t, st.Builds())
}

// testRelatedSearch runs the shared includes of related domains: the
// store's rows, nested from the store (store.AQLSource), must render as
// the reference evaluator's nested from aqltest.Corpus.
func testRelatedSearch(t *testing.T, st store.Store) {
	ctx := context.Background()
	corpus := aqltest.Corpus()
	render := func(res aql.Response) string {
		b, err := json.Marshal(res)
		must(t, err)
		return string(b)
	}
	for _, c := range aqltest.RelatedCases() {
		t.Run("related/"+c.Name, func(t *testing.T) {
			q, err := c.Parse()
			must(t, err)
			var got, want string
			if q.Domain == aql.DomainItems {
				hits, err := st.Items().Search(ctx, q.ForNesting(), c.Options)
				must(t, err)
				rel, err := q.Nest(ctx, store.AQLSource(st), aql.Nesting{Now: aqltest.Now}, hits)
				must(t, err)
				got = render(q.Response(hits).WithRelated(rel))
				ref := aql.SelectItems(q.ForNesting(), c.Options, corpus)
				rel, err = q.Nest(ctx, corpus, aql.Nesting{Now: aqltest.Now}, ref)
				must(t, err)
				want = render(q.Response(ref).WithRelated(rel))
			} else {
				hits, err := st.Builds().Search(ctx, q, c.Options)
				must(t, err)
				rel, err := q.NestBuilds(ctx, store.AQLSource(st), aql.Nesting{}, hits)
				must(t, err)
				got = render(q.BuildResponse(hits).WithRelated(rel))
				ref := aql.SelectBuilds(q, c.Options, corpus)
				rel, err = q.NestBuilds(ctx, corpus, aql.Nesting{}, ref)
				must(t, err)
				want = render(q.BuildResponse(ref).WithRelated(rel))
			}
			if got != want {
				t.Fatalf("got  %s\nwant %s", got, want)
			}
		})
	}
	// A source that cannot read reports the store's error.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	src := store.AQLSource(st)
	for name, call := range map[string]func() error{
		"runs":       func() error { _, err := src.RunsWithFile(cancelled, false, "a"); return err },
		"items":      func() error { _, err := src.ItemsWithSHA1(cancelled, "a"); return err },
		"run":        func() error { _, err := src.Run(cancelled, 1); return err },
		"promotions": func() error { _, err := src.RunPromotions(cancelled, 1); return err },
	} {
		if call() == nil {
			t.Errorf("%s with a cancelled context succeeded", name)
		}
	}
}

// seedBuilds stores aqltest.BuildSeeds, which a fresh store numbers as
// aqltest.Corpus does.
func seedBuilds(t *testing.T, s store.BuildStore) {
	ctx := context.Background()
	for _, seed := range aqltest.BuildSeeds() {
		b := get[types.Build](t)(s.Put(ctx, seed.Build))
		for _, p := range seed.Promotions {
			p.BuildID = b.ID
			get[types.BuildPromotion](t)(s.AddPromotion(ctx, p))
		}
	}
}

// testBuildSearch runs the shared builds and build.promotions cases. Each
// hit carries the run's own columns, and a promotions hit its promotion,
// exactly as the reference evaluator returns them.
func testBuildSearch(t *testing.T, s store.BuildStore) {
	ctx := context.Background()
	corpus := aqltest.Corpus()
	for _, c := range aqltest.BuildCases() {
		t.Run(c.Name, func(t *testing.T) {
			q, err := c.Parse()
			must(t, err)
			hits, err := s.Search(ctx, q, c.Options)
			must(t, err)
			same(t, "hits", aql.SelectBuilds(q, c.Options, corpus), hits)
			if got := aqltest.BuildRows(hits); !slices.Equal(got, c.Want) {
				t.Fatalf("got  %q\nwant %q", got, c.Want)
			}
		})
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	q, err := aql.Parse(`builds.find()`)
	must(t, err)
	if _, err := s.Search(cancelled, q, aql.Options{}); err == nil {
		t.Fatal("Search with a cancelled context succeeded")
	}
	q, err = aql.Parse(`items.find()`)
	must(t, err)
	if _, err := s.Search(ctx, q, aql.Options{}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("an items query on the build store: %v", err)
	}
}
