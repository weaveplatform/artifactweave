package federationhttp

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// FailedBinaries is the failed content-fetch control service (FED-06).
type FailedBinaries interface {
	List(context.Context, string, int, int) ([]federated.FailedBinary, bool, error)
	Delete(context.Context, string, string) (int, error)
	Replay(context.Context, string, string) (int, error)
	Counts(context.Context, string, bool) (federated.BinaryCounts, error)
}

// GetListOfFailedBinaryTasks returns one stable page of failed fetches.
func (a FederatedAPI) GetListOfFailedBinaryTasks(
	ctx context.Context, req artgen.GetListOfFailedBinaryTasksRequestObject,
) (artgen.GetListOfFailedBinaryTasksResponseObject, error) {
	if a.Binaries == nil {
		return nil, apierror.ErrNotImplemented
	}
	limit, page := 1000, 1
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	if req.Params.Page != nil {
		page = *req.Params.Page
	}
	tasks, more, err := a.Binaries.List(ctx, req.Params.RepoKey, limit, page)
	if err != nil {
		return nil, federatedError(ctx, req.Params.RepoKey, err)
	}
	body := make([]artgen.FederatedFailedBinary, 0, len(tasks))
	for _, task := range tasks {
		created := task.Created.UnixMilli()
		item := artgen.FederatedFailedBinary{CreationTime: &created, FailuresCount: &task.Failures, RepoKey: &task.RepoKey, Sha1: &task.SHA1}
		if task.Path != "" {
			item.Path = &task.Path
		}
		body = append(body, item)
	}
	out := artgen.GetListOfFailedBinaryTasks200JSONResponse{Body: body}
	if more {
		query := url.Values{"repoKey": {req.Params.RepoKey}, "limit": {strconv.Itoa(limit)}, "page": {strconv.Itoa(page + 1)}}
		link := fmt.Sprintf("</artifactory/api/federation/binaries/failures/list?%s>; rel=\"next\"", query.Encode())
		out.Headers.Link = &link
	}
	return out, nil
}

// DeleteFailedBinaryTasks cancels failed fetches without changing the applied
// stream position or publishing the content they had waited for.
func (a FederatedAPI) DeleteFailedBinaryTasks(
	ctx context.Context, req artgen.DeleteFailedBinaryTasksRequestObject,
) (artgen.DeleteFailedBinaryTasksResponseObject, error) {
	if a.Binaries == nil {
		return nil, apierror.ErrNotImplemented
	}
	n, err := a.Binaries.Delete(ctx, req.Params.RepoKey, deref(req.Params.Sha1))
	if err != nil {
		return nil, federatedError(ctx, req.Params.RepoKey, err)
	}
	return artgen.DeleteFailedBinaryTasks200JSONResponse{DeletedTasks: &n}, nil
}

// ReplayFailedBinaryTasks immediately fetches matching content again. The
// engine publishes only the pending changes still current after the fetch.
func (a FederatedAPI) ReplayFailedBinaryTasks(
	ctx context.Context, req artgen.ReplayFailedBinaryTasksRequestObject,
) (artgen.ReplayFailedBinaryTasksResponseObject, error) {
	if a.Binaries == nil {
		return nil, apierror.ErrNotImplemented
	}
	n, err := a.Binaries.Replay(ctx, req.Params.RepoKey, deref(req.Params.Sha1))
	if err != nil {
		return nil, federatedError(ctx, req.Params.RepoKey, err)
	}
	return artgen.ReplayFailedBinaryTasks200JSONResponse{ReplayTasks: &n}, nil
}

// GetFederatedBinaryTasks returns administrator-only failed task counts.
func (a FederatedAPI) GetFederatedBinaryTasks(
	ctx context.Context, req artgen.GetFederatedBinaryTasksRequestObject,
) (artgen.GetFederatedBinaryTasksResponseObject, error) {
	if a.Binaries == nil {
		return nil, apierror.ErrNotImplemented
	}
	counts, err := a.Binaries.Counts(ctx, req.RepoKey, false)
	if err != nil {
		return nil, federatedError(ctx, req.RepoKey, err)
	}
	return artgen.GetFederatedBinaryTasks200JSONResponse{FailedTasks: counts.FailedTasks}, nil
}

// GetFederatedRepositoryBinaryState returns counts to a caller with Read on
// the repository, without disclosing another repository's failed paths.
func (a FederatedAPI) GetFederatedRepositoryBinaryState(
	ctx context.Context, req artgen.GetFederatedRepositoryBinaryStateRequestObject,
) (artgen.GetFederatedRepositoryBinaryStateResponseObject, error) {
	if a.Binaries == nil {
		return nil, apierror.ErrNotImplemented
	}
	counts, err := a.Binaries.Counts(ctx, req.RepoKey, true)
	if err != nil {
		return nil, federatedError(ctx, req.RepoKey, err)
	}
	return artgen.GetFederatedRepositoryBinaryState200JSONResponse{FailedTasks: counts.FailedTasks}, nil
}
