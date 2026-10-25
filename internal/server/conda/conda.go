// Package conda serves the native Conda channel protocol. Index responses are
// built from validated package records and filtered by the caller's rights.
package conda

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	condafmt "github.com/weaveplatform/artifactweave/internal/format/conda"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type virtualSource struct{}

func (virtualSource) Item(
	context.Context,
	store.Store,
	repo.Config,
	string,
) (pkgroute.Found, error) {
	return pkgroute.Found{}, source.ErrUnsupported
}

func Protocol(remote *source.Service) pkgroute.Protocol {
	sources := map[repo.Class]pkgroute.Source{
		repo.Local: pkgroute.Local{}, repo.Federated: pkgroute.Local{},
		repo.Virtual: virtualSource{},
	}
	if remote != nil {
		sources[repo.Remote] = pkgroute.Remote{Service: remote}
	}
	return pkgroute.Protocol{
		Name: "conda", Types: []repo.PackageType{repo.Conda},
		Surface: apierror.Artifactory, Sources: sources,
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k: k}
			r.Get("/*", h.read)
			r.Head("/*", h.read)
		},
	}
}

type handler struct{ k *pkgroute.Kit }

func (h handler) read(w http.ResponseWriter, r *http.Request) {
	p, err := pkgroute.ParsePath(pkgroute.RequestFrom(r).Rest)
	if err != nil || r.URL.RawQuery != "" {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Conda channel path"))
		return
	}
	requested := p.String()
	if id, ok := condafmt.ParseMetadataPath(requested); ok {
		h.readMetadata(w, r, id)
		return
	}
	if id, ok := condafmt.ParsePackagePath(requested); ok {
		h.readPackage(w, r, id)
		return
	}
	h.k.Error(w, r, store.ErrNotFound)
}

func (h handler) readMetadata(w http.ResponseWriter, r *http.Request, id condafmt.MetadataPath) {
	if !h.k.RequireFolder(w, r, path.Dir(id.Path), types.ActionRead) ||
		!h.k.Require(w, r, id.Path, types.ActionRead) {
		return
	}
	if id.Filename == "notices.json" {
		h.serveBytes(w, r, []byte(`{"notices":[]}`), "application/json", nil)
		return
	}
	if id.Filename == "repodata.json.bz2" {
		h.k.Error(w, r, store.ErrNotFound)
		return
	}
	canonical := condafmt.IndexPath(id.Channel, id.Subdir, "repodata.json")
	index, finish, err := h.memberIndex(r, canonical, map[string]bool{})
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	encoded, err := condafmt.EncodeRepodata(index, id.Channel, id.Subdir)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	body := encoded[id.Filename]
	if body == nil {
		h.k.Error(w, r, store.ErrNotFound)
		return
	}
	media := "application/json"
	if strings.HasSuffix(id.Filename, ".zst") {
		media = "application/zstd"
	}
	h.serveBytes(w, r, body, media, finish)
}

func (h handler) serveBytes(w http.ResponseWriter, r *http.Request, body []byte, media string,
	finish func(context.Context) error,
) {
	if finish != nil {
		if err := finish(r.Context()); err != nil {
			h.k.Error(w, r, pkgroute.SourceError(err, false))
			return
		}
	}
	w.Header().Set("Content-Type", media)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	http.ServeContent(w, r, path.Base(r.URL.Path), time.Time{}, bytes.NewReader(body))
}

// memberIndex returns only package records the caller may read. A virtual
// aggregates concrete members in configured order; it never inherits one
// member's authority or source network permit from another.
func (h handler) memberIndex(r *http.Request, indexPath string, seen map[string]bool) (
	condafmt.Repodata, func(context.Context) error, error,
) {
	c := pkgroute.RequestFrom(r).Repo
	id, ok := condafmt.ParseMetadataPath(indexPath)
	if !ok {
		return condafmt.Repodata{}, nil, store.ErrNotFound
	}
	if seen[c.Key] || len(seen) >= 8 {
		return condafmt.Repodata{}, nil, apierror.New(
			http.StatusBadRequest,
			"Conda virtual member cycle",
		)
	}
	seen[c.Key] = true
	defer delete(seen, c.Key)
	if c.Class == repo.Virtual {
		return h.virtualIndex(r, id, seen)
	}
	if !h.k.CanFolder(r, path.Dir(indexPath), types.ActionRead) ||
		!h.k.Can(r, indexPath, types.ActionRead) ||
		generic.CheckDownload(c, indexPath, false) != nil {
		return condafmt.Repodata{}, nil, apierror.New(
			http.StatusForbidden,
			"Conda index is not readable",
		)
	}
	if c.Class == repo.Remote {
		return h.remoteIndex(r, id)
	}
	return h.localIndex(r, id)
}

