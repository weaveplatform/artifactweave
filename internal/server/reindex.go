package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// reindex answers a JFrog reindex operation for package type t by
// rebuilding key's index through its format (decision 0032). Until t's
// format is a Reindexer the operation answers 501, exactly as the stub
// did, before anything else is checked. Reindexing is administration, so
// only an admin may ask for it: no source states Artifactory's rule, and
// this is the narrowest (#216).
func (s artifactoryService) reindex(
	ctx context.Context,
	key string,
	t repo.PackageType,
) (string, error) {
	if _, ok := s.format(t).(format.Reindexer); !ok {
		return "", apierror.ErrNotImplemented
	}
	if err := adminOnly(ctx); err != nil {
		return "", err
	}
	st, err := s.content()
	if err != nil {
		return "", err
	}
	c, cached, err := resolveRepo(ctx, st, key)
	switch {
	case err != nil:
		return "", s.itemError(ctx, "resolve repository", err)
	case cached || c.PackageType != t:
		return "", badRequest("Repository '%s' is not a %s repository", key, t)
	// Decision 0034: the npm operations act on local repositories only. The
	// format itself rebuilds any repository's index, a remote's from its
	// cache, as the contract suite holds it to.
	case t == repo.NPM && c.Class != repo.Local:
		return "", badRequest("Repository '%s' is not a local npm repository", key)
	}
	err = s.items(st).Reindex(ctx, c)
	switch {
	case errors.Is(err, item.ErrNoReindex):
		return "", apierror.ErrNotImplemented
	case err != nil:
		return "", s.itemError(ctx, "reindex", err)
	}
	return fmt.Sprintf("The index of repository '%s' was recalculated", key), nil
}

// The generated reindex operations, one per package type, all answered by
// reindex.

func (s artifactoryService) CalculateAlpineRepositoryMetadata(
	ctx context.Context,
	req artgen.CalculateAlpineRepositoryMetadataRequestObject,
) (artgen.CalculateAlpineRepositoryMetadataResponseObject, error) {
	msg, err := s.reindex(ctx, req.RepoKey, repo.Alpine)
	if err != nil {
		return nil, err
	}
	return artgen.CalculateAlpineRepositoryMetadata200TextResponse(msg), nil
}

func (s artifactoryService) CalculateBowerIndex(
	ctx context.Context,
	req artgen.CalculateBowerIndexRequestObject,
) (artgen.CalculateBowerIndexResponseObject, error) {
	msg, err := s.reindex(ctx, req.RepoKey, repo.Bower)
	if err != nil {
		return nil, err
	}
	return artgen.CalculateBowerIndex200TextResponse(msg), nil
}

func (s artifactoryService) CalculateCargoRepositoryMetadata(
	ctx context.Context,
	req artgen.CalculateCargoRepositoryMetadataRequestObject,
) (artgen.CalculateCargoRepositoryMetadataResponseObject, error) {
	msg, err := s.reindex(ctx, req.RepoKey, repo.Cargo)
	if err != nil {
		return nil, err
	}
	return artgen.CalculateCargoRepositoryMetadata200TextResponse(msg), nil
}

func (s artifactoryService) CalculateCocoaPodsIndex(
	ctx context.Context,
	req artgen.CalculateCocoaPodsIndexRequestObject,
) (artgen.CalculateCocoaPodsIndexResponseObject, error) {
	msg, err := s.reindex(ctx, req.RepoKey, repo.CocoaPods)
	if err != nil {
		return nil, err
	}
	return artgen.CalculateCocoaPodsIndex200TextResponse(msg), nil
}

func (s artifactoryService) CalculateConanRepositoryMetadata(
	ctx context.Context,
	req artgen.CalculateConanRepositoryMetadataRequestObject,
) (artgen.CalculateConanRepositoryMetadataResponseObject, error) {
	msg, err := s.reindex(ctx, req.RepoKey, repo.Conan)
	if err != nil {
		return nil, err
	}
	return artgen.CalculateConanRepositoryMetadata200TextResponse(msg), nil
}

func (s artifactoryService) CalculateCondaRepositoryMetadata(
	ctx context.Context,
	req artgen.CalculateCondaRepositoryMetadataRequestObject,
) (artgen.CalculateCondaRepositoryMetadataResponseObject, error) {
	msg, err := s.reindex(ctx, req.RepoKey, repo.Conda)
	if err != nil {
		return nil, err
	}
	return artgen.CalculateCondaRepositoryMetadata200TextResponse(msg), nil
}

func (s artifactoryService) CalculateCranRepositoryMetadata(
	ctx context.Context,
	req artgen.CalculateCranRepositoryMetadataRequestObject,
) (artgen.CalculateCranRepositoryMetadataResponseObject, error) {
	msg, err := s.reindex(ctx, req.RepoKey, repo.Cran)
	if err != nil {
		return nil, err
	}
	return artgen.CalculateCranRepositoryMetadata200TextResponse(msg), nil
}

