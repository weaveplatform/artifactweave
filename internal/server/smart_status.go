package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func (s artifactoryService) smartStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := adminOnly(ctx); err != nil {
		apierror.Write(w, apierror.Artifactory, err)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	path := query.Get("path")
	key := chi.URLParam(r, "repoKey")
	_, _, _, pathErr := types.SplitItemPath(path)
	if err != nil || len(query) != 1 || len(query["path"]) != 1 || pathErr != nil ||
		path != strings.Trim(path, "/") ||
		strings.ContainsAny(path, "*?\r\n") ||
		strings.HasSuffix(key, types.CacheSuffix) {
		apierror.Write(
			w,
			apierror.Artifactory,
			badRequestf("one canonical repository path is required"),
		)
		return
	}
	if s.cfg.RemoteStatus == nil {
		apierror.Write(
			w,
			apierror.Artifactory,
			apierror.New(http.StatusNotImplemented, "remote status is not configured"),
		)
		return
	}
	status, err := s.cfg.RemoteStatus(ctx, key, path)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			err = apierror.New(http.StatusNotFound, "remote repository does not exist")
		case errors.Is(err, store.ErrInvalid), errors.Is(err, source.ErrInvalidRequest):
			err = badRequestf("one canonical remote repository path is required")
		default:
			err = s.internalError(ctx, "remote status", err)
		}
		apierror.Write(w, apierror.Artifactory, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(status)
}
