package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The JFrog reindex operations of the package types artifactweave serves
// (decision 0033). Each queues its format's rebuild of a repository's index
// on the persistent work queue, as format.KindReindex keyed by the
// repository, or as format.KindReindexPath for Helm's partial reindex, and
// answers 7.x's text once it is queued, since 7.x schedules them ("an
// asynchronous reindexing of a local repository", KB 000005641). PyPI's
// alone runs before it answers, unless asked not to. Until the format of an
// operation's package type is a format.Reindexer the operation answers 501,
// as the stub did, before anything else is checked. The reindex operations
// of the other package types, which no format here serves, keep the
// generated stub.

// reindexOp is how 7.x serves one package type's reindex operation.
type reindexOp struct {
	// admin: only an administrator may ask. Otherwise Manage on the
	// repository is enough.
	admin bool
	// classes are the repository classes 7.x reindexes and artifactweave
	// serves. later are those 7.x reindexes that artifactweave serves only
	// from P9 (remote, a remote's cache included) and P10 (virtual), which
	// answer 501 until then (decision 0032). Any other class, or another
	// package type, answers 400 with refused, a format taking the key.
	classes, later []repo.Class
	refused        string
	// answer is the 200's text, a format taking the key.
	answer string
}

// hosted are the classes whose own items a reindex reads. A federated
// repository is a local one that federates. [I] Of the reindex pages only
// Helm's names it; RTFACT-27144 says the REST reindex reaches it (#216).
var hosted = []repo.Class{repo.Local, repo.Federated}

// reindexOps are the operations by package type, each from 7.x's REST
// reference: the archived pages for the permission, the classes and the
// answer, and the current ones for the classes. [I] The order of the checks
// and the text of each refusal are inferred (#216).
var reindexOps = map[repo.PackageType]reindexOp{
	// Manage, local repositories only, scheduled, and 7.x's answer word for
	// word (decision 0034; KB 000005641). Federated ones answer 400, as #293
	// left them.
	repo.NPM: {
		classes: []repo.Class{repo.Local},
		refused: "Repository '%s' is not a local npm repository",
		answer:  "Recalculating index for npm repository %s scheduled to run",
	},
	// "Requires a user with Manage permissions", "Supported for local
	// repositories only", synchronous unless async is set, and the sample
	// answer. The text of a synchronous run's answer is the same [I].
	repo.PyPI: {
		classes: hosted,
		refused: "Repository '%s' is not a local pypi repository",
		answer:  "Recalculating index for pypi repository %s scheduled to run",
	},
	// "From version 4.8 only requires the set of permissions assumed by
	// Manage", "Supported by local, local-cache, remote and virtual
	// repositories", and the sample answer, which accepts it to run later.
	repo.NuGet: {
		classes: hosted,
		later:   []repo.Class{repo.Remote, repo.Virtual},
		refused: "Repository '%s' is not a NuGet repository",
		answer:  "NuGet reindex calculation for repository '%s' accepted.",
	},
	// "Requires the set of permissions assumed by Manage", "local and
	// Federated repositories only". The archived sample shows only OK; the
	// text is what Artifactory answered the helm-push-artifactory plugin in
	// 2018, as its README prints it [I].
	repo.Helm: {
		classes: hosted,
		refused: "Repository '%s' is not a local helm repository",
		answer:  "Reindex of helm repo %s was scheduled to run.",
	},
	// "Requires a valid admin user", a local repository (the Terraform
	// page: "re-indexing of a local Terraform repository"), and the sample
	// answer.
	repo.Terraform: {
		admin:   true,
		classes: hosted,
		refused: "Repository '%s' is not a local terraform repository",
		answer:  "Reindexing of Terraform repository %s was scheduled to run.",
	},
}

// reindex queues the reindex operation of package type t on the repository
// key names, or on path in it when path is not empty, for its caller. It
// answers 7.x's text once the work is queued, or once it has run when wait.
func (s artifactoryService) reindex(
	ctx context.Context,
	t repo.PackageType,
	key, path string,
	wait bool,
) (string, error) {
	op, ok := reindexOps[t]
	if _, indexed := s.format(t).(format.Reindexer); !ok || !indexed {
		return "", apierror.ErrNotImplemented
	}
	st, c, by, err := s.operationRepo(ctx, t, op, key)
	if err != nil {
		return "", err
	}
	kind, workKey := format.KindReindex, c.Key
	if path != "" {
		kind, workKey = format.KindReindexPath, c.Key+"/"+path
		// [I] 7.x's answer to an overlong path is unknown (#216).
		if len(workKey) > store.MaxWorkKey {
			return "", badRequest("The path '%s' is too long to reindex", path)
		}
	}
	if err := s.enqueue(ctx, st, kind, workKey, by); err != nil {
		return "", err
	}
	if wait {
		if err := s.await(ctx, st, kind, workKey); err != nil {
			return "", s.itemError(ctx, "reindex", err)
		}
	}
	return fmt.Sprintf(op.answer, key), nil
}

// operationRepo resolves the repository a JFrog operation of package type
// t names and checks it as op says: 404 when there is none, 401 or 403 to a
// caller without the right, 400 for a repository op refuses, and 501 for
// one it serves later. It returns the caller's name.
func (s artifactoryService) operationRepo(
	ctx context.Context,
	t repo.PackageType,
	op reindexOp,
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
	if op.admin && !p.Admin {
		return nil, c, "", denied(p)
	}
	if !op.admin {
		acc, err := accessFor(ctx, st, p)
		if err != nil {
			return nil, c, "", s.internalError(ctx, "operation rights", err)
		}
		if !acc.file(c, "", types.ActionManage) {
			return nil, c, "", denied(p)
		}
	}
	class := c.Class
	if cached {
		class = repo.Remote
	}
	switch {
	case c.PackageType == t && slices.Contains(op.later, class):
		return nil, c, "", apierror.New(http.StatusNotImplemented, fmt.Sprintf(
			"'%s' is a %s repository, which artifactweave does not reindex yet", key, class))
	case c.PackageType != t || !slices.Contains(op.classes, class):
		return nil, c, "", badRequest(op.refused, key)
	}
	return st, c, p.Name, nil
}

