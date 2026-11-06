package aql

import (
	"context"
	"fmt"
	"slices"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// BuildRuns reads the build runs an AQL include of builds reaches: the runs
// that hold a file as an artifact or as a dependency, a run by ID and a
// run's promotions. Each method keeps the build store's contract.
type BuildRuns interface {
	FindArtifacts(ctx context.Context, c types.Checksums) ([]types.BuildFileRef, error)
	FindDependencies(ctx context.Context, c types.Checksums) ([]types.BuildFileRef, error)
	ByID(ctx context.Context, id int64) (types.Build, error)
	Promotions(ctx context.Context, buildID int64) ([]types.BuildPromotion, error)
}

// ItemSearch runs the item search that nested files are read through: the
// item store's AQL search, which loads a hit's properties and statistics as
// it does for a query's own rows.
type ItemSearch interface {
	Search(ctx context.Context, q *Query, o Options) ([]Hit, error)
}

// AQLSource is the source of the rows an AQL include of related domains
// reaches (Query.Nest): runs by the checksum of their files, files by
// checksum, runs by ID and their promotions. It reads through the store's
// own lookups, builds and items, so every backend nests the same rows. It
// takes those two rather than the store aggregate because the store imports
// this package: a store parameter would be an import cycle.
func AQLSource(builds BuildRuns, items ItemSearch) Source {
	return aqlSource{builds: builds, items: items}
}

type aqlSource struct {
	builds BuildRuns
	items  ItemSearch
}

func (a aqlSource) RunsWithFile(
	ctx context.Context,
	dependency bool,
	sha1 string,
) ([]types.Build, error) {
	find := a.builds.FindArtifacts
	if dependency {
		find = a.builds.FindDependencies
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

// ItemsWithSHA1 reads the files through the store's AQL search, which
// loads their properties and statistics as it does for a query's own rows:
// the item store's Stats answers zero for an item without a record, which a
// nested item would then render as a record of zeros where its own row
// renders none.
func (a aqlSource) ItemsWithSHA1(ctx context.Context, sha1 string) ([]Hit, error) {
	hits, err := a.items.Search(ctx, &Query{
		Criteria: And{
			&FieldCriterion{Field: FieldType, Op: OpEq, Text: "file"},
			&FieldCriterion{Field: FieldActualSHA1, Op: OpEq, Text: sha1},
		},
		Include: []string{"property", "stat"},
	}, Options{})
	if err != nil {
		return nil, fmt.Errorf("find files by checksum: %w", err)
	}
	return hits, nil
}

func (a aqlSource) Run(ctx context.Context, id int64) (types.Build, error) {
	b, err := a.builds.ByID(ctx, id)
	if err != nil {
		return b, fmt.Errorf("build run %d: %w", id, err)
	}
	return b, nil
}

func (a aqlSource) RunPromotions(ctx context.Context, id int64) ([]types.BuildPromotion, error) {
	ps, err := a.builds.Promotions(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("promotions of build run %d: %w", id, err)
	}
	return ps, nil
}
