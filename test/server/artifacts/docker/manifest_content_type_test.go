package docker_test

import (
	"net/http"
	"testing"

	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
)

// TestAccRegistryManifestIgnoresContentTypeProperty: a manifest whose file
// carries artifactory.content-type is served through the registry, by tag
// and by digest, GET and HEAD, with the media type it was pushed with, and
// a client that accepts only that type still gets it. 7.x's registry hands
// a manifest's download a DockerManifestResponse, whose setContentType
// drops the type the download would send, the property's included, and
// sets the manifest's type itself later in the flow (7.161.26 OSS
// sources). Whether that later type ever reads the property is not in the
// OSS sources, so this is kept as it was [I] (#1396).
func TestAccRegistryManifestIgnoresContentTypeProperty(t *testing.T) {
	body := registryManifest("content type")
	w := dockerLocalWorld(t, "a layer", body)
	wantStatus(t, w.do(http.MethodPut, "/artifactory/api/storage/docker-local/app/v1/manifest.json"+
		"?properties=artifactory.content-type=text%2Fx-custom", "admin", ""), http.StatusNoContent)
	digest := sha256Digest(body)
	for _, target := range []string{
		"/v2/docker-local/app/manifests/v1",
		"/v2/docker-local/app/manifests/" + digest,
		"/artifactory/api/docker/docker-local/v2/app/manifests/v1",
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, accept := range []string{"", layout.MediaTypeOCIManifest} {
				rec := w.do(method, target, "admin", "", "Accept", accept)
				wantStatus(t, rec, http.StatusOK)
				if got := rec.Header().Get("Content-Type"); got != layout.MediaTypeOCIManifest {
					t.Fatalf("%s %s Accept %q: Content-Type %q, want %q",
						method, target, accept, got, layout.MediaTypeOCIManifest)
				}
			}
		}
	}
}
