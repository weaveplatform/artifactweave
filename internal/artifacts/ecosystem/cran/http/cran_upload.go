package cranhttp

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/cran"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// The native POST API identifies a CRAN package by its archive contents. A
// bounded spool lets the same complete archive validator used for direct PUT
// inspect the body before the item service commits it under its canonical path.
const cranUploadLimit = 512 << 20

// RegisterCranRoutes mounts CRAN's native source and binary package
// uploads on r: POST /artifactory/api/cran/{repoKey}/sources, and
// /binaries with its distribution and rVersion query. The URL gives only
// the feed: each deploys the package under the file name its archive's
// name and version give, after the same archive checks as a PUT.
func (s CranAPI) RegisterCranRoutes(r chi.Router) {
	r.Post("/artifactory/api/cran/{repoKey}/sources", s.uploadCranSource)
	r.Post("/artifactory/api/cran/{repoKey}/binaries", s.uploadCranBinary)
}

func (s CranAPI) uploadCranSource(w http.ResponseWriter, r *http.Request) {
	s.uploadCran(w, r, "src/contrib", ".tar.gz")
}

func (s CranAPI) uploadCranBinary(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	distribution := q.Get("distribution")
	version := q.Get("rVersion")
	if len(q["distribution"]) != 1 || len(q["rVersion"]) != 1 ||
		strings.Contains(version, "/") {
		httpkit.WriteErr(w, r, httpkit.BadRequestf("distribution and rVersion are required"))
		return
	}
	var feed, suffix string
	switch distribution {
	case "windows":
		feed, suffix = "bin/windows/contrib/"+version, ".zip"
	case "mac", "macosx":
		feed, suffix = "bin/macosx/contrib/"+version, ".tgz"
	case "linux":
		feed, suffix = "bin/linux/contrib/"+version, ".tar.gz"
	}
	if _, ok := cran.Feed(feed); !ok {
		httpkit.WriteErr(w, r, httpkit.BadRequestf("invalid CRAN distribution or R version"))
		return
	}
	s.uploadCran(w, r, feed, suffix)
}

func (s CranAPI) uploadCran(
	w http.ResponseWriter,
	r *http.Request,
	feed, suffix string,
) {
	ctx := r.Context()
	st, err := s.Content()
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	p, acc, err := s.ItemCaller(ctx, st)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	c, cached, err := itemkit.ResolveRepo(ctx, st, chi.URLParam(r, "repoKey"))
	if err == nil {
		c, err = itemkit.DeployTarget(ctx, st, c, cached)
	}
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "resolve CRAN upload repository", err))
		return
	}
	if c.PackageType != repo.Cran {
		httpkit.WriteErr(w, r, httpkit.BadRequestf("repository is not a CRAN repository"))
		return
	}
	if !acc.Folder(c, feed, api.ActionWrite) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	tmp, err := os.CreateTemp("", "artifactweave-cran-upload-*")
	if err != nil {
		httpkit.WriteErr(w, r, s.InternalError(ctx, "spool CRAN upload", err))
		return
	}
	defer func() { _ = os.Remove(tmp.Name()); _ = tmp.Close() }()
	size, err := io.Copy(tmp, io.LimitReader(r.Body, cranUploadLimit+1))
	if err != nil {
		httpkit.WriteErr(w, r, httpkit.BadRequestf("cannot read CRAN package upload"))
		return
	}
	if size > cranUploadLimit {
		httpkit.WriteErr(w, r, httpkit.BadRequestf("CRAN package exceeds upload limit"))
		return
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		httpkit.WriteErr(w, r, s.InternalError(ctx, "rewind CRAN upload", err))
		return
	}
	pkg, err := cran.ReadUploaded(ctx, tmp, size, feed, suffix)
	if err != nil {
		httpkit.WriteErr(w, r, httpkit.BadRequestf("invalid CRAN package archive"))
		return
	}
	if !acc.File(c, pkg.Path, api.ActionWrite) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		httpkit.WriteErr(w, r, s.InternalError(ctx, "rewind CRAN package", err))
		return
	}
	svc := s.Items(st)
	it, err := svc.Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: c, Path: pkg.Path}, Body: tmp,
		Declared: pkgroute.DeclaredChecksums(r.Header), By: p.Name,
		CanOverwritePath:   func(full string) bool { return acc.File(c, full, api.ActionDelete) },
		OverwriteIdentical: s.MayBeIdentical(false),
	})
	if errors.Is(err, cran.ErrPackage) {
		httpkit.WriteErr(w, r, httpkit.BadRequestf("invalid CRAN package archive"))
		return
	}
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "deploy CRAN package", err))
		return
	}
	w.Header().Set("Location", itemkit.DownloadURI(ctx, it.ReportedRepoKey(), it.FullPath()))
	httpkit.WriteJSON(w, http.StatusCreated, itemkit.ItemCreated(ctx, it))
}
