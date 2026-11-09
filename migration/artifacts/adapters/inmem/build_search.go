package inmem

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/weaveplatform/artifactweave/migration/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/migration/kernel"
)

// Search implements port.BuildStore with the reference evaluator.
func (b buildStore) Search(
	ctx context.Context,
	q *aql.Query,
	o aql.Options,
) ([]aql.BuildHit, error) {
	if q.Domain == aql.DomainItems {
		return nil, fmt.Errorf("search builds: an %s query: %w", q.Domain, kernel.ErrInvalid)
	}
	var out []aql.BuildHit
	err := b.s.Do(ctx, func() error {
		c := &aql.Corpus{}
		if q.Joins() {
			c.Items = items(b).hits(false)
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
	c.Builds = slices.Collect(maps.Values(b.runs().Rows))
	c.Promotions = slices.Collect(maps.Values(b.promotions().Rows))
}

// hits returns every item with its properties and, with stats, its
// statistics, read in place.
func (i items) hits(stats bool) []aql.Hit {
	props := i.props().Rows
	st := i.stats().Rows
	out := make([]aql.Hit, 0, len(i.t().Rows))
	for id, it := range i.t().Rows {
		h := aql.Hit{Item: it, Properties: props[id]}
		if s, ok := st[id]; ok && stats {
			h.Stats = &s
		}
		out = append(out, h)
	}
	return out
}
