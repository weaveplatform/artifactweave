package server

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/format/cran"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The native POST API identifies a CRAN package by its archive contents. A
// bounded spool lets the same complete archive validator used for direct PUT
// inspect the body before the item service commits it under its canonical path.
const cranUploadLimit = 512 << 20

func (s artifactoryService) uploadCranSource(w http.ResponseWriter, r *http.Request) {
	s.uploadCran(w, r, "src/contrib", ".tar.gz")
}

func (s artifactoryService) uploadCranBinary(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	distribution := q.Get("distribution")
	version := q.Get("rVersion")
	if len(q["distribution"]) != 1 || len(q["rVersion"]) != 1 ||
		strings.Contains(version, "/") {
		writeErr(w, r, badRequestf("distribution and rVersion are required"))
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
		writeErr(w, r, badRequestf("invalid CRAN distribution or R version"))
		return
	}
	s.uploadCran(w, r, feed, suffix)
}

func (s artifactoryService) uploadCran(
	w http.ResponseWriter,
	r *http.Request,
	feed, suffix string,
) {
	ctx := r.Context()
	st, err := s.content()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	p, acc, err := s.itemCaller(ctx, st)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	c, cached, err := resolveRepo(ctx, st, chi.URLParam(r, "repoKey"))
	if err == nil {
		c, err = deployTarget(ctx, st, c, cached)
	}
	if err != nil {
		writeErr(w, r, s.itemError(ctx, "resolve CRAN upload repository", err))
		return
	}
	if c.PackageType != repo.Cran {
		writeErr(w, r, badRequestf("repository is not a CRAN repository"))
		return
	}
	if !acc.folder(c, feed, types.ActionWrite) {
		writeErr(w, r, denied(p))
		return
	}
	tmp, err := os.CreateTemp("", "artifactweave-cran-upload-*")
	if err != nil {
		writeErr(w, r, s.internalError(ctx, "spool CRAN upload", err))
		return
	}
	defer func() { _ = os.Remove(tmp.Name()); _ = tmp.Close() }()
	size, err := io.Copy(tmp, io.LimitReader(r.Body, cranUploadLimit+1))
	if err != nil {
		writeErr(w, r, badRequestf("cannot read CRAN package upload"))
		return
	}
	if size > cranUploadLimit {
		writeErr(w, r, badRequestf("CRAN package exceeds upload limit"))
		return
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		writeErr(w, r, s.internalError(ctx, "rewind CRAN upload", err))
		return
	}
	pkg, err := cran.ReadUploaded(ctx, tmp, size, feed, suffix)
	if err != nil {
		writeErr(w, r, badRequestf("invalid CRAN package archive"))
		return
	}
	if !acc.file(c, pkg.Path, types.ActionWrite) {
		writeErr(w, r, denied(p))
		return
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		writeErr(w, r, s.internalError(ctx, "rewind CRAN package", err))
		return
	}
	svc := s.items(st)
	it, err := svc.Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: c, Path: pkg.Path}, Body: tmp,
		Declared: pkgroute.DeclaredChecksums(r.Header), By: p.Name,
		CanOverwritePath:   func(full string) bool { return acc.file(c, full, types.ActionDelete) },
		OverwriteIdentical: s.mayBeIdentical(false),
	})
	if errors.Is(err, cran.ErrPackage) {
		writeErr(w, r, badRequestf("invalid CRAN package archive"))
		return
	}
	if err != nil {
		writeErr(w, r, s.itemError(ctx, "deploy CRAN package", err))
		return
	}
	w.Header().Set("Location", downloadURI(ctx, it.ReportedRepoKey(), it.FullPath()))
	writeJSON(w, http.StatusCreated, itemCreated(ctx, it))
}
