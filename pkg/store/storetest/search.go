package storetest

import (
	"context"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/aql/aqltest"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// testItemSearch runs the shared AQL cases (pkg/aql/aqltest): every backend
// must return the reference evaluator's rows in the reference order, which
// is what keeps the SQL dialects' globbing, collation and NULL handling
// honest.
func testItemSearch(t *testing.T, s store.ItemStore) {
	ctx := context.Background()
	props := map[string]types.Properties{}
	for _, seed := range aqltest.Seeds() {
		it := get[types.Item](t)(s.Put(ctx, seed.Item))
		if seed.Properties != nil {
			must(t, s.SetProperties(ctx, it.ID, seed.Properties))
			props[aqltest.Paths([]aql.Hit{{Item: it}})[0]] = seed.Properties
		}
	}
	for _, c := range aqltest.Cases() {
		t.Run(c.Name, func(t *testing.T) {
			q, err := aql.Parse(c.Query)
			must(t, err)
			hits, err := s.Search(ctx, q, c.Options)
			must(t, err)
			if got := aqltest.Paths(hits); !slices.Equal(got, c.Want) {
				t.Fatalf("got  %q\nwant %q", got, c.Want)
			}
			for _, h := range hits {
				var want types.Properties
				if q.Properties() {
					want = props[aqltest.Paths([]aql.Hit{h})[0]]
				}
				same(t, "properties of "+h.Item.FullPath(), want, h.Properties)
			}
		})
	}
	// A query that cannot run reports the store's error, not a panic.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	q, err := aql.Parse(`items.find()`)
	must(t, err)
	if _, err := s.Search(cancelled, q, aql.Options{}); err == nil {
		t.Fatal("Search with a cancelled context succeeded")
	}
}
