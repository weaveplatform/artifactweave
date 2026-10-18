package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// aqlAction carries out items.delete or properties.update on the rows the
// search found, in their order, and returns the rows it changed, or would
// change on a dry run (decision 0012). Rows are skipped, and left out of the
// response, when the action cannot apply to them (actsOn) or, for a
// non-admin, when the caller may not both read them and delete or annotate
// them, or their repository's patterns reject them as they would for find.
// The checks are the ones DELETE and set-properties make, so an action can
// do nothing those would refuse.
func (s artifactoryService) aqlAction(
	ctx context.Context,
	st store.Store,
	p auth.Principal,
	q *aql.Query,
	configs map[string]repo.Config,
	hits []aql.Hit,
) ([]aql.Hit, error) {
	want := types.ActionDelete
	if q.Action == aql.UpdateProperties {
		want = types.ActionAnnotate
	}
	if !p.Admin {
		acc, err := accessFor(ctx, st, p)
		if err != nil {
			return nil, err
		}
		hits = acc.rows(configs, hits, want)
	}
	kept := hits[:0]
	for _, h := range hits {
		if c, ok := configs[h.Item.RepoKey]; ok && actsOn(c, h.Item, q.Action) {
			kept = append(kept, h)
		}
	}
	items := s.items(st)
	switch {
	case q.Action == aql.UpdateProperties:
		return kept, updateProperties(ctx, st, items, configs, q, kept)
	case q.DryRun:
		return kept, nil
	}
	for _, h := range kept {
		t := item.Target{
			Repo: configs[h.Item.RepoKey], Cached: h.Item.Cached, Path: h.Item.FullPath(),
		}
		// A folder earlier in the rows takes the items below it with it, so
		// an item already gone when its turn comes was deleted by this run.
		if _, err := items.Delete(
			ctx,
			t,
			p.Name,
		); err != nil &&
			!errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("delete %s/%s: %w", t.Repo.Key, t.Path, err)
		}
	}
	return kept, nil
}

// actsOn reports whether action a can change it in c. A virtual repository
// holds nothing of its own, and a remote repository's items are changed in
// its cache, which is where DELETE and set-properties address them. A
// repository root's record holds the repository's own properties, which
// properties.update may change; items.delete passes it over, because
// deleting the root would empty the whole repository, not remove one row.
func actsOn(c repo.Config, it types.Item, a aql.Action) bool {
	return c.Class != repo.Virtual && (c.Class != repo.Remote || it.Cached) &&
		(a == aql.UpdateProperties || !it.IsRoot())
}

// updateProperties replaces the values of each of q's keys that a hit has
// with q's new value, in one transaction with the formats' Annotated hooks
// unless q is a dry run. Keys a hit lacks are not added. Each hit's
// properties become the keys it changes, with their new values, which is
// what its row shows either way.
func updateProperties(
	ctx context.Context,
	st store.Store,
	items *item.Service,
	configs map[string]repo.Config,
	q *aql.Query,
	hits []aql.Hit,
) error {
	for n, h := range hits {
		set := types.Properties{}
		for _, k := range q.Keys {
			if _, ok := h.Properties[k]; ok {
				set[k] = []string{q.NewValue}
			}
		}
		hits[n].Properties = set
	}
	if q.DryRun {
		return nil
	}
	err := st.Tx(ctx, func(ctx context.Context) error {
		for _, h := range hits {
			if err := items.Annotate(
				ctx,
				configs[h.Item.RepoKey],
				h.Item,
				h.Properties,
				nil,
			); err != nil {
				return fmt.Errorf("properties of %s/%s: %w", h.Item.RepoKey, h.Item.FullPath(), err)
			}
		}
		return nil
	})
	return err //nolint:wrapcheck // the transaction returns ours
}
