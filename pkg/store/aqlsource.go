package store

import (
	"context"
	"fmt"
	"slices"

	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// AQLSource is s as the source of the rows an AQL include of related
// domains reaches (aql.Query.Nest): runs by the checksum of their files,
// files by checksum, runs by ID and their promotions. It reads through the
// store's own lookups, so every backend nests the same rows.
func AQLSource(s Store) aql.Source { return aqlSource{s} }

type aqlSource struct{ s Store }

func (a aqlSource) RunsWithFile(
	ctx context.Context,
	dependency bool,
	sha1 string,
) ([]types.Build, error) {
	find := a.s.Builds().FindArtifacts
	if dependency {
		find = a.s.Builds().FindDependencies
	}
	refs, err := find(ctx, types.Checksums{SHA1: sha1})
	if err != nil {
		return nil, fmt.Errorf("find build files: %w", err)
	}
	var ids []int64
	for _, r := range refs {
		if !slices.Contains(ids, r.Run.ID) {
			ids = append(ids, r.Run.ID)
		}
	}
	slices.Sort(ids)
	out := make([]types.Build, 0, len(ids))
	for _, id := range ids {
		b, err := a.Run(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func (a aqlSource) ItemsWithSHA1(ctx context.Context, sha1 string) ([]aql.Hit, error) {
	var out []aql.Hit
	page := Page{Limit: MaxLimit}
	for {
		items, next, err := a.s.Items().FindByChecksum(ctx, types.Checksums{SHA1: sha1}, page)
		if err != nil {
			return nil, fmt.Errorf("find files by checksum: %w", err)
		}
		for _, it := range items {
			props, err := a.s.Items().Properties(ctx, it.ID)
			if err != nil {
				return nil, fmt.Errorf("properties of %s/%s: %w", it.RepoKey, it.FullPath(), err)
			}
			if len(props) == 0 {
				props = nil
			}
			out = append(out, aql.Hit{Item: it, Properties: props})
		}
		if next == "" {
			return out, nil
		}
		page.After = next
	}
}

func (a aqlSource) Run(ctx context.Context, id int64) (types.Build, error) {
	b, err := a.s.Builds().ByID(ctx, id)
	if err != nil {
		return b, fmt.Errorf("build run %d: %w", id, err)
	}
	return b, nil
}

func (a aqlSource) RunPromotions(ctx context.Context, id int64) ([]types.BuildPromotion, error) {
	ps, err := a.s.Builds().Promotions(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("promotions of build run %d: %w", id, err)
	}
	return ps, nil
}
