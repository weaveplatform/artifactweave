package debianhttp

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/debian"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// CalculateDebianRepositoryMetadata rebuilds a local apt view. The REST
// contract defaults to a synchronous calculation and writes item properties
// unless writeProps=0; both paths retain Manage permission checks.
func (s DebianAPI) CalculateDebianRepositoryMetadata(
	ctx context.Context,
	req artgen.CalculateDebianRepositoryMetadataRequestObject,
) (artgen.CalculateDebianRepositoryMetadataResponseObject, error) {
	if req.Params.WriteProps != nil && !req.Params.WriteProps.Valid() {
		return nil, httpkit.BadRequestf("Invalid writeProps value")
	}
	async := req.Params.Async != nil && *req.Params.Async
	writeProps := req.Params.WriteProps == nil ||
		*req.Params.WriteProps == artgen.CalculateDebianRepositoryMetadataParamsWritePropsN1
	if writeProps {
		msg, err := s.Reindex(ctx, repo.Debian, req.RepoKey, "", !async)
		if err != nil {
			return nil, err
		}
		return artgen.CalculateDebianRepositoryMetadata200TextResponse(msg), nil
	}
	st, c, by, err := s.OperationRepo(
		ctx,
		repo.Debian,
		itemkit.ReindexOps[repo.Debian],
		req.RepoKey,
	)
	if err != nil {
		return nil, err
	}
	if err := s.Enqueue(ctx, st, debian.KindReindexNoProps, c.Key, by); err != nil {
		return nil, err
	}
	if !async {
		if err := s.Await(ctx, st, debian.KindReindexNoProps, c.Key); err != nil {
			return nil, s.ItemError(ctx, "Debian reindex", err)
		}
	}
	return artgen.CalculateDebianRepositoryMetadata200TextResponse(
		fmt.Sprintf(itemkit.ReindexOps[repo.Debian].Answer, c.Key)), nil
}
