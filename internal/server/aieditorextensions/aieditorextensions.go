// Package aieditorextensions serves the VS Code gallery API for read-only
// remote extension repositories. The IDE has no header-authentication channel
// for this gallery; repository policy still controls anonymous read access.
package aieditorextensions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	format "github.com/weaveplatform/artifactweave/internal/format/aieditorextensions"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const galleryPath = "extensionquery"

func Protocol(svc *source.Service) pkgroute.Protocol {
	sources := map[repo.Class]pkgroute.Source{}
	if svc != nil {
		sources[repo.Remote] = pkgroute.Remote{Service: svc}
	}
	return pkgroute.Protocol{
		Name: "aieditorextensions", Types: []repo.PackageType{repo.AIEditorExtensions},
		ErrorProfile: apierror.ProfileArtifactory, Sources: sources,
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k}
			r.Post("/_apis/public/gallery/extensionquery", h.query)
			r.Get("/assets/{publisher}/{name}/{version}/{assetType}", h.asset)
			r.Head("/assets/{publisher}/{name}/{version}/{assetType}", h.asset)
		},
	}
}

type handler struct{ k *pkgroute.Kit }

func (h handler) query(w http.ResponseWriter, r *http.Request) {
	q, err := parseQuery(r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	catalog, original, err := h.catalog(r, q)
	if err != nil {
		h.k.Error(w, r, sourceFailure(err))
		return
	}
	assetBase := baseurl.From(r.Context()) + "/artifactory/api/aieditorextensions/" +
		url.PathEscape(pkgroute.RequestFrom(r).Repo.Key)
	projected, paths, err := projectCatalog(
		r.Context(),
		catalog,
		assetBase,
		func(ctx context.Context, path string) bool {
			if !h.k.Can(r, path, types.ActionRead) {
				return false
			}
			return original.CheckRead == nil || original.CheckRead(ctx, path) == nil
		},
	)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	result := projectedResult(original, projected, paths)
	h.k.ServeResult(w, r, result)
}

func (h handler) catalog(r *http.Request, q parsedQuery) (galleryCatalog, source.Result, error) {
	c := pkgroute.RequestFrom(r).Repo
	permission := ""
	if q.Publisher != "" {
		permission = format.ExtensionFolder(q.Publisher, q.Name)
	}
	resource := source.Resource{
		Kind:                source.ResourceKindMetadata,
		Path:                q.Path,
		UpstreamPath:        galleryPath,
		Method:              http.MethodPost,
		PostBody:            q.Body,
		PermissionFolder:    true,
		PermissionPath:      permission,
		Variant:             "aieditorextensions-gallery-v1",
		Accept:              galleryAccept,
		ExpectedContentType: "application/json",
		ValidateMetadata: func(_ context.Context, body io.ReadSeeker, size int64, response source.MetadataResponse) error {
			_, err := readCatalog(body, size, response.BaseURL, q)
			return err
		},
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return galleryCatalog{}, source.Result{}, fmt.Errorf("read extension gallery: %w", err)
	}
	if result.Stream == nil {
		return galleryCatalog{}, source.Result{}, errCatalog
	}
	defer result.Stream.Body.Close()
	catalog, err := readCatalog(result.Stream.Body, result.Stream.Size, c.URL+"/"+galleryPath, q)
	if err != nil {
		return galleryCatalog{}, source.Result{}, err
	}
	return catalog, result, nil
}

func projectedResult(original source.Result, data []byte, paths []string) source.Result {
	result := source.Result{
		Stream: &source.Stream{
			Body: io.NopCloser(bytes.NewReader(data)), Status: http.StatusOK,
			Representation: source.Representation{
				MediaType: "application/json", Size: int64(len(data)), ETag: projectedETag(data),
			},
		},
		Used: original.Used,
	}
	result.Revalidate = func(ctx context.Context) error {
		if original.Revalidate != nil {
			if err := original.Revalidate(ctx); err != nil {
				return fmt.Errorf("revalidate gallery source: %w", err)
			}
		}
		for _, path := range paths {
			if original.CheckRead != nil {
				if err := original.CheckRead(ctx, path); err != nil {
					return fmt.Errorf("revalidate extension read: %w", err)
				}
			}
		}
		return nil
	}
	return result
}

func exactQuery(publisher, name string) parsedQuery {
	q := galleryQuery{Filters: []filter{{
		Criteria: []criterion{
			{FilterType: 7, Value: publisher + "." + name},
			{FilterType: 8, Value: "Microsoft.VisualStudio.Code"},
			{FilterType: 12, Value: "4096"},
		},
		PageNumber: 1, PageSize: 1, SortBy: 0, SortOrder: 0,
	}}, AssetTypes: []string{}, Flags: 950}
	body, _ := json.Marshal(q) // fixed schema and values
	return parsedQuery{Body: body, Path: format.QueryPath(body), Publisher: publisher, Name: name}
}

func notFound() error { return apierror.New(http.StatusNotFound, "extension asset not found") }

func badAsset() error { return apierror.New(http.StatusBadRequest, "invalid extension asset") }

func validAssetQuery(raw string) bool {
	if raw == "" {
		return true
	}
	values, err := url.ParseQuery(raw)
	if err != nil || len(values) > 2 {
		return false
	}
	for key, value := range values {
		if key != "redirect" && key != "install" || len(value) != 1 || value[0] != "true" {
			return false
		}
	}
	return true
}

func sourceFailure(err error) error {
	if errors.Is(err, errCatalog) {
		return apierror.New(http.StatusBadGateway, "invalid extension gallery metadata")
	}
	return err
}

func safeAssetIdentity(publisher, name, version, assetType string) (string, error) {
	p, n, ok := format.Identity(publisher + "." + name)
	if !ok || p != publisher || n != name || !format.Version(version) ||
		strings.ContainsAny(assetType, "/\\%") {
		return "", badAsset()
	}
	path, ok := format.AssetCachePath(publisher, name, version, assetType)
	if !ok {
		return "", badAsset()
	}
	return path, nil
}
