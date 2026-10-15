package inmem

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// Search implements store.BuildStore with the reference evaluator.
func (b buildStore) Search(
	ctx context.Context,
	q *aql.Query,
	o aql.Options,
) ([]aql.BuildHit, error) {
	if q.Domain == aql.DomainItems {
		return nil, fmt.Errorf("search builds: an %s query: %w", q.Domain, store.ErrInvalid)
	}
	var out []aql.BuildHit
	err := b.s.do(ctx, func() error {
		c := &aql.Corpus{}
		if q.Joins() {
			c.Items = items{b.s}.hits(false)
		}
		b.addBuilds(c)
		// SelectBuilds builds each hit afresh from the rows it reads in
		// place, so nothing returned aliases the stored runs.
		out = aql.SelectBuilds(q, o, c)
		return nil
	})
	return out, err
}

// addBuilds adds the stored runs and promotions to c, read in place.
func (b buildStore) addBuilds(c *aql.Corpus) {
	c.Builds = slices.Collect(maps.Values(b.runs().rows))
	c.Promotions = slices.Collect(maps.Values(b.promotions().rows))
}

// hits returns every item with its properties and, with stats, its
// statistics, read in place.
func (i items) hits(stats bool) []aql.Hit {
	props := i.props().rows
	st := i.stats().rows
	out := make([]aql.Hit, 0, len(i.t().rows))
	for id, it := range i.t().rows {
		h := aql.Hit{Item: it, Properties: props[id]}
		if s, ok := st[id]; ok && stats {
			h.Stats = &s
		}
		out = append(out, h)
	}
	return out
}
