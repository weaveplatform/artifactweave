package luarockshttp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	luafmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/luarocks"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
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

// upstreamManifest reads the remote's manifest at path. A client's request
// for it (lookup false) is checked by the download policy at path as a
// file, where 7.x caches it, so a blackout names the manifest and patterns
// that reject it refuse it: RTFACT-30802 shows 7.x checking a remote's
// patterns at a metadata file's cache path. remoteRock's lookup of a rock
// in the manifest (lookup true) is part of the rock's download, which the
// rock's own path decides, so only the blackout refuses it (R4).
func (h handler) upstreamManifest(
	r *http.Request,
	path string,
	lookup bool,
) (source.Result, remoteManifest, error) {
	resource := source.Resource{
		Kind:             source.ResourceKindMetadata,
		Path:             path,
		Method:           http.MethodGet,
		PermissionFolder: true,
		Variant:          "luarocks-manifest-v1",
		ValidateMetadata: validateRemoteManifest,
	}
	if lookup {
		resource.BlackoutOnly = true
	} else {
		resource.PolicyPath = path
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return source.Result{}, remoteManifest{}, fmt.Errorf("luarocks manifest read: %w", err)
	}
	if result.Stream == nil {
		return source.Result{}, remoteManifest{}, remoteFailure()
	}
	defer result.Stream.Body.Close()
	data, err := pkgroute.ReadServed(
		r.Context(),
		result.Stream.Body,
		remoteManifestLimit,
		remoteFailure(),
	)
	if err != nil {
		return source.Result{}, remoteManifest{}, err
	}
	if len(data) > remoteManifestLimit {
		return source.Result{}, remoteManifest{}, remoteFailure()
	}
	manifest, err := parseRemoteManifest(data)
	if err != nil {
		return source.Result{}, remoteManifest{}, remoteFailure()
	}
	result.Stream = nil
	return result, manifest, nil
}

// fenceDisclosures has result recheck, when it is delivered, the authority
// of the manifest that disclosed paths and check on each of them: the
// download policy with Read permission for a rock the result serves, Read
// permission alone for the rocks a manifest lists, which stay listed when
// the patterns reject their files (R3).
func fenceDisclosures(
	result *source.Result,
	manifest func(context.Context) error,
	check func(context.Context, string) error,
	paths []string,
) {
	prior := result.Revalidate
	result.Revalidate = func(ctx context.Context) error {
		if prior != nil {
			if err := prior(ctx); err != nil {
				return err
			}
		}
		if manifest != nil {
			if err := manifest(ctx); err != nil {
				return fmt.Errorf("luarocks manifest authority: %w", err)
			}
		}
		if check != nil {
			for _, path := range paths {
				if err := check(ctx, path); err != nil {
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
	if !h.k.Require(w, r, path, api.ActionRead) {
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
		return source.Result{}, kernel.ErrNotFound
	}
	// The rock's own path is checked before the manifest lookup, so a
	// blackout names the rock, the file the download asks for, and a member
	// that refuses it is passed over before its upstream is asked anything.
	if err := h.k.CheckDocument(r, path); err != nil {
		return source.Result{}, fmt.Errorf("luarocks rock policy: %w", err)
	}
	metadata, manifest, err := h.upstreamManifest(r, luafmt.ManifestPath, true)
	if err != nil {
		return source.Result{}, err
	}
	if !manifest.has(path) {
		return source.Result{}, kernel.ErrNotFound
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
	fenceDisclosures(&result, metadata.Revalidate, metadata.CheckRead, []string{path})
	return result, nil
}

func (h handler) remoteManifest(
	ctx context.Context, w http.ResponseWriter, r *http.Request, path string,
) {
	metadata, manifest, err := h.upstreamManifest(r, path, false)
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	var checkErr error
	// A rock whose file the remote's patterns reject stays listed and only
	// its download is refused (R3), so the rows take Read permission
	// alone.
	data, paths, err := manifest.filtered(func(path string) bool {
		if !h.k.Can(r, path, api.ActionRead) {
			return false
		}
		if metadata.CheckPermission != nil {
			if e := metadata.CheckPermission(ctx, path); e != nil {
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
			Checksums: kernel.Checksums{
				SHA256: fmt.Sprintf("%x", digest),
			},
		},
	}
	fenceDisclosures(&metadata, nil, metadata.CheckPermission, paths)
	h.k.ServeResult(w, r, metadata)
}
