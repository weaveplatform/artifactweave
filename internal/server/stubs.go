package server

import (
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/docker"
)

// surfaceFor picks the error envelope from the request path. Clients read
// errors by surface (the Terraform providers join errors[] entries, Docker
// reads errors[].code, npm prints error, jfrog-client-go prints the body), so
// the shape must match the surface even for a 404 that no handler claimed.
func surfaceFor(path string) apierror.Surface {
	switch {
	case strings.HasPrefix(path, "/xray/"):
		return apierror.Xray
	case npmOperation(path), isNpmReindex(path), isTerraformReindex(path):
		return apierror.Artifactory
	case strings.HasPrefix(path, "/artifactory/api/npm/"):
		return apierror.NPM
	case path == "/artifactory/api/terraform/remote/v2" || strings.HasPrefix(path, "/artifactory/api/terraform/remote/v2/"):
		return apierror.TerraformBackend
	case strings.HasPrefix(path, "/artifactory/api/terraform/"):
		return apierror.TerraformRegistry
	case strings.HasPrefix(path, "/artifactory/api/go/"):
		return apierror.Text
	case strings.HasPrefix(path, "/artifactory/api/pypi/"),
		strings.HasPrefix(path, "/artifactory/api/nuget/"),
		strings.HasPrefix(path, "/artifactory/api/helm/"):
		// Named, though the default gives the same, because research 10
		// once gave PyPI and Helm plain text. 7.x answers Artifactory's
		// envelope under all three (decisions 0037, 0038 and 0039), and
		// pip, twine, NuGet.Protocol and helm read only the status.
		return apierror.Artifactory
	case docker.IsRegistryPath(path):
		// The JFrog operations beside the registry (promote,
		// parentManifests) keep the Artifactory envelope (decision 0031).
		return apierror.OCI
	case strings.HasPrefix(path, "/access/"), strings.HasPrefix(path, "/event/"),
		strings.HasPrefix(path, "/worker/"):
		return apierror.Access
	case strings.HasPrefix(path, "/artifactory/api/retention/"):
		return apierror.ArtifactoryLegacy
	case strings.HasPrefix(path, "/artifactory/api/import/"),
		strings.HasPrefix(path, "/artifactory/api/export/"),
		strings.HasPrefix(path, "/artifactory/api/artifactexport/"):
		return apierror.ImportExport
	case strings.HasPrefix(path, "/distribution/"):
		return apierror.Distribution
	case strings.HasPrefix(path, "/pfed/"):
		return apierror.PlatformFederation
	case strings.HasPrefix(path, "/grid/"):
		return apierror.Grid
	default:
		return apierror.Artifactory
	}
}

// isTerraformReindex reports whether path is the JFrog reindex operation
// beside the Terraform registry. It is Artifactory's REST operation, not
// the registry's, so it answers in Artifactory's envelope, as the JFrog
// operations beside npm's registry do (RTFACT-30181), where the registry's
// routes answer {"errors":["..."]} (decision 0040). [I] No 7.x capture shows
// this operation's error body (#216). Only POST is the operation, and no
// registry route has two segments, so any method takes the envelope.
func isTerraformReindex(path string) bool {
	rest, ok := strings.CutPrefix(path, "/artifactory/api/terraform/")
	key, op, _ := strings.Cut(rest, "/")
	return ok && key != "" && op == "reindex"
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