func (s artifactoryService) CalculateDebianRepositoryMetadata(
	ctx context.Context,
	req artgen.CalculateDebianRepositoryMetadataRequestObject,
) (artgen.CalculateDebianRepositoryMetadataResponseObject, error) {
	msg, err := s.reindex(ctx, req.RepoKey, repo.Debian)
	if err != nil {
		return nil, err
	}
	return artgen.CalculateDebianRepositoryMetadata200TextResponse(msg), nil
}

func (s artifactoryService) CalculateRubyGemsIndex(
	ctx context.Context,
	req artgen.CalculateRubyGemsIndexRequestObject,
) (artgen.CalculateRubyGemsIndexResponseObject, error) {
	msg, err := s.reindex(ctx, req.RepoKey, repo.Gems)
	if err != nil {
		return nil, err
	}
	return artgen.CalculateRubyGemsIndex200TextResponse(msg), nil
}

func (s artifactoryService) CalculateHelmChartIndex(
	ctx context.Context,
	req artgen.CalculateHelmChartIndexRequestObject,
) (artgen.CalculateHelmChartIndexResponseObject, error) {
	msg, err := s.reindex(ctx, req.RepoKey, repo.Helm)
	if err != nil {
		return nil, err
	}
	return artgen.CalculateHelmChartIndex200TextResponse(msg), nil
}

func (s artifactoryService) CalculateNpmRepositoryMetadata(
	ctx context.Context,
	req artgen.CalculateNpmRepositoryMetadataRequestObject,
) (artgen.CalculateNpmRepositoryMetadataResponseObject, error) {
	msg, err := s.reindex(ctx, req.RepoKey, repo.NPM)
	if err != nil {
		return nil, err
	}
	return artgen.CalculateNpmRepositoryMetadata200TextResponse(msg), nil
}

func (s artifactoryService) CalculateNuGetRepositoryMetadata(
	ctx context.Context,
	req artgen.CalculateNuGetRepositoryMetadataRequestObject,
) (artgen.CalculateNuGetRepositoryMetadataResponseObject, error) {
	msg, err := s.reindex(ctx, req.RepoKey, repo.NuGet)
	if err != nil {
		return nil, err
	}
	return artgen.CalculateNuGetRepositoryMetadata200TextResponse(msg), nil
}

func (s artifactoryService) CalculateOpkgRepositoryMetadata(
	ctx context.Context,
	req artgen.CalculateOpkgRepositoryMetadataRequestObject,
) (artgen.CalculateOpkgRepositoryMetadataResponseObject, error) {
	msg, err := s.reindex(ctx, req.RepoKey, repo.Opkg)
	if err != nil {
		return nil, err
	}
	return artgen.CalculateOpkgRepositoryMetadata200TextResponse(msg), nil
}

func (s artifactoryService) CalculatePubRepositoryMetadata(
	ctx context.Context,
	req artgen.CalculatePubRepositoryMetadataRequestObject,
) (artgen.CalculatePubRepositoryMetadataResponseObject, error) {
	msg, err := s.reindex(ctx, req.RepoKey, repo.Pub)
	if err != nil {
		return nil, err
	}
	return artgen.CalculatePubRepositoryMetadata200TextResponse(msg), nil
}

func (s artifactoryService) CalculatePypiRepositoryMetadata(
	ctx context.Context,
	req artgen.CalculatePypiRepositoryMetadataRequestObject,
) (artgen.CalculatePypiRepositoryMetadataResponseObject, error) {
	msg, err := s.reindex(ctx, req.RepoKey, repo.PyPI)
	if err != nil {
		return nil, err
	}
	return artgen.CalculatePypiRepositoryMetadata200TextResponse(msg), nil
}

func (s artifactoryService) CalculateSwiftIndex(
	ctx context.Context,
	req artgen.CalculateSwiftIndexRequestObject,
) (artgen.CalculateSwiftIndexResponseObject, error) {
	msg, err := s.reindex(ctx, req.RepoKey, repo.Swift)
	if err != nil {
		return nil, err
	}
	return artgen.CalculateSwiftIndex200TextResponse(msg), nil
}

func (s artifactoryService) CalculateTerraformIndex(
	ctx context.Context,
	req artgen.CalculateTerraformIndexRequestObject,
) (artgen.CalculateTerraformIndexResponseObject, error) {
	msg, err := s.reindex(ctx, req.RepoKey, repo.Terraform)
	if err != nil {
		return nil, err
	}
	return artgen.CalculateTerraformIndex200TextResponse(msg), nil
}
