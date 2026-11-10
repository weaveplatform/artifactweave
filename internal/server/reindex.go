package server

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	artgen "github.com/weaveplatform/artifactweave/migration/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
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

// CalculateBowerIndex rebuilds the native Bower version index from manifests.
func (s artifactoryAPI) CalculateBowerIndex(
	ctx context.Context,
	req artgen.CalculateBowerIndexRequestObject,
) (artgen.CalculateBowerIndexResponseObject, error) {
	msg, err := s.Reindex(ctx, repo.Bower, req.RepoKey, "", false)
	return artgen.CalculateBowerIndex200TextResponse(msg), err
}

// CalculateConanRepositoryMetadata rebuilds the index from completed
// recipe/package revisions. Conan manifests are the commit markers.
func (s artifactoryAPI) CalculateConanRepositoryMetadata(
	ctx context.Context,
	req artgen.CalculateConanRepositoryMetadataRequestObject,
) (artgen.CalculateConanRepositoryMetadataResponseObject, error) {
	msg, err := s.Reindex(ctx, repo.Conan, req.RepoKey, "", false)
	return artgen.CalculateConanRepositoryMetadata200TextResponse(msg), err
}

// CalculateCranRepositoryMetadata rebuilds the hosted PACKAGES indexes. The
// documented default is synchronous; async=true only schedules the work.
func (s artifactoryAPI) CalculateCranRepositoryMetadata(
	ctx context.Context,
	req artgen.CalculateCranRepositoryMetadataRequestObject,
) (artgen.CalculateCranRepositoryMetadataResponseObject, error) {
	wait := req.Params.Async == nil || !*req.Params.Async
	msg, err := s.Reindex(ctx, repo.Cran, req.RepoKey, "", wait)
	return artgen.CalculateCranRepositoryMetadata200TextResponse(msg), err
}

// CalculateAlpineRepositoryMetadata queues the documented asynchronous
// APKINDEX rebuild from the current stored package tree.
func (s artifactoryAPI) CalculateAlpineRepositoryMetadata(
	ctx context.Context,
	req artgen.CalculateAlpineRepositoryMetadataRequestObject,
) (artgen.CalculateAlpineRepositoryMetadataResponseObject, error) {
	msg, err := s.Reindex(ctx, repo.Alpine, req.RepoKey, "", false)
	return artgen.CalculateAlpineRepositoryMetadata200TextResponse(msg), err
}

// CalculateCargoRepositoryMetadata validates and rebuilds the hosted crate view.
func (s artifactoryAPI) CalculateCargoRepositoryMetadata(
	ctx context.Context,
	req artgen.CalculateCargoRepositoryMetadataRequestObject,
) (artgen.CalculateCargoRepositoryMetadataResponseObject, error) {
	msg, err := s.Reindex(ctx, repo.Cargo, req.RepoKey, "", false)
	return artgen.CalculateCargoRepositoryMetadata200TextResponse(msg), err
}

func (s artifactoryAPI) CalculateRubyGemsIndex(
	ctx context.Context, req artgen.CalculateRubyGemsIndexRequestObject,
) (artgen.CalculateRubyGemsIndexResponseObject, error) {
	msg, err := s.Reindex(ctx, repo.Gems, req.RepoKey, "", true)
	return artgen.CalculateRubyGemsIndex200TextResponse(msg), err
}

