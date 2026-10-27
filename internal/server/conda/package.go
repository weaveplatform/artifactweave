package conda

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"

	condafmt "github.com/weaveplatform/artifactweave/internal/format/conda"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func recordOf(index condafmt.Repodata, id condafmt.PackagePath) (condafmt.Record, bool) {
	if id.Extension == ".conda" {
		rec, ok := index.PackagesConda[id.Filename]
		return rec, ok
	}
	rec, ok := index.Packages[id.Filename]
	return rec, ok
}

func (h handler) readPackage(w http.ResponseWriter, r *http.Request, id condafmt.PackagePath) {
	if !h.k.Require(w, r, id.Path, types.ActionRead) {
		return
	}
	if err := h.servePackage(w, r, id, map[string]bool{}); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			err = apierror.New(http.StatusNotFound, "Conda package not found")
		}
		h.k.Error(w, r, pkgroute.SourceError(err, false))
	}
}

func (h handler) servePackage(w http.ResponseWriter, r *http.Request, id condafmt.PackagePath,
	seen map[string]bool,
) error {
	c := pkgroute.RequestFrom(r).Repo
	if seen[c.Key] || len(seen) >= 8 {
		return source.ErrInvalidRequest
	}
	seen[c.Key] = true
	defer delete(seen, c.Key)
	if err := generic.CheckDownload(c, id.Path, false); err != nil {
		return fmt.Errorf("conda: download policy: %w", err)
	}
	if c.Class == repo.Virtual {
		members, err := h.members(r)
		if err != nil {
			return err
		}
		for _, mr := range members {
			if !h.k.Can(mr, id.Path, types.ActionRead) {
				continue
			}
			indexPath := condafmt.IndexPath(id.Channel, id.Subdir, "repodata.json")
			index, fence, err := h.memberIndex(mr, indexPath, map[string]bool{})
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if _, ok := recordOf(index, id); !ok {
				continue
			}
			if fence != nil {
				if err := fence(r.Context()); err != nil {
					return fmt.Errorf("conda: virtual member authority: %w", err)
				}
			}
			return h.servePackage(w, mr, id, seen)
		}
		return store.ErrNotFound
	}
	if !h.k.Can(r, id.Path, types.ActionRead) ||
		!h.k.CanFolder(r, path.Dir(id.Path), types.ActionRead) {
		return source.ErrDenied
	}
	indexPath := condafmt.IndexPath(id.Channel, id.Subdir, "repodata.json")
	index, finish, err := h.memberIndex(r, indexPath, map[string]bool{})
	if err != nil {
		return err
	}
	rec, ok := recordOf(index, id)
	if !ok {
		return store.ErrNotFound
	}
	if finish != nil {
		if err := finish(r.Context()); err != nil {
			return fmt.Errorf("conda: package index authority: %w", err)
		}
	}
	if c.Class != repo.Remote {
		h.k.ServeItem(w, r, id.Path)
		return nil
	}
	resource := source.Resource{
		Kind: source.ResourceKindArtifact, Path: id.Path, Method: http.MethodGet,
		Variant:   "conda-package-v1",
		Checksums: types.Checksums{SHA256: rec.SHA256, MD5: rec.MD5},
		Validate: func(ctx context.Context, body io.ReadSeeker, size int64) error {
			if size != rec.Size {
				return source.ErrInvalidResult
			}
			if _, err := condafmt.ReadPackage(ctx, body, size, id); err != nil {
				return source.ErrInvalidResult
			}
			return nil
		},
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return fmt.Errorf("conda: upstream package: %w", err)
	}
	h.k.ServeResult(w, r, result)
	return nil
}
