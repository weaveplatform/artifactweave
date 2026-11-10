package pubhttp

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	pubfmt "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/pub"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/baseurl"
)

const maxMultipartBytes = pubfmt.MaxArchiveBytes + 2<<20

func (h handler) publishStart(w http.ResponseWriter, r *http.Request) {
	if !h.publishAllowed(w, r) {
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{
		"url":    hostedURL(r) + "/api/packages/versions/new",
		"fields": map[string]string{},
	})
}

func (h handler) publishUpload(w http.ResponseWriter, r *http.Request) {
	if !h.publishAllowed(w, r) {
		return
	}
	if r.ContentLength > maxMultipartBytes {
		h.k.Error(w, r, apierror.New(http.StatusRequestEntityTooLarge, "pub archive exceeds limit"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxMultipartBytes)
	form, err := h.k.ReadForm(r, "file")
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if form.File.Size <= 0 || form.File.Size > pubfmt.MaxArchiveBytes {
		h.k.Error(w, r, apierror.New(http.StatusRequestEntityTooLarge, "pub archive exceeds limit"))
		return
	}
	rc, info, err := h.k.Blobs().Open(r.Context(), form.File.Digests.SHA256)
	if err != nil {
		h.k.Error(w, r, fmt.Errorf("pub: open uploaded archive: %w", err))
		return
	}
	meta, parseErr := pubfmt.ReadArchive(rc, info.Size)
	_ = rc.Close()
	if parseErr != nil {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Dart pub archive"))
		return
	}
	path := pubfmt.Path(meta.Name, meta.Version)
	if !h.k.Require(w, r, path, api.ActionWrite) {
		return
	}
	item, err := h.k.StoreNew(r, path, form.File, nil, kernel.Checksums{}, func() error {
		return apierror.New(http.StatusConflict, "Dart pub version already published")
	})
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	query := url.Values{
		"name": {meta.Name}, "version": {meta.Version}, "sha256": {item.Actual.SHA256},
	}
	w.Header().Set(
		"Location",
		hostedURL(r)+"/api/packages/versions/new/complete?"+query.Encode(),
	)
	w.WriteHeader(http.StatusNoContent)
}

func (h handler) publishComplete(w http.ResponseWriter, r *http.Request) {
	if !h.publishAllowed(w, r) {
		return
	}
	query := r.URL.Query()
	name, version, sha := query.Get("name"), query.Get("version"), query.Get("sha256")
	if len(query) != 3 || !pubfmt.ValidName(name) || !pubfmt.ValidVersion(version) ||
		len(sha) != 64 {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid pub finalize request"))
		return
	}
	path := pubfmt.Path(name, version)
	if !h.k.Require(w, r, path, api.ActionWrite) {
		return
	}
	item, err := h.k.DB().Items().Get(r.Context(), pkgroute.RequestFrom(r).Repo.Key, false, path)
	if errors.Is(err, kernel.ErrNotFound) {
		h.k.Error(w, r, apierror.New(http.StatusNotFound, "pub version not found"))
		return
	}
	if err != nil {
		h.k.Error(w, r, fmt.Errorf("pub: finalize lookup: %w", err))
		return
	}
	if item.Folder || item.Actual.SHA256 != sha {
		h.k.Error(w, r, apierror.New(http.StatusConflict, "pub upload changed before finalize"))
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{
		"success": map[string]string{
			"message": "Published " + name + " " + version + ".",
		},
	})
}

func (h handler) publishAllowed(w http.ResponseWriter, r *http.Request) bool {
	class := pkgroute.RequestFrom(r).Repo.Class
	if class != repo.Local && class != repo.Federated {
		h.k.Error(w, r, apierror.New(
			http.StatusMethodNotAllowed,
			"Dart pub publishing requires a local or federated repository",
		))
		return false
	}
	return h.k.RequireFolder(w, r, "", api.ActionWrite)
}

func hostedURL(r *http.Request) string {
	return baseurl.From(r.Context()) + "/artifactory/api/pub/" +
		url.PathEscape(pkgroute.RequestFrom(r).Repo.Key)
}
