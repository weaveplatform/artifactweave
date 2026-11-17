package condahttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	condafmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/conda"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

func recordOf(index condafmt.Repodata, id condafmt.PackagePath) (condafmt.Record, bool) {
	if id.Extension == ".conda" {
		rec, ok := index.PackagesConda[id.Filename]
		return rec, ok
	}
	rec, ok := index.Packages[id.Filename]
	return rec, ok
}

// packageLookup is the policy of the index reads a download of id makes to
// find its record: the package is checked at its own path, so a pattern on
// the index does not refuse it (decision 0032: the path checked is
// Artifactory's), and only the blackout refuses the lookup, naming the
// package.
func packageLookup(id condafmt.PackagePath) readPolicy {
	return readPolicy{path: id.Path, download: true}
}

// readPackage serves the package id. A virtual repository is not asked
// for Read: no permission target can name one (decision 0006), so the
// member that serves the package decides, and a caller who may read it in
// no member gets Conda's not-found (decision 0008, R3).
func (h handler) readPackage(w http.ResponseWriter, r *http.Request, id condafmt.PackagePath) {
	if pkgroute.RequestFrom(r).Repo.Class != repo.Virtual && !h.k.Require(w, r, id.Path, api.ActionRead) {
		return
	}
	if err := h.servePackage(w, r, id); err != nil {
		if errors.Is(err, kernel.ErrNotFound) {
			err = apierror.New(http.StatusNotFound, "Conda package not found")
		}
		h.k.Error(w, r, pkgroute.SourceError(err, false))
	}
}

func (h handler) servePackage(w http.ResponseWriter, r *http.Request, id condafmt.PackagePath) error {
	c := pkgroute.RequestFrom(r).Repo
	if err := generic.CheckDownload(c, id.Path, false); err != nil {
		return fmt.Errorf("conda: download policy: %w", err)
	}
	if c.Class == repo.Virtual {
		return h.virtualPackage(w, r, id)
	}
	if !h.k.Can(r, id.Path, api.ActionRead) ||
		!h.k.CanFolder(r, path.Dir(id.Path), api.ActionRead) {
		return source.ErrDenied
	}
	indexPath := condafmt.IndexPath(id.Channel, id.Subdir, "repodata.json")
	index, err := h.memberIndex(r, indexPath, packageLookup(id))
	if err != nil {
		return err
	}
	rec, ok := recordOf(index.records, id)
	if !ok {
		return kernel.ErrNotFound
	}
	if index.finish != nil {
		if err := index.finish(r.Context()); err != nil {
			return fmt.Errorf("conda: package index authority: %w", err)
		}
	}
	return h.serveRecord(w, r, id, rec)
}

// serveRecord serves the package id that rec, the record of r's own
// index, describes: a local or federated repository's stored file, or a
// remote's upstream file checked against rec.
func (h handler) serveRecord(w http.ResponseWriter, r *http.Request, id condafmt.PackagePath,
	rec condafmt.Record,
) error {
	if pkgroute.RequestFrom(r).Repo.Class != repo.Remote {
		h.k.ServeItem(w, r, id.Path)
		return nil
	}
	resource := source.Resource{
		Kind: source.ResourceKindArtifact, Path: id.Path, Method: http.MethodGet,
		Variant:   "conda-package-v1",
		Checksums: kernel.Checksums{SHA256: rec.SHA256, MD5: rec.MD5},
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
