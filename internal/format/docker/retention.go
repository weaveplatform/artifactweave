package docker

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Retained returns the previous manifests of the image name's tag that Tag
// Retention kept (decision 0031), oldest push first. A retained manifest is
// one that moved from the tag folder to its own digest folder and so still
// names the tag in docker.manifest, where a manifest pushed by digest names
// its digest folder.
func Retained(
	ctx context.Context,
	items store.ItemStore,
	repoKey, name, tag string,
) ([]types.Item, error) {
	q := manifestQuery(
		repoKey,
		name,
		&aql.FieldCriterion{
			Field: aql.FieldPath,
			Op:    aql.OpMatch,
			Text:  name + "/" + DigestFolder("") + "*",
		},
		&aql.PropertyCriterion{Part: aql.PartPair, Key: ManifestProperty, Op: aql.OpEq, Value: tag},
	)
	q.Sort = &aql.Sort{Fields: []aql.Field{aql.FieldModified}}
	return search(ctx, items, q, "retained versions of "+name+":"+tag)
}

// Manifests returns every manifest stored in the image name, in its tag
// and digest folders alike.
func Manifests(
	ctx context.Context,
	items store.ItemStore,
	repoKey, name string,
) ([]types.Item, error) {
	return search(ctx, items, manifestQuery(repoKey, name), "manifests of "+name)
}

func search(
	ctx context.Context,
	items store.ItemStore,
	q *aql.Query,
	what string,
) ([]types.Item, error) {
	hits, err := items.Search(ctx, q, aql.Options{})
	if err != nil {
		return nil, fmt.Errorf("docker: %s: %w", what, err)
	}
	out := make([]types.Item, len(hits))
	for i, h := range hits {
		out[i] = h.Item
	}
	return out, nil
}