func (h handler) localIndex(r *http.Request, id condafmt.MetadataPath) (
	condafmt.Repodata, func(context.Context) error, error,
) {
	f, err := h.k.ResolveItem(r, id.Path)
	if errors.Is(err, store.ErrNotFound) {
		return condafmt.EmptyRepodata(id.Subdir), nil, nil
	}
	if err != nil {
		return condafmt.Repodata{}, nil, fmt.Errorf("conda: local index: %w", err)
	}
	if f.Item.Folder {
		return condafmt.Repodata{}, nil, store.ErrNotFound
	}
	rc, _, err := h.k.Blobs().Open(r.Context(), f.Item.Actual.SHA256)
	if err != nil {
		return condafmt.Repodata{}, nil, fmt.Errorf("conda: index blob: %w", err)
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(io.LimitReader(rc, 64<<20+1))
	if err != nil {
		return condafmt.Repodata{}, nil, fmt.Errorf("conda: index body: %w", err)
	}
	index, err := condafmt.DecodeRepodata(body, id)
	if err != nil {
		return condafmt.Repodata{}, nil, fmt.Errorf("conda: local repodata: %w", err)
	}
	filtered, err := h.filterIndex(r.Context(), r, index, id, nil)
	return filtered, nil, err
}

func (h handler) remoteIndex(r *http.Request, id condafmt.MetadataPath) (
	condafmt.Repodata, func(context.Context) error, error,
) {
	resource := source.Resource{
		Kind: source.Metadata, Path: id.Path, Method: http.MethodGet,
		PermissionFolder: true, PermissionPath: path.Dir(id.Path),
		Variant: "conda-repodata-v1", ExpectedContentTypes: []string{
			"application/json", "text/plain", "application/octet-stream",
		},
		ValidateMetadata: func(_ context.Context, body io.ReadSeeker, size int64,
			_ source.MetadataResponse,
		) error {
			if size <= 0 || size > 64<<20 {
				return source.ErrInvalidResult
			}
			data, err := io.ReadAll(io.LimitReader(body, 64<<20+1))
			if err != nil || int64(len(data)) != size {
				return source.ErrInvalidResult
			}
			if _, err := condafmt.DecodeRepodata(data, id); err != nil {
				return source.ErrInvalidResult
			}
			return nil
		},
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return condafmt.Repodata{}, nil, fmt.Errorf("conda: upstream repodata: %w", err)
	}
	body, err := h.resultBytes(r.Context(), result)
	if err != nil {
		return condafmt.Repodata{}, nil, err
	}
	index, err := condafmt.DecodeRepodata(body, id)
	if err != nil {
		return condafmt.Repodata{}, nil, fmt.Errorf("conda: upstream repodata content: %w", err)
	}
	filtered, err := h.filterIndex(r.Context(), r, index, id, result.CheckRead)
	if err != nil {
		return condafmt.Repodata{}, nil, err
	}
	return filtered, result.Revalidate, nil
}

func (h handler) resultBytes(ctx context.Context, result source.Result) ([]byte, error) {
	if result.Stream != nil {
		defer func() { _ = result.Stream.Body.Close() }()
		body, err := io.ReadAll(io.LimitReader(result.Stream.Body, 64<<20+1))
		if err != nil {
			return nil, fmt.Errorf("conda: read upstream repodata stream: %w", err)
		}
		return body, nil
	}
	if result.Item == nil {
		return nil, source.ErrInvalidResult
	}
	rc, _, err := h.k.Blobs().Open(ctx, result.Item.Actual.SHA256)
	if err != nil {
		return nil, fmt.Errorf("conda: open cached repodata: %w", err)
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(io.LimitReader(rc, 64<<20+1))
	if err != nil {
		return nil, fmt.Errorf("conda: read cached repodata: %w", err)
	}
	return body, nil
}

func (h handler) filterIndex(ctx context.Context, r *http.Request,
	index condafmt.Repodata, id condafmt.MetadataPath,
	check func(context.Context, string) error,
) (condafmt.Repodata, error) {
	filtered := condafmt.EmptyRepodata(id.Subdir)
	c := pkgroute.RequestFrom(r).Repo
	keep := func(filename string) (bool, error) {
		p := condafmt.IndexPath(id.Channel, id.Subdir, filename)
		if !h.k.Can(r, p, types.ActionRead) || generic.CheckDownload(c, p, false) != nil {
			return false, nil
		}
		if check != nil {
			if err := check(ctx, p); err != nil {
				if errors.Is(err, source.ErrDenied) {
					return false, nil
				}
				return false, fmt.Errorf("conda: source package authority: %w", err)
			}
		}
		return true, nil
	}
	for filename, rec := range index.Packages {
		allowed, err := keep(filename)
		if err != nil {
			return condafmt.Repodata{}, err
		}
		if allowed {
			filtered.Packages[filename] = rec
		}
	}
	for filename, rec := range index.PackagesConda {
		allowed, err := keep(filename)
		if err != nil {
			return condafmt.Repodata{}, err
		}
		if allowed {
			filtered.PackagesConda[filename] = rec
		}
	}
	return filtered, nil
}
