package aieditorextensions

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	manifestAsset  = "Microsoft.VisualStudio.Code.Manifest"
	packageAsset   = "Microsoft.VisualStudio.Services.VSIXPackage"
	signatureAsset = "Microsoft.VisualStudio.Services.VsixSignature"
)

func (h handler) asset(w http.ResponseWriter, r *http.Request) {
	publisher, name := chi.URLParam(r, "publisher"), chi.URLParam(r, "name")
	version, assetType := chi.URLParam(r, "version"), chi.URLParam(r, "assetType")
	cachePath, err := safeAssetIdentity(publisher, name, version, assetType)
	if err != nil || !validAssetQuery(r.URL.RawQuery) {
		h.k.Error(w, r, badAsset())
		return
	}
	if !h.k.Can(r, cachePath, types.ActionRead) {
		h.k.Error(w, r, notFound())
		return
	}
	catalog, metadata, err := h.catalog(r, exactQuery(publisher, name))
	if err != nil {
		h.k.Error(w, r, sourceFailure(err))
		return
	}
	var versionEntry *galleryVersion
	for i := range catalog.Results[0].Extensions {
		e := &catalog.Results[0].Extensions[i]
		if e.Publisher.PublisherName != publisher || e.ExtensionName != name {
			continue
		}
		for vi := range e.Versions {
			if e.Versions[vi].Version == version {
				versionEntry = &e.Versions[vi]
				break
			}
		}
	}
	if versionEntry == nil {
		h.k.Error(w, r, notFound())
		return
	}
	upstream := fileSource(*versionEntry, assetType)
	if upstream == "" {
		h.k.Error(w, r, notFound())
		return
	}
	c := pkgroute.RequestFrom(r).Repo
	resource := source.Resource{
		Kind: source.Artifact, Path: cachePath, ExternalURL: upstream,
		Method: http.MethodGet, Variant: "aieditorextensions-asset-v1",
		CheckURL: func(_ context.Context, next *url.URL) error {
			if !safeAssetSource(
				c.URL+"/"+galleryPath,
				next.String(),
				publisher,
				name,
				version,
				assetType,
			) {
				return source.ErrDenied
			}
			return nil
		},
	}
	switch assetType {
	case manifestAsset:
		resource.Kind = source.Metadata
		resource.ExpectedContentType = "application/json"
		resource.ValidateMetadata = func(_ context.Context, body io.ReadSeeker, size int64, _ source.MetadataResponse) error {
			return validateManifest(body, size, publisher, name, version)
		}
	case packageAsset:
		if sha := versionProperty(
			*versionEntry,
			"Microsoft.VisualStudio.Services.VsixSha256",
		); sha != "" {
			resource.Checksums.SHA256 = sha
		}
		resource.Validate = func(_ context.Context, body io.ReadSeeker, size int64) error {
			return validatePackage(body, size, publisher, name, version)
		}
	case signatureAsset:
		resource.Kind = source.Metadata
		resource.ExpectedContentTypes = []string{"application/zip", "application/octet-stream"}
		resource.ValidateMetadata = func(_ context.Context, body io.ReadSeeker, size int64, _ source.MetadataResponse) error {
			return validateSignature(
				body,
				size,
				versionProperty(*versionEntry, "Microsoft.VisualStudio.Services.VsixSha256"),
			)
		}
	default:
		// Clients install from the three validated assets above. Additional
		// presentation files are added only with format-specific validators.
		h.k.Error(w, r, notFound())
		return
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		h.k.Error(w, r, fmt.Errorf("read extension asset: %w", err))
		return
	}
	assetFence, assetUsed := result.Revalidate, result.Used
	result.Revalidate = func(ctx context.Context) error {
		if metadata.Revalidate != nil {
			if err := metadata.Revalidate(ctx); err != nil {
				return fmt.Errorf("revalidate asset catalog: %w", err)
			}
		}
		if metadata.CheckRead != nil {
			if err := metadata.CheckRead(ctx, cachePath); err != nil {
				return fmt.Errorf("revalidate asset read: %w", err)
			}
		}
		if assetFence != nil {
			if err := assetFence(ctx); err != nil {
				return fmt.Errorf("revalidate asset source: %w", err)
			}
		}
		return nil
	}
	result.Used = func(ctx context.Context) error {
		if assetUsed != nil {
			if err := assetUsed(ctx); err != nil {
				return fmt.Errorf("mark asset used: %w", err)
			}
		}
		if metadata.Used != nil {
			if err := metadata.Used(ctx); err != nil {
				return fmt.Errorf("mark gallery used: %w", err)
			}
		}
		return nil
	}
	h.k.ServeResult(w, r, result)
}
