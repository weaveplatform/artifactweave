package luarocks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	luafmt "github.com/weaveplatform/artifactweave/internal/format/luarocks"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func remoteFailure() error {
	return apierror.New(http.StatusBadGateway, "invalid upstream LuaRocks response")
}

func manifestPath(p string) bool {
	return p == luafmt.ManifestPath || strings.HasPrefix(p, "manifest-5.") &&
		len(p) == len("manifest-5.0") && p[len(p)-1] >= '1' && p[len(p)-1] <= '5'
}

func validateRemoteManifest(
	ctx context.Context, body io.ReadSeeker, size int64, _ source.MetadataResponse,
) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("luarocks manifest validation: %w", err)
	}
	if size <= 0 || size > remoteManifestLimit {
		return remoteFailure()
	}
	data, err := io.ReadAll(io.LimitReader(body, remoteManifestLimit+1))
	if err != nil || int64(len(data)) != size {
		return remoteFailure()
	}
	if _, err = parseRemoteManifest(data); err != nil {
		return remoteFailure()
	}
	return nil
}

func (h handler) upstreamManifest(
	r *http.Request,
	path string,
) (source.Result, remoteManifest, error) {
	resource := source.Resource{
		Kind:             source.Metadata,
		Path:             path,
		Method:           http.MethodGet,
		PermissionFolder: true,
		Variant:          "luarocks-manifest-v1",
		ValidateMetadata: validateRemoteManifest,
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return source.Result{}, remoteManifest{}, fmt.Errorf("luarocks manifest read: %w", err)
	}
	if result.Stream == nil {
		return source.Result{}, remoteManifest{}, remoteFailure()
	}
	defer result.Stream.Body.Close()
	data, err := io.ReadAll(io.LimitReader(result.Stream.Body, remoteManifestLimit+1))
	if err != nil || len(data) > remoteManifestLimit {
		return source.Result{}, remoteManifest{}, remoteFailure()
	}
	manifest, err := parseRemoteManifest(data)
	if err != nil {
		return source.Result{}, remoteManifest{}, remoteFailure()
	}
	result.Stream = nil
	return result, manifest, nil
}

func fenceDisclosures(result *source.Result, metadata source.Result, paths []string) {
	prior := result.Revalidate
	result.Revalidate = func(ctx context.Context) error {
		if prior != nil {
			if err := prior(ctx); err != nil {
				return err
			}
		}
		if metadata.Revalidate != nil {
			if err := metadata.Revalidate(ctx); err != nil {
				return fmt.Errorf("luarocks manifest authority: %w", err)
			}
		}
		if metadata.CheckRead != nil {
			for _, path := range paths {
				if err := metadata.CheckRead(ctx, path); err != nil {
					return fmt.Errorf("luarocks package authority: %w", err)
				}
			}
		}
		return nil
	}
}

func (h handler) remoteRead(w http.ResponseWriter, r *http.Request, path string) {
	if r.URL.RawQuery != "" {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid LuaRocks query"))
		return
	}
	if manifestPath(path) {
		h.remoteManifest(r.Context(), w, r, path)
		return
	}
	if _, ok := luafmt.Parse(path); !ok {
		h.k.Error(w, r, apierror.New(http.StatusNotFound, "Could not find resource"))
		return
	}
	if !h.k.Require(w, r, path, types.ActionRead) {
		return
	}
	result, err := h.remoteRock(r, path)
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	h.k.ServeResult(w, r, result)
}

func (h handler) remoteRock(r *http.Request, path string) (source.Result, error) {
	id, ok := luafmt.Parse(path)
	if !ok {
		return source.Result{}, store.ErrNotFound
	}
	metadata, manifest, err := h.upstreamManifest(r, luafmt.ManifestPath)
	if err != nil {
		return source.Result{}, err
	}
	if !manifest.has(path) {
		return source.Result{}, store.ErrNotFound
	}
	if metadata.CheckRead != nil {
		if err = metadata.CheckRead(r.Context(), path); err != nil {
			return source.Result{}, fmt.Errorf("luarocks rock authority: %w", err)
		}
	}
	resource := pkgroute.Resource(r, path, "")
	resource.Method = http.MethodGet
	resource.Query, resource.Accept = "", ""
	resource.Variant = "luarocks-rock-v1"
	resource.Validate = func(ctx context.Context, body io.ReadSeeker, size int64) error {
		if err := luafmt.ValidateContent(ctx, body, size, id); err != nil {
			return source.ErrInvalidResult
		}
		return nil
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return source.Result{}, fmt.Errorf("luarocks rock read: %w", err)
	}
	fenceDisclosures(&result, metadata, []string{path})
	return result, nil
}

func (h handler) remoteManifest(
	ctx context.Context, w http.ResponseWriter, r *http.Request, path string,
) {
	metadata, manifest, err := h.upstreamManifest(r, path)
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	var checkErr error
	data, paths, err := manifest.filtered(func(path string) bool {
		if !h.k.Can(r, path, types.ActionRead) {
			return false
		}
		if metadata.CheckRead != nil {
			if e := metadata.CheckRead(ctx, path); e != nil {
				if !errors.Is(e, source.ErrDenied) {
					checkErr = e
				}
				return false
			}
		}
		return true
	})
	if err != nil || checkErr != nil {
		h.k.Error(w, r, remoteFailure())
		return
	}
	digest := sha256.Sum256(data)
	metadata.Stream = &source.Stream{
		Status: http.StatusOK,
		Body:   io.NopCloser(bytes.NewReader(data)),
		Representation: source.Representation{
			MediaType: "text/plain; charset=utf-8",
			Size:      int64(len(data)),
			ETag:      fmt.Sprintf(`"%x"`, digest),
			Checksums: types.Checksums{
				SHA256: fmt.Sprintf("%x", digest),
			},
		},
	}
	fenceDisclosures(&metadata, source.Result{CheckRead: metadata.CheckRead}, paths)
	h.k.ServeResult(w, r, metadata)
}
