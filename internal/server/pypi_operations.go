package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/server/pypi"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func (s artifactoryService) GetPypiLatestPackageVersionMetadataInJson(
	ctx context.Context,
	req artgen.GetPypiLatestPackageVersionMetadataInJsonRequestObject,
) (artgen.GetPypiLatestPackageVersionMetadataInJsonResponseObject, error) {
	doc, err := s.pypiJSON(ctx, req.Repository, req.Package, "")
	return pypiJSONResponse(doc), err
}

func (s artifactoryService) GetPypiPackageVersionMetadataInJson(
	ctx context.Context,
	req artgen.GetPypiPackageVersionMetadataInJsonRequestObject,
) (artgen.GetPypiPackageVersionMetadataInJsonResponseObject, error) {
	doc, err := s.pypiJSON(ctx, req.Repository, req.Package, req.Version)
	return pypiJSONResponse(doc), err
}

type pypiJSONResponse map[string]any

func (doc pypiJSONResponse) VisitGetPypiLatestPackageVersionMetadataInJsonResponse(
	w http.ResponseWriter,
) error {
	return doc.write(w)
}

func (doc pypiJSONResponse) VisitGetPypiPackageVersionMetadataInJsonResponse(
	w http.ResponseWriter,
) error {
	return doc.write(w)
}

func (doc pypiJSONResponse) write(w http.ResponseWriter) error {
	if err := pypi.WriteMetadata(w, doc, false); err != nil {
		return fmt.Errorf("pypi metadata response: %w", err)
	}
	return nil
}

func (s artifactoryService) pypiJSON(
	ctx context.Context,
	key, name, version string,
) (map[string]any, error) {
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	c, err := st.Repos().Get(ctx, key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, apierror.New(
			http.StatusNotFound,
			fmt.Sprintf("Repository '%s' does not exist", key),
		)
	case err != nil:
		return nil, s.internalError(ctx, "pypi repository", err)
	case c.PackageType != repo.PyPI:
		return nil, apierror.New(
			http.StatusNotFound,
			fmt.Sprintf("Repository '%s' is not a pypi repository", key),
		)
	case c.Class != repo.Local && c.Class != repo.Federated:
		return nil, apierror.New(http.StatusNotImplemented, fmt.Sprintf(
			"'%s' is a %s repository, which artifactweave does not serve through the pypi API yet",
			key, c.Class))
	}
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	acc, err := accessFor(ctx, st, p)
	if err != nil {
		return nil, s.internalError(ctx, "pypi read rights", err)
	}
	if !acc.folder(c, "", types.ActionRead) {
		return nil, denied(p)
	}
	doc, err := pypi.JSONMetadata(ctx, st, key, name, version,
		pypi.RepoURL(baseurl.From(ctx), key),
		func(path string) bool { return acc.file(c, path, types.ActionRead) })
	if err != nil {
		return nil, s.itemError(ctx, "pypi metadata", err)
	}
	return doc, nil
}
