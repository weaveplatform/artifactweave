package server

import (
	"net/http"
	"strings"
	"testing"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// Shared OCI fixture data is used by the checksum and Helm OCI assertions.
func sha256Digest(body string) string { return support.SHA256Digest(body) }

const registryConfig = support.RegistryConfig

var registryLayer = support.RegistryLayer

func registryManifest(note string) string { return support.RegistryManifest(note) }

func webServerStatusRows() []statusCase {
	const route = "GET /artifactory/api/system/configuration/webServer"
	const path = "/artifactory/api/system/configuration/webServer"
	return []statusCase{
		{route, path, "admin", "", 200},
		{route, path, "user", "", 200},
		{route, path, "anonymous", "", 401},
		{route, path, "", "", 401},
	}
}

func webServerMatrixRows() []matrixRow {
	return []matrixRow{at("GET /artifactory/api/system/configuration/webServer", "", signedIn, 200)}
}

func allOf(rules ...rule) rule {
	return func(p matrixPrincipal) int {
		for _, r := range rules {
			if status := r(p); status != 0 {
				return status
			}
		}
		return 0
	}
}

// contentTypeOf decodes a route table's body prefix for all formats.
func contentTypeOf(body string) (contentType, rest string) {
	if b, ok := strings.CutPrefix(body, "multipart:"); ok {
		boundary, form, _ := strings.Cut(b, "\n")
		return "multipart/form-data; boundary=" + boundary, form
	}
	if b, ok := strings.CutPrefix(body, "manifest:"); ok {
		return layout.MediaTypeOCIManifest, b
	}
	if b, ok := strings.CutPrefix(body, "form:"); ok {
		return "application/x-www-form-urlencoded", b
	}
	return "application/json", body
}

// Content type normalization is middleware shared by generated routes and
// registry routes, so this assertion stays in the root server package.
func TestAccRegistryKeepsContentType(t *testing.T) {
	const manifest = "application/vnd.oci.image.manifest.v1+json"
	for path, want := range map[string]string{
		"/v2/r/app/manifests/v1":                        manifest,
		"/artifactory/api/docker/r/v2/app/manifests/v1": manifest,
		"/artifactory/api/repositories/r":               "application/json",
	} {
		var got string
		h := normalizeContentType(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			got = r.Header.Get("Content-Type")
		}))
		do(t, h, http.MethodPut, path, map[string]string{"Content-Type": manifest}, "{}")
		if got != want {
			t.Errorf("%s: Content-Type %q, want %q", path, got, want)
		}
	}
}
