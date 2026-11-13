package docker_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestAccRegistryBlobIgnoresContentTypeProperty: a layer whose stored
// files carry artifactory.content-type downloads through the registry,
// GET and HEAD, with the type it has without the property. The item route
// applies the property (#1380); 7.x's Docker API is not in its OSS
// sources, and JFrog's docs do not say it reads the property.
func TestAccRegistryBlobIgnoresContentTypeProperty(t *testing.T) {
	const layer = "a layer"
	w := dockerLocalWorld(t, layer, registryManifest("content type"))
	route := "/v2/docker-local/app/blobs/" + sha256Digest(layer)
	methods := []string{http.MethodGet, http.MethodHead}
	before := map[string]string{}
	for _, m := range methods {
		rec := w.do(m, route, "admin", "")
		wantStatus(t, rec, http.StatusOK)
		before[m] = rec.Header().Get("Content-Type")
	}
	rec := w.do(
		http.MethodGet,
		"/artifactory/api/storage/docker-local/app?list&deep=1",
		"admin",
		"",
	)
	wantStatus(t, rec, http.StatusOK)
	var listing struct {
		Files []struct {
			URI string `json:"uri"`
		} `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	name := strings.Replace(sha256Digest(layer), ":", "__", 1)
	set := 0
	for _, f := range listing.Files {
		if strings.HasSuffix(f.URI, "/"+name) {
			wantStatus(t, w.do(http.MethodPut, "/artifactory/api/storage/docker-local/app"+f.URI+
				"?properties=artifactory.content-type=text%2Fx-custom", "admin", ""), http.StatusNoContent)
			set++
		}
	}
	if set == 0 {
		t.Fatalf("no stored file of the layer in %s", rec.Body)
	}
	for _, m := range methods {
		rec := w.do(m, route, "admin", "")
		wantStatus(t, rec, http.StatusOK)
		if got := rec.Header().Get("Content-Type"); got != before[m] {
			t.Fatalf(
				"%s %s: Content-Type %q, want %q as without the property",
				m,
				route,
				got,
				before[m],
			)
		}
	}
}