// enqueue queues kind's work on key, asked for by by, through the runtime's
// queue, which wakes its workers; a server without one, in tests, leaves
// the item in the store for whichever queue polls it.
func (s artifactoryService) enqueue(
	ctx context.Context,
	st store.Store,
	kind, key, by string,
) error {
	// A struct of one string always marshals.
	payload, _ := json.Marshal(format.WorkRequest{By: by})
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

// await waits for the work of kind on key queued just now: it runs it here,
// or waits for the worker already running it, as a read of derived work
// does. A server without the runtime's queue, in tests, runs it through a
// queue of its own.
func (s artifactoryService) await(ctx context.Context, st store.Store, kind, key string) error {
	q := s.cfg.Work
	if q == nil {
		var err error
		if q, err = work.New(st, work.Options{}); err == nil {
			err = s.items(st).RegisterWork(q)
		}
		if err != nil {
			return fmt.Errorf("work queue: %w", err)
		}
	}
	if err := q.EnsureFresh(ctx, kind, key); err != nil {
		return fmt.Errorf("%s of %s: %w", kind, key, err)
	}
	return nil
}

// helmAPI prefixes Helm's routes and its JFrog operations.
const helmAPI = "/artifactory/api/helm/"

// helmPartialReindex serves Helm's partial reindex, POST
// /artifactory/api/helm/{repoKey}/{pathForPartialReindex}/reindex, whose
// path may span several segments: "a path containing multiple charts ... as
// well as ... a specific chart archive" (kubernetes-helm-chart-repositories).
// chi routes no parameter across segments and no literal after a
// catch-all, and a catch-all below the repository key would take requests
// from the Helm protocol's router, which the router kit mounts there
// (decision 0032), so the operation is matched before routing. It queues
// the reindex of the path, with the Helm reindex's permission, classes and
// answer [I]: no source states them for the partial one (#216).
func (s artifactoryService) helmPartialReindex(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, p, ok := helmPartialReindexPath(r)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		msg, err := s.reindex(r.Context(), repo.Helm, key, p, false)
		if err != nil {
			writeErr(w, r, err)
			return
		}
		// The generated reindex operations answer the same way.
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, msg) //nolint:gosec // G705: plain text, which no browser runs
	})
}

// helmPartialReindexPath reports whether r is a partial Helm reindex, and
// returns the repository key and the path it names, cleaned. A path that
// cleans to the repository's root, such as ".", names all of it [I].
func helmPartialReindexPath(r *http.Request) (string, string, bool) {
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), helmAPI)
	if r.Method != http.MethodPost || !ok {
		return "", "", false
	}
	escKey, escPath, _ := strings.Cut(rest, "/")
	escPath, ok = strings.CutSuffix(escPath, "/reindex")
	key, keyErr := url.PathUnescape(escKey)
	p, pathErr := url.PathUnescape(escPath)
	if !ok || key == "" || escPath == "" || keyErr != nil || pathErr != nil {
		return "", "", false
	}
	return key, strings.Trim(path.Clean("/"+p), "/"), true
}

// The generated reindex operations of the package types with a format.

func (s artifactoryService) CalculateHelmChartIndex(
	ctx context.Context,
	req artgen.CalculateHelmChartIndexRequestObject,
) (artgen.CalculateHelmChartIndexResponseObject, error) {
	msg, err := s.reindex(ctx, repo.Helm, req.RepoKey, "", false)
	if err != nil {
		return nil, err
	}
	return artgen.CalculateHelmChartIndex200TextResponse(msg), nil
}

func (s artifactoryService) CalculateNuGetRepositoryMetadata(
	ctx context.Context,
	req artgen.CalculateNuGetRepositoryMetadataRequestObject,
) (artgen.CalculateNuGetRepositoryMetadataResponseObject, error) {
	msg, err := s.reindex(ctx, repo.NuGet, req.RepoKey, "", false)
	if err != nil {
		return nil, err
	}
	return artgen.CalculateNuGetRepositoryMetadata200TextResponse(msg), nil
}

// CalculatePypiRepositoryMetadata runs the reindex before it answers unless
// async is true: "The calculation can be synchronous (the default) or
// asynchronous" (calculatepypirepositorymetadata), with async=1 or 0 in the
// archived reference, which the boolean binding reads too.
func (s artifactoryService) CalculatePypiRepositoryMetadata(
	ctx context.Context,
	req artgen.CalculatePypiRepositoryMetadataRequestObject,
) (artgen.CalculatePypiRepositoryMetadataResponseObject, error) {
	async := req.Params.Async != nil && *req.Params.Async
	msg, err := s.reindex(ctx, repo.PyPI, req.RepoKey, "", !async)
	if err != nil {
		return nil, err
	}
	return artgen.CalculatePypiRepositoryMetadata200TextResponse(msg), nil
}

func (s artifactoryService) CalculateTerraformIndex(
	ctx context.Context,
	req artgen.CalculateTerraformIndexRequestObject,
) (artgen.CalculateTerraformIndexResponseObject, error) {
	msg, err := s.reindex(ctx, repo.Terraform, req.RepoKey, "", false)
	if err != nil {
		return nil, err
	}
	return artgen.CalculateTerraformIndex200TextResponse(msg), nil
}
