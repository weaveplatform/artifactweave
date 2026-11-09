package server

import (
	"net/http"
	"strings"

	dockerhttp "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/docker/http"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
)

// errorProfileRules select the error response contract from the request
// path. Terraform providers join errors[] entries, Docker reads
// errors[].code, npm prints error, and jfrog-client-go prints the body.
// Order matters: the JFrog operations beside the npm and Terraform
// registries must match before the registries' own prefixes, and the first
// matching rule wins.
var errorProfileRules = []ProfileRule{
	{Match: pathPrefix("/xray/"), Profile: apierror.ProfileXray},
	{
		Match: func(path string) bool {
			return npmOperation(path) || isNPMReindex(path) || isTerraformReindex(path)
		},
		Profile: apierror.ProfileArtifactory,
	},
	{Match: pathPrefix("/artifactory/api/npm/"), Profile: apierror.ProfileNPM},
	{
		Match: func(path string) bool {
			return path == "/artifactory/api/terraform/remote/v2" ||
				strings.HasPrefix(path, "/artifactory/api/terraform/remote/v2/")
		},
		Profile: apierror.ProfileTerraformBackend,
	},
	{Match: pathPrefix("/artifactory/api/terraform/"), Profile: apierror.ProfileTerraformRegistry},
	{Match: pathPrefix("/artifactory/api/go/"), Profile: apierror.ProfileText},
	{
		// Named, though the default gives the same, because research 10
		// once gave PyPI and Helm plain text. 7.x answers Artifactory's
		// envelope under all three (decisions 0037, 0038 and 0039), and
		// pip, twine, NuGet.Protocol and helm read only the status.
		Match: pathPrefix(
			"/artifactory/api/pypi/",
			"/artifactory/api/nuget/",
			"/artifactory/api/helm/",
		),
		Profile: apierror.ProfileArtifactory,
	},
	{
		// The JFrog operations beside the registry (promote,
		// parentManifests) keep the Artifactory envelope (decision 0031).
		Match:   dockerhttp.IsRegistryPath,
		Profile: apierror.ProfileOCI,
	},
	{Match: pathPrefix("/access/", "/event/", "/worker/"), Profile: apierror.ProfileAccess},
	{Match: pathPrefix("/artifactory/api/retention/"), Profile: apierror.ProfileArtifactoryLegacy},
	{
		Match: pathPrefix(
			"/artifactory/api/import/",
			"/artifactory/api/export/",
			"/artifactory/api/artifactexport/",
		),
		Profile: apierror.ProfileImportExport,
	},
	{Match: pathPrefix("/distribution/"), Profile: apierror.ProfileJFrogDistribution},
	{Match: pathPrefix("/pfed/"), Profile: apierror.ProfilePlatformFederation},
	{Match: pathPrefix("/grid/"), Profile: apierror.ProfileGrid},
}

// errorProfileFor is the profile errorProfileRules give path. Unclaimed
// routes keep the same profile as handled requests so clients can read
// their 404 responses.
func errorProfileFor(path string) apierror.Profile {
	return ProfileFor(errorProfileRules, path)
}

// pathPrefix matches a path that starts with any of prefixes.
func pathPrefix(prefixes ...string) func(path string) bool {
	return func(path string) bool {
		for _, p := range prefixes {
			if strings.HasPrefix(path, p) {
				return true
			}
		}
		return false
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
		apierror.Write(w, errorProfileFor(r.URL.Path), apierror.NotSupported(product))
	}
}

// notFound is the catch-all. Under /v2 the OCI code is NAME_UNKNOWN because
// that is what registry clients expect for an unknown repository.
func notFound(w http.ResponseWriter, r *http.Request) {
	s := errorProfileFor(r.URL.Path)
	e := apierror.New(http.StatusNotFound, "not found: "+r.Method+" "+r.URL.Path)
	if s == apierror.ProfileOCI {
		e.Code = "NAME_UNKNOWN"
	}
	apierror.Write(w, s, e)
}

// methodNotAllowed keeps the envelope rule for 405 too.
func methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	apierror.Write(
		w,
		errorProfileFor(r.URL.Path),
		apierror.New(http.StatusMethodNotAllowed, "method not allowed: "+r.Method+" "+r.URL.Path),
	)
}