// CalculateOpkgRepositoryMetadata rebuilds Packages, Packages.gz and Release
// from the current IPKs in each feed. The documented endpoint is synchronous
// unless async=true is selected.
func (s artifactoryAPI) CalculateOpkgRepositoryMetadata(
	ctx context.Context,
	req artgen.CalculateOpkgRepositoryMetadataRequestObject,
) (artgen.CalculateOpkgRepositoryMetadataResponseObject, error) {
	wait := req.Params.Async == nil || !*req.Params.Async
	kind := format.KindReindex
	if req.Params.WriteProps != nil && *req.Params.WriteProps == 0 {
		kind = format.KindReindexNoProps
	}
	msg, err := s.ReindexAs(ctx, repo.Opkg, req.RepoKey, "", wait, kind)
	return artgen.CalculateOpkgRepositoryMetadata200TextResponse(msg), err
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
func (s artifactoryAPI) helmPartialReindex(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, p, ok := helmPartialReindexPath(r)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		msg, err := s.Reindex(r.Context(), repo.Helm, key, p, false)
		if err != nil {
			httpkit.WriteErr(w, r, err)
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

func (s artifactoryAPI) CalculatePubRepositoryMetadata(
	ctx context.Context,
	req artgen.CalculatePubRepositoryMetadataRequestObject,
) (artgen.CalculatePubRepositoryMetadataResponseObject, error) {
	async := req.Params.Async != nil && *req.Params.Async
	msg, err := s.Reindex(ctx, repo.Pub, req.RepoKey, "", !async)
	if err != nil {
		return nil, err
	}
	return artgen.CalculatePubRepositoryMetadata200TextResponse(msg), nil
}

func (s artifactoryAPI) CalculateCocoaPodsIndex(
	ctx context.Context,
	req artgen.CalculateCocoaPodsIndexRequestObject,
) (artgen.CalculateCocoaPodsIndexResponseObject, error) {
	msg, err := s.Reindex(ctx, repo.CocoaPods, req.RepoKey, "", false)
	if err != nil {
		return nil, err
	}
	return artgen.CalculateCocoaPodsIndex200TextResponse(msg), nil
}

// CalculateCondaRepositoryMetadata rebuilds hosted channel indexes from
// stored package archives. The documented async query controls whether the
// response waits for the persistent worker.
func (s artifactoryAPI) CalculateCondaRepositoryMetadata(
	ctx context.Context,
	req artgen.CalculateCondaRepositoryMetadataRequestObject,
) (artgen.CalculateCondaRepositoryMetadataResponseObject, error) {
	async := req.Params.Async != nil && *req.Params.Async
	msg, err := s.Reindex(ctx, repo.Conda, req.RepoKey, "", !async)
	if err != nil {
		return nil, err
	}
	return artgen.CalculateCondaRepositoryMetadata200TextResponse(msg), nil
}

func (s artifactoryAPI) CalculateHelmChartIndex(
	ctx context.Context,
	req artgen.CalculateHelmChartIndexRequestObject,
) (artgen.CalculateHelmChartIndexResponseObject, error) {
	msg, err := s.Reindex(ctx, repo.Helm, req.RepoKey, "", false)
	if err != nil {
		return nil, err
	}
	return artgen.CalculateHelmChartIndex200TextResponse(msg), nil
}

func (s artifactoryAPI) CalculateNuGetRepositoryMetadata(
	ctx context.Context,
	req artgen.CalculateNuGetRepositoryMetadataRequestObject,
) (artgen.CalculateNuGetRepositoryMetadataResponseObject, error) {
	msg, err := s.Reindex(ctx, repo.NuGet, req.RepoKey, "", false)
	if err != nil {
		return nil, err
	}
	return artgen.CalculateNuGetRepositoryMetadata200TextResponse(msg), nil
}

// CalculatePypiRepositoryMetadata runs the reindex before it answers unless
// async is true: "The calculation can be synchronous (the default) or
// asynchronous" (calculatepypirepositorymetadata), with async=1 or 0 in the
// archived reference, which the boolean binding reads too.
func (s artifactoryAPI) CalculatePypiRepositoryMetadata(
	ctx context.Context,
	req artgen.CalculatePypiRepositoryMetadataRequestObject,
) (artgen.CalculatePypiRepositoryMetadataResponseObject, error) {
	async := req.Params.Async != nil && *req.Params.Async
	msg, err := s.Reindex(ctx, repo.PyPI, req.RepoKey, "", !async)
	if err != nil {
		return nil, err
	}
	return artgen.CalculatePypiRepositoryMetadata200TextResponse(msg), nil
}

func (s artifactoryAPI) CalculateTerraformIndex(
	ctx context.Context,
	req artgen.CalculateTerraformIndexRequestObject,
) (artgen.CalculateTerraformIndexResponseObject, error) {
	msg, err := s.Reindex(ctx, repo.Terraform, req.RepoKey, "", false)
	if err != nil {
		return nil, err
	}
	return artgen.CalculateTerraformIndex200TextResponse(msg), nil
}

func (s artifactoryAPI) CalculateSwiftIndex(
	ctx context.Context,
	req artgen.CalculateSwiftIndexRequestObject,
) (artgen.CalculateSwiftIndexResponseObject, error) {
	msg, err := s.Reindex(ctx, repo.Swift, req.RepoKey, "", false)
	if err != nil {
		return nil, err
	}
	return artgen.CalculateSwiftIndex200TextResponse(msg), nil
}
