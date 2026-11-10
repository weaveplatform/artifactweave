package artifactshttp

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/multipart"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/baseurl"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/middleware"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Multipart upload routes (migration/artifacts/core/multipart). create authenticates
// as any request; the others authenticate themselves (SelfAuthenticated).
const (
	uploadsCreate   = "/artifactory/api/v1/uploads/create"
	uploadsURLPart  = "/artifactory/api/v1/uploads/urlPart"
	uploadsPart     = "/artifactory/api/v1/uploads/part"
	uploadsComplete = "/artifactory/api/v1/uploads/complete"
	uploadsStatus   = "/artifactory/api/v1/uploads/status"
	uploadsAbort    = "/artifactory/api/v1/uploads/abort"
)

// SelfAuthenticated returns "METHOD path" of the routes that verify their
// own credential, which the authentication middleware passes untouched
// (accesshttp.AuthnOptions.SelfAuthenticated): the multipart upload calls
// carry the upload token as a Bearer token, which only the multipart service
// can verify until access tokens arrive (P4), and a part PUT carries a
// signed URL and no credentials at all, as the CLI sends it.
func SelfAuthenticated() map[string]bool {
	return map[string]bool{
		"POST " + uploadsURLPart:  true,
		"PUT " + uploadsPart:      true,
		"POST " + uploadsComplete: true,
		"POST " + uploadsStatus:   true,
		"POST " + uploadsAbort:    true,
	}
}

// RegisterMultipartRoutes serves the JFrog CLI's multipart upload protocol
// on r. Its routes are the spec's, so, registered before the router's stubs
// of the hand-written operations, they replace the 501 stubs; and the
// authentication middleware must pass the calls SelfAuthenticated names to
// them untouched. Every call but a part PUT, which carries up to 5 GiB, is
// under the management body limit.
func (s ArtifactoryAPI) RegisterMultipartRoutes(r chi.Router) {
	r.With(middleware.BodyLimit(middleware.ManagementBodyLimit)).
		Post(uploadsCreate, s.createMultipart)
	r.With(middleware.BodyLimit(middleware.ManagementBodyLimit)).
		Post(uploadsURLPart, s.multipartPartURL)
	// A part is up to 5 GiB: no management body limit.
	r.Put(uploadsPart, s.putMultipartPart)
	r.With(middleware.BodyLimit(middleware.ManagementBodyLimit)).
		Post(uploadsComplete, s.completeMultipart)
	r.With(middleware.BodyLimit(middleware.ManagementBodyLimit)).
		Post(uploadsStatus, s.multipartStatus)
	r.With(middleware.BodyLimit(middleware.ManagementBodyLimit)).
		Post(uploadsAbort, s.abortMultipart)
}

func (s ArtifactoryAPI) multipartService() (*multipart.Service, error) {
	// 501, as for an operation not built: config already answers supported
	// false, so the CLI never gets here without a blob store.
	if s.cfg.Multipart == nil {
		return nil, apierror.New(http.StatusNotImplemented, "Multipart upload is not available")
	}
	return s.cfg.Multipart, nil
}

// multipartError maps the service's errors onto statuses.
func (s ArtifactoryAPI) multipartError(r *http.Request, op string, err error) error {
	var api *apierror.Error
	switch {
	case errors.As(err, &api):
		return api
	case errors.Is(err, multipart.ErrBadToken):
		return kernel.ErrBadCredentials
	case errors.Is(err, multipart.ErrInvalid):
		return httpkit.BadRequestf("%s", err.Error())
	case errors.Is(err, multipart.ErrState):
		return apierror.New(http.StatusConflict, err.Error())
	}
	return s.InternalError(r.Context(), op, err)
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
func (s ArtifactoryAPI) createMultipart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	mp, err := s.multipartService()
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	st, err := s.content()
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	p, acc, err := s.ItemCaller(ctx, st)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	q := r.URL.Query()
	partSizeMB, perr := strconv.ParseInt(q.Get("partSizeMB"), 10, 64)
	repoPath := strings.Trim(q.Get("repoPath"), "/")
	if perr != nil || q.Get("repoKey") == "" || repoPath == "" {
		httpkit.WriteErr(w, r, httpkit.BadRequestf("repoKey, repoPath and partSizeMB are required"))
		return
	}
	if _, _, _, err := types.SplitItemPath(repoPath); err != nil {
		httpkit.WriteErr(w, r, httpkit.BadRequestf("malformed path %q", repoPath))
		return
	}
	c, cached, err := itemkit.ResolveRepo(ctx, st, q.Get("repoKey"))
	if err == nil {
		c, err = itemkit.DeployTarget(ctx, st, c, cached)
	}
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "resolve deploy target", err))
		return
	}
	if !acc.File(c, repoPath, types.ActionWrite) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	token, err := mp.Create(ctx, c.Key, repoPath, partSizeMB, p.Name)
	if err != nil {
		httpkit.WriteErr(w, r, s.multipartError(r, "create multipart upload", err))
		return
	}
	httpkit.WriteJSON(w, http.StatusOK, map[string]string{"token": token})
}

func (s ArtifactoryAPI) multipartPartURL(w http.ResponseWriter, r *http.Request) {
	mp, err := s.multipartService()
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	part, err := strconv.Atoi(r.URL.Query().Get("partNumber"))
	if err != nil {
		httpkit.WriteErr(w, r, httpkit.BadRequestf("partNumber is required"))
		return
	}
	ref, err := mp.PartURL(r.Context(), bearer(r), part)
	if err != nil {
		httpkit.WriteErr(w, r, s.multipartError(r, "multipart part URL", err))
		return
	}
	httpkit.WriteJSON(w, http.StatusOK, map[string]string{"url": baseurl.From(r.Context()) + ref})
}

func (s ArtifactoryAPI) putMultipartPart(w http.ResponseWriter, r *http.Request) {
	mp, err := s.multipartService()
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	q := r.URL.Query()
	part, err := strconv.Atoi(q.Get("partNumber"))
	if err != nil {
		httpkit.WriteErr(w, r, kernel.ErrBadCredentials)
		return
	}
	if err := mp.PutPart(
		r.Context(),
		q.Get("upload"),
		part,
		q.Get("signature"),
		r.Body,
	); err != nil {
		httpkit.WriteErr(w, r, s.multipartError(r, "multipart part", err))
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s ArtifactoryAPI) completeMultipart(w http.ResponseWriter, r *http.Request) {
	mp, err := s.multipartService()
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	if err := mp.Complete(r.Context(), bearer(r), r.URL.Query().Get("sha1")); err != nil {
		httpkit.WriteErr(w, r, s.multipartError(r, "complete multipart upload", err))
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s ArtifactoryAPI) multipartStatus(w http.ResponseWriter, r *http.Request) {
	mp, err := s.multipartService()
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	st, err := mp.Status(r.Context(), bearer(r))
	if err != nil {
		httpkit.WriteErr(w, r, s.multipartError(r, "multipart upload status", err))
		return
	}
	httpkit.WriteJSON(w, http.StatusOK, st)
}

func (s ArtifactoryAPI) abortMultipart(w http.ResponseWriter, r *http.Request) {
	mp, err := s.multipartService()
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	if err := mp.Abort(r.Context(), bearer(r)); err != nil {
		httpkit.WriteErr(w, r, s.multipartError(r, "abort multipart upload", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
