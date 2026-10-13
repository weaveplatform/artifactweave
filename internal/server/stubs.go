package server

import (
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
)

// surfaceFor picks the error envelope from the request path. Clients parse
// errors (jfrog-client-go reads errors[].message, Docker reads errors[].code,
// npm prints error), so the shape must match the surface even for a 404 that
// no handler claimed.
func surfaceFor(path string) apierror.Surface {
	switch {
	case strings.HasPrefix(path, "/xray/"):
		return apierror.Xray
	case strings.HasPrefix(path, "/artifactory/api/npm/"):
		return apierror.NPM
	case strings.HasPrefix(path, "/artifactory/api/terraform/"):
		return apierror.TerraformRegistry
	case strings.HasPrefix(path, "/artifactory/api/go/"),
		strings.HasPrefix(path, "/artifactory/api/pypi/"):
		return apierror.Text
	case strings.HasPrefix(path, "/v2/"), strings.Contains(path, "/api/docker/"):
		return apierror.OCI
	case strings.HasPrefix(path, "/access/"), strings.HasPrefix(path, "/event/"),
		strings.HasPrefix(path, "/worker/"):
		return apierror.Access
	case strings.HasPrefix(path, "/artifactory/api/retention/"),
		strings.HasPrefix(path, "/artifactory/api/import/"),
		strings.HasPrefix(path, "/artifactory/api/export/"),
		strings.HasPrefix(path, "/artifactory/api/artifactexport/"):
		return apierror.ArtifactoryLegacy
	case strings.HasPrefix(path, "/distribution/"):
		return apierror.Distribution
	case strings.HasPrefix(path, "/pfed/"), strings.HasPrefix(path, "/grid/"):
		return apierror.Flat
	default:
		return apierror.Artifactory
	}
}

// notSupported answers for JFrog products artifactweave deliberately does not
// implement (Xray, Lifecycle, AppTrust, Evidence, Workers, Federation). 501 with
// a product name tells a Terraform or jf user the feature is absent, not broken
// (research 10).
func notSupported(product string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		apierror.Write(w, surfaceFor(r.URL.Path), apierror.NotSupported(product))
	}
}

// notFound is the catch-all. Under /v2 the OCI code is NAME_UNKNOWN because
// that is what registry clients expect for an unknown repository.
func notFound(w http.ResponseWriter, r *http.Request) {
	s := surfaceFor(r.URL.Path)
	e := apierror.New(http.StatusNotFound, "not found: "+r.Method+" "+r.URL.Path)
	if s == apierror.OCI {
		e.Code = "NAME_UNKNOWN"
	}
	apierror.Write(w, s, e)
}

// methodNotAllowed keeps the envelope rule for 405 too.
func methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	apierror.Write(
		w,
		surfaceFor(r.URL.Path),
		apierror.New(http.StatusMethodNotAllowed, "method not allowed: "+r.Method+" "+r.URL.Path),
	)
}
