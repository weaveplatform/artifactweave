package server

import (
	"context"
	"errors"
	"strings"

	npmfmt "github.com/weaveplatform/artifactweave/internal/format/npm"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The JFrog operations on an npm local repository's index: the reindex and
// the strict-layout conversion with its resume (decision 0034). Each needs
// Manage on the repository ("From version 4.8 only requires the set of
// permissions assumed by Manage", JFrog's archived REST reference for
// Calculate npm Repository Metadata) and queues its work on the persistent
// work queue, keyed by the repository (decision 0033), as 7.x schedules
// them rather than running them before it answers. The reindex is every
// format's (reindex.go); the conversion is the npm format's own work.

// npmOperation resolves the repository an npm operation names and checks
// it as the reindex does: 404 when there is none, 401 or 403 to a caller
// without Manage on it, and 400 for a repository that is no npm local one.
func (s artifactoryService) npmOperation(
	ctx context.Context,
	key string,
) (store.Store, repo.Config, string, error) {
	return s.operationRepo(ctx, repo.NPM, reindexOps[repo.NPM], key)
}

func (s artifactoryService) CalculateNpmRepositoryMetadata(
	ctx context.Context,
	req artgen.CalculateNpmRepositoryMetadataRequestObject,
) (artgen.CalculateNpmRepositoryMetadataResponseObject, error) {
	msg, err := s.reindex(ctx, repo.NPM, req.RepoKey, "", false)
	if err != nil {
		return nil, err
	}
	return artgen.CalculateNpmRepositoryMetadata200TextResponse(msg), nil
}

func (s artifactoryService) MigrateNpmRepositoryToStrictLayout(
	ctx context.Context,
	req artgen.MigrateNpmRepositoryToStrictLayoutRequestObject,
) (artgen.MigrateNpmRepositoryToStrictLayoutResponseObject, error) {
	st, c, by, err := s.npmOperation(ctx, req.RepoKey)
	if err != nil {
		return nil, err
	}
	if err := s.enqueue(ctx, st, npmfmt.KindConvert, c.Key, by); err != nil {
		return nil, err
	}
	return artgen.MigrateNpmRepositoryToStrictLayout202Response{}, nil
}

// ResumeNpmRepositoryMigration queues again a conversion the work queue
// parked once its retries ran out. A conversion cannot be cancelled, so
// that is the only one to resume; any other answers 400. [I] 7.x's answer
// then is unknown (#216).
func (s artifactoryService) ResumeNpmRepositoryMigration(
	ctx context.Context,
	req artgen.ResumeNpmRepositoryMigrationRequestObject,
) (artgen.ResumeNpmRepositoryMigrationResponseObject, error) {
	st, c, by, err := s.npmOperation(ctx, req.RepoKey)
	if err != nil {
		return nil, err
	}
	it, err := st.WorkQueue().Get(ctx, npmfmt.KindConvert, c.Key)
	switch {
	case errors.Is(err, store.ErrNotFound) || err == nil && it.State != types.WorkParked:
		return nil, badRequest("Repository '%s' has no failed conversion to resume", c.Key)
	case err != nil:
		return nil, s.internalError(ctx, "npm conversion", err)
	}
	if err := s.enqueue(ctx, st, npmfmt.KindConvert, c.Key, by); err != nil {
		return nil, err
	}
	return artgen.ResumeNpmRepositoryMigration202Response{}, nil
}

// isNpmReindex reports whether path is the JFrog reindex operation beside
// an npm registry, which answers in Artifactory's envelope, as the JFrog
// operations there do on 7.x (RTFACT-30181), where the registry answers in
// npm's. Only POST is the operation: to a GET, "reindex" is a package, and
// a refusal the server answers before routing gives its npm client
// Artifactory's envelope.
func isNpmReindex(path string) bool {
	rest, ok := strings.CutPrefix(path, "/artifactory/api/npm/")
	if !ok {
		return false
	}
	key, op, ok := strings.Cut(rest, "/")
	return ok && key != "" && op == "reindex"
}
