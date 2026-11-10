package artifactshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
)

// SmartStatus serves GET
// /artifactory/api/artifactweave/remote-status/{repoKey}?path=, the
// artifactweave-only Smart Remote diagnostics of one canonical path of a
// remote repository, to administrators: 400 for any other query, 501
// without Deps.RemoteStatus, 404 for a repository that is no remote one.
// It is no spec operation, so the router mounts it as a plain route.
func (s ArtifactoryAPI) SmartStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := httpkit.AdminOnly(ctx); err != nil {
		apierror.Write(w, apierror.ProfileArtifactory, err)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	path := query.Get("path")
	key := chi.URLParam(r, "repoKey")
	_, _, _, pathErr := kernel.SplitItemPath(path)
	if err != nil || len(query) != 1 || len(query["path"]) != 1 || pathErr != nil ||
		path != strings.Trim(path, "/") ||
		strings.ContainsAny(path, "*?\r\n") ||
		strings.HasSuffix(key, model.CacheSuffix) {
		apierror.Write(
			w,
			apierror.ProfileArtifactory,
			httpkit.BadRequestf("one canonical repository path is required"),
		)
		return
	}
	if s.cfg.RemoteStatus == nil {
		apierror.Write(
			w,
			apierror.ProfileArtifactory,
			apierror.New(http.StatusNotImplemented, "remote status is not configured"),
		)
		return
	}
	status, err := s.cfg.RemoteStatus(ctx, key, path)
	if err != nil {
		switch {
		case errors.Is(err, kernel.ErrNotFound):
			err = apierror.New(http.StatusNotFound, "remote repository does not exist")
		case errors.Is(err, kernel.ErrInvalid), errors.Is(err, source.ErrInvalidRequest):
			err = httpkit.BadRequestf("one canonical remote repository path is required")
		default:
			err = s.InternalError(ctx, "remote status", err)
		}
		apierror.Write(w, apierror.ProfileArtifactory, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(status)
}
