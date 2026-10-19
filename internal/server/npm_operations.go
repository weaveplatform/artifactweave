package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

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
// Calculate npm Repository Metadata) and queues the npm format's work on
// the persistent work queue, keyed by the repository (decision 0033), as
// 7.x schedules them rather than running them before it answers.

// npmOperation resolves the repository an npm operation names: 404 when
// there is none, 401 or 403 to a caller without Manage on it, and 400 for
// a repository that is no npm local one. [I] The order of 7.x's checks is
// unknown (#216).
func (s artifactoryService) npmOperation(
	ctx context.Context,
	key string,
) (store.Store, repo.Config, string, error) {
	st, err := s.content()
	if err != nil {
		return nil, repo.Config{}, "", err
	}
	c, cached, err := resolveRepo(ctx, st, key)
	if err != nil {
		return nil, c, "", s.itemError(ctx, "resolve repository", err)
	}
	p, err := principal(ctx)
	if err != nil {
		return nil, c, "", err
	}
	acc, err := accessFor(ctx, st, p)
	if err != nil {
		return nil, c, "", s.internalError(ctx, "npm operation rights", err)
	}
	if !acc.file(c, "", types.ActionManage) {
		return nil, c, "", denied(p)
	}
	if cached || c.PackageType != repo.NPM || c.Class != repo.Local {
		return nil, c, "", badRequest("Repository '%s' is not a local npm repository", key)
	}
	return st, c, p.Name, nil
}

// queueNpm asks for the npm format's work of kind on the repository key
// names, for its caller.
func (s artifactoryService) queueNpm(ctx context.Context, key, kind string) error {
	st, c, by, err := s.npmOperation(ctx, key)
	if err != nil {
		return err
	}
	return s.enqueueNpm(ctx, st, kind, c.Key, by)
}

// enqueueNpm queues kind's work on key through the runtime's queue, which
// wakes its workers; a server without one, in tests, leaves the item in
// the store for whichever queue polls it.
func (s artifactoryService) enqueueNpm(
	ctx context.Context,
	st store.Store,
	kind, key, by string,
) error {
	// A struct of one string always marshals.
	payload, _ := json.Marshal(npmfmt.WorkRequest{By: by})
	var err error
	if s.cfg.Work != nil {
		err = s.cfg.Work.Enqueue(ctx, kind, key, payload)
	} else {
		err = st.WorkQueue().Enqueue(ctx, kind, key, payload, time.Now())
	}
	if err != nil {
		return s.internalError(ctx, "queue "+kind, err)
	}
	return nil
}

func (s artifactoryService) CalculateNpmRepositoryMetadata(
	ctx context.Context,
	req artgen.CalculateNpmRepositoryMetadataRequestObject,
) (artgen.CalculateNpmRepositoryMetadataResponseObject, error) {
	if err := s.queueNpm(ctx, req.RepoKey, npmfmt.KindReindex); err != nil {
		return nil, err
	}
	// 7.x's answer, word for word (JFrog's archived REST reference, KB
	// 000005641).
	return artgen.CalculateNpmRepositoryMetadata200TextResponse(fmt.Sprintf(
		"Recalculating index for npm repository %s scheduled to run", req.RepoKey)), nil
}

func (s artifactoryService) MigrateNpmRepositoryToStrictLayout(
	ctx context.Context,
	req artgen.MigrateNpmRepositoryToStrictLayoutRequestObject,
) (artgen.MigrateNpmRepositoryToStrictLayoutResponseObject, error) {
	if err := s.queueNpm(ctx, req.RepoKey, npmfmt.KindConvert); err != nil {
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
	if err := s.enqueueNpm(ctx, st, npmfmt.KindConvert, c.Key, by); err != nil {
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
