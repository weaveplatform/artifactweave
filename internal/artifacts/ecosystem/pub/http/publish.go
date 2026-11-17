package pubhttp

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	pubfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/pub"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
)

const maxMultipartBytes = pubfmt.MaxArchiveBytes + 2<<20

func (h handler) publishStart(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.publishTarget(w, r); !ok {
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{
		"url":    hostedURL(r) + "/api/packages/versions/new",
		"fields": map[string]string{},
	})
}

func (h handler) publishUpload(w http.ResponseWriter, r *http.Request) {
	target, ok := h.publishTarget(w, r)
	if !ok {
		return
	}
	if r.ContentLength > maxMultipartBytes {
		h.k.Error(w, r, apierror.New(http.StatusRequestEntityTooLarge, "pub archive exceeds limit"))
		return
	}
	// Through a virtual repository target is a copy of r, and the form is
	// read from its body.
	target.Body = http.MaxBytesReader(w, r.Body, maxMultipartBytes)
	form, err := h.k.ReadForm(target, "file")
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if form.File.Size > pubfmt.MaxArchiveBytes {
		h.k.Error(w, r, apierror.New(http.StatusRequestEntityTooLarge, "pub archive exceeds limit"))
		return
	}
	if form.File.Size == 0 {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Dart pub archive"))
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
	if !h.k.Require(w, target, path, api.ActionWrite) {
		return
	}
	item, err := h.k.StoreNew(target, path, form.File, nil, kernel.Checksums{}, func() error {
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
	target, ok := h.publishTarget(w, r)
	if !ok {
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
	if !h.k.Require(w, target, path, api.ActionWrite) {
		return
	}
	item, err := h.k.DB().Items().Get(
		r.Context(), pkgroute.RequestFrom(target).Repo.Key, false, path,
	)
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

// publishTarget returns the request a publish step to r's repository
// writes with, once the caller holds Write on that repository's root: r
// itself for a local or federated repository, or, for a virtual repository,
// a member request for its default deployment repository, a local or
// federated member (virtual.DeployTarget), whose Write alone the publish
// needs. 7.x publishes through a virtual repository to that repository
// (RTFACT-31048 logs a dart pub publish to the virtual "pub" deployed to
// pub-local; decision 0008, R6), and without one answers 405 with Allow:
// GET before it checks any permission (UploadServiceUtils.
// getTargetRepository 248-261, 7.161.26). The URLs a step hands the client
// stay the virtual repository's (hostedURL of r), so the client's next step
// comes through it too. A remote repository takes no publish.
func (h handler) publishTarget(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	target := r
	switch v := pkgroute.RequestFrom(r).Repo; v.Class {
	case repo.Local, repo.Federated:
	case repo.Virtual:
		c, err := virtual.DeployTarget(r.Context(), h.k.DB().Repos(), v)
		switch {
		case errors.Is(err, virtual.ErrNoDeployTarget):
			err = itemkit.NoDeployTarget(v.Key)
		case err == nil:
			target, err = h.k.MemberRequest(r, c)
		default:
			err = fmt.Errorf("pub: %w", err)
		}
		if err != nil {
			h.k.Error(w, r, err)
			return nil, false
		}
	default:
		h.k.Error(w, r, apierror.New(
			http.StatusMethodNotAllowed,
			"Dart pub publishing requires a local or federated repository",
		))
		return nil, false
	}
	return target, h.k.RequireFolder(w, target, "", api.ActionWrite)
}

func hostedURL(r *http.Request) string {
	return baseurl.From(r.Context()) + "/artifactory/api/pub/" +
		url.PathEscape(pkgroute.RequestFrom(r).Repo.Key)
}
