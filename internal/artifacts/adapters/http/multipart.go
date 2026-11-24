package artifactshttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/multipart"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/middleware"
)

// Multipart upload routes (internal/artifacts/core/multipart). create authenticates
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

// deployForbidden is the text of 7.161.26's
// MultipartUploadServiceImpl.FORBIDDEN_DEPLOY_MESSAGE (65).
const deployForbidden = "The user is not allowed to deploy to this location"

// createRefused is generateToken's ForbiddenException(FORBIDDEN_DEPLOY_MESSAGE)
// (105) as 7.161.26's ForbiddenExceptionMapper answers it: 403 with the
// message in the error envelope, or, through
// ArtifactoryRestExceptionMapperBase.interceptForbiddenStatus, 401
// "Unauthorized" with the Basic challenge to an anonymous caller, who can
// only get here with anonymous access on, or MapperHidden while
// hideUnauthorizedResources is on.
func createRefused(p kernel.Principal) error {
	if p.Anonymous {
		return apierror.Hide(apierror.New(http.StatusUnauthorized, "Unauthorized"), httpkit.MapperHidden)
	}
	return apierror.New(http.StatusForbidden, deployForbidden)
}

// createMultipart answers create as 7.161.26's MultipartUploadResource.create
// and MultipartUploadServiceImpl.generateToken (80-106) do: a key that names
// no repository and a caller without Deploy get the same refusal
// (createRefused), and no repository is refused for its class, so a remote
// repository, its cache or a virtual repository without a deployment
// repository hands out a token to a caller with Deploy there, an
// administrator always (AuthorizationServiceBase.hasPermission 298). The
// checksum deploy that ends the upload then answers that deploy's own
// refusal (decision 0008, R6). The 400s for partSizeMB and the path are
// artifactweave's own, kept ahead of the repository's checks: generateToken
// makes neither check.
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
	// assertRepoKeyAndPathNotNull (MultipartUploadResource 92-96) refuses
	// only an absent parameter: an empty repoKey goes on to name no
	// repository, and is refused as one.
	if !q.Has("repoKey") || !q.Has("repoPath") {
		httpkit.WriteErr(w, r, httpkit.BadRequestf("Query param repoKey or repoPath is null"))
		return
	}
	partSizeMB, perr := strconv.ParseInt(q.Get("partSizeMB"), 10, 64)
	repoPath := strings.Trim(q.Get("repoPath"), "/")
	if perr != nil || repoPath == "" {
		httpkit.WriteErr(w, r, httpkit.BadRequestf("repoKey, repoPath and partSizeMB are required"))
		return
	}
	if _, _, _, err := kernel.SplitItemPath(repoPath); err != nil {
		httpkit.WriteErr(w, r, httpkit.BadRequestf("malformed path %q", repoPath))
		return
	}
	c, err := multipartTarget(ctx, st, q.Get("repoKey"))
	if isMissingRepo(err) {
		httpkit.WriteErr(w, r, createRefused(p))
		return
	}
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "resolve multipart target", err))
		return
	}
	if !acc.File(c, repoPath, api.ActionWrite) {
		httpkit.WriteErr(w, r, createRefused(p))
		return
	}
	token, err := mp.Create(ctx, c.Key, repoPath, partSizeMB, p.Name)
	if err != nil {
		httpkit.WriteErr(w, r, s.multipartError(r, "create multipart upload", err))
		return
	}
	httpkit.WriteJSON(w, http.StatusOK, map[string]string{"token": token})
}

// multipartTarget returns the repository on whose path create checks
// Deploy, as MultipartUploadServiceImpl.getRepoPath (108-119) picks it: the
// one key names, or the default deployment repository a virtual repository
// names, whatever its class and whether or not it exists. One that does not
// exist has no class here, so only a grant on any repository or on its key
// covers it, as 7.x matches a key of no known type
// (AuthorizationServiceImpl.createArtifactPermissionScope,
// LegacyAuthorizationServiceImpl.hasMatchingRepoKey). The error is
// ResolveRepo's for key, or the store's.
func multipartTarget(ctx context.Context, st itemkit.Store, key string) (repo.Config, error) {
	c, _, err := itemkit.ResolveRepo(ctx, st, key)
	if err != nil || c.Class != repo.Virtual || c.DefaultDeploymentRepo == "" {
		return c, err //nolint:wrapcheck // ResolveRepo's 404 is the caller's to map
	}
	d, err := st.Repos().Get(ctx, c.DefaultDeploymentRepo)
	if errors.Is(err, kernel.ErrNotFound) {
		return repo.Config{Key: c.DefaultDeploymentRepo}, nil
	}
	if err != nil {
		return c, fmt.Errorf("default deployment repository %s of %s: %w",
			c.DefaultDeploymentRepo, c.Key, err)
	}
	return d, nil
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
