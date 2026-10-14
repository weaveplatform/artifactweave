package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/service/multipart"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Multipart upload routes (internal/service/multipart). create authenticates
// as any request; the others authenticate themselves (selfAuthenticated).
const (
	uploadsCreate   = "/artifactory/api/v1/uploads/create"
	uploadsURLPart  = "/artifactory/api/v1/uploads/urlPart"
	uploadsPart     = "/artifactory/api/v1/uploads/part"
	uploadsComplete = "/artifactory/api/v1/uploads/complete"
	uploadsStatus   = "/artifactory/api/v1/uploads/status"
	uploadsAbort    = "/artifactory/api/v1/uploads/abort"
)

func (s artifactoryService) registerMultipartRoutes(r chi.Router) {
	r.With(bodyLimit(managementBodyLimit)).Post(uploadsCreate, s.createMultipart)
	r.With(bodyLimit(managementBodyLimit)).Post(uploadsURLPart, s.multipartPartURL)
	// A part is up to 5 GiB: no management body limit.
	r.Put(uploadsPart, s.putMultipartPart)
	r.With(bodyLimit(managementBodyLimit)).Post(uploadsComplete, s.completeMultipart)
	r.With(bodyLimit(managementBodyLimit)).Post(uploadsStatus, s.multipartStatus)
	r.With(bodyLimit(managementBodyLimit)).Post(uploadsAbort, s.abortMultipart)
}

func (s artifactoryService) multipartService() (*multipart.Service, error) {
	// 501, as for an operation not built: config already answers supported
	// false, so the CLI never gets here without a blob store.
	if s.cfg.Multipart == nil {
		return nil, apierror.New(http.StatusNotImplemented, "Multipart upload is not available")
	}
	return s.cfg.Multipart, nil
}

// multipartError maps the service's errors onto statuses.
func (s artifactoryService) multipartError(r *http.Request, op string, err error) error {
	var api *apierror.Error
	switch {
	case errors.As(err, &api):
		return api
	case errors.Is(err, multipart.ErrBadToken):
		return auth.ErrBadCredentials
	case errors.Is(err, multipart.ErrInvalid):
		return badRequest("%s", err.Error())
	case errors.Is(err, multipart.ErrState):
		return apierror.New(http.StatusConflict, err.Error())
	}
	return s.internalError(r.Context(), op, err)
}

// bearer returns the upload token the CLI sends as a Bearer token, or "".
func bearer(r *http.Request) string {
	t, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return ""
	}
	return t
}

// createMultipart answers create: the caller needs deploy rights on the
// repository, decided as for a deploy.
func (s artifactoryService) createMultipart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	mp, err := s.multipartService()
	if err != nil {
		writeErr(w, r, err)
		return
	}
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
	q := r.URL.Query()
	partSizeMB, perr := strconv.ParseInt(q.Get("partSizeMB"), 10, 64)
	repoPath := strings.Trim(q.Get("repoPath"), "/")
	if perr != nil || q.Get("repoKey") == "" || repoPath == "" {
		writeErr(w, r, badRequest("repoKey, repoPath and partSizeMB are required"))
		return
	}
	if _, _, _, err := types.SplitItemPath(repoPath); err != nil {
		writeErr(w, r, badRequest("malformed path %q", repoPath))
		return
	}
	c, cached, err := resolveRepo(ctx, st, q.Get("repoKey"))
	if err == nil {
		c, err = deployTarget(ctx, st, c, cached)
	}
	if err != nil {
		writeErr(w, r, s.itemError(ctx, "resolve deploy target", err))
		return
	}
	if !acc.file(c, repoPath, types.ActionWrite) {
		writeErr(w, r, denied(p))
		return
	}
	token, err := mp.Create(ctx, c.Key, repoPath, partSizeMB, p.Name)
	if err != nil {
		writeErr(w, r, s.multipartError(r, "create multipart upload", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

func (s artifactoryService) multipartPartURL(w http.ResponseWriter, r *http.Request) {
	mp, err := s.multipartService()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	part, err := strconv.Atoi(r.URL.Query().Get("partNumber"))
	if err != nil {
		writeErr(w, r, badRequest("partNumber is required"))
		return
	}
	ref, err := mp.PartURL(r.Context(), bearer(r), part)
	if err != nil {
		writeErr(w, r, s.multipartError(r, "multipart part URL", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": baseurl.From(r.Context()) + ref})
}

func (s artifactoryService) putMultipartPart(w http.ResponseWriter, r *http.Request) {
	mp, err := s.multipartService()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	q := r.URL.Query()
	part, err := strconv.Atoi(q.Get("partNumber"))
	if err != nil {
		writeErr(w, r, auth.ErrBadCredentials)
		return
	}
	if err := mp.PutPart(
		r.Context(),
		q.Get("upload"),
		part,
		q.Get("signature"),
		r.Body,
	); err != nil {
		writeErr(w, r, s.multipartError(r, "multipart part", err))
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s artifactoryService) completeMultipart(w http.ResponseWriter, r *http.Request) {
	mp, err := s.multipartService()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := mp.Complete(r.Context(), bearer(r), r.URL.Query().Get("sha1")); err != nil {
		writeErr(w, r, s.multipartError(r, "complete multipart upload", err))
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s artifactoryService) multipartStatus(w http.ResponseWriter, r *http.Request) {
	mp, err := s.multipartService()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	st, err := mp.Status(r.Context(), bearer(r))
	if err != nil {
		writeErr(w, r, s.multipartError(r, "multipart upload status", err))
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s artifactoryService) abortMultipart(w http.ResponseWriter, r *http.Request) {
	mp, err := s.multipartService()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := mp.Abort(r.Context(), bearer(r)); err != nil {
		writeErr(w, r, s.multipartError(r, "abort multipart upload", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
