package docker_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
)

// TestAccRegistryManifestIgnoresContentTypeProperty: a manifest whose file
// carries artifactory.content-type is served through the registry, by tag
// and by digest, GET and HEAD, with the media type it was pushed with, and
// a client that accepts only that type still gets it, whether the push set
// the property or a user changed it after. 7.x's registry hands a
// manifest's download a DockerManifestResponse, whose setContentType drops
// the type the download would send, the property's included, and sets the
// manifest's type itself later in the flow (7.161.26 OSS sources). Whether
// that later type ever reads the property is not in the OSS sources, so
// this is kept as it was [I] (#1396).
func TestAccRegistryManifestIgnoresContentTypeProperty(t *testing.T) {
	body := registryManifest("content type")
	w := dockerLocalWorld(t, "a layer", body)
	digest := sha256Digest(body)
	for _, custom := range []bool{false, true} {
		if custom {
			wantStatus(t, w.do(http.MethodPut,
				"/artifactory/api/storage/docker-local/app/v1/manifest.json"+
					"?properties=artifactory.content-type=text%2Fx-custom", "admin", ""),
				http.StatusNoContent)
		}
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
						t.Fatalf("custom %t %s %s Accept %q: Content-Type %q, want %q",
							custom, method, target, accept, got, layout.MediaTypeOCIManifest)
					}
				}
			}
		}
	}
}

// manifestProperties is the properties of the file at path in key.
func manifestProperties(t *testing.T, w *itemWorld, key, path string) map[string][]string {
	t.Helper()
	rec := w.do(http.MethodGet, "/artifactory/api/storage/"+key+"/"+path+"?properties", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	var got struct {
		Properties map[string][]string `json:"properties"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got.Properties
}

// TestAccRegistryPushSetsManifestContentType: an image manifest pushed to a
// local Docker, OCI or Helm OCI repository, Docker or OCI, by tag and as an
// index's platform, carries artifactory.content-type equal to its
// docker.manifest.type, as 7.161.26's AbstractMetadataProviderTest gives a
// docker-local and an oci-local manifest.json. So the item route serves the
// file, GET and HEAD, with that type, and the storage API still types it by
// its path. An index's list.manifest.json carries no such property, as no
// source shows one [I].
func TestAccRegistryPushSetsManifestContentType(t *testing.T) {
	w := newItemWorld(t, false)
	oci := registryManifest("pushed type")
	docker := strings.Replace(registryManifest("pushed docker type"),
		layout.MediaTypeOCIManifest, layout.MediaTypeDockerManifest, 1)
	for _, c := range []repo.Config{
		{Key: "docker-local", Class: repo.Local, PackageType: repo.Docker},
		{Key: "oci-local", Class: repo.Local, PackageType: repo.OCI},
		{Key: "helmoci-local", Class: repo.Local, PackageType: repo.HelmOCI},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
		for _, b := range []string{registryConfig, "registry layer"} {
			wantStatus(t, w.do(http.MethodPost, "/v2/"+c.Key+"/app/blobs/uploads/?digest="+
				sha256Digest(b), "admin", b), http.StatusCreated)
		}
		for _, p := range []struct{ tag, body, mediaType string }{
			{"v1", oci, layout.MediaTypeOCIManifest},
			{"v2", docker, layout.MediaTypeDockerManifest},
			{"multi", indexOf(oci), layout.MediaTypeOCIIndex},
		} {
			wantStatus(t, w.do(http.MethodPut, "/v2/"+c.Key+"/app/manifests/"+p.tag, "admin",
				p.body, "Content-Type", p.mediaType), http.StatusCreated)
		}
		for path, mediaType := range map[string]string{
			"app/v1/manifest.json":                oci + "|" + layout.MediaTypeOCIManifest,
			"app/v2/manifest.json":                docker + "|" + layout.MediaTypeDockerManifest,
			"app/multi-linux-amd64/manifest.json": oci + "|" + layout.MediaTypeOCIManifest,
			"app/multi/list.manifest.json":        indexOf(oci) + "|",
		} {
			body, mediaType, _ := strings.Cut(mediaType, "|")
			props := manifestProperties(t, w, c.Key, path)
			want := []string{mediaType}
			if mediaType == "" {
				want = nil
			}
			if got := props["artifactory.content-type"]; !slices.Equal(got, want) {
				t.Fatalf("%s %s: artifactory.content-type %v, want %v", c.Key, path, got, want)
			}
			if mediaType == "" {
				mediaType = "application/json"
			}
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				rec := w.do(method, "/artifactory/"+c.Key+"/"+path, "admin", "")
				wantStatus(t, rec, http.StatusOK)
				if got := rec.Header().Get("Content-Type"); got != mediaType {
					t.Fatalf("%s %s %s: Content-Type %q, want %q",
						c.Key, method, path, got, mediaType)
				}
				if method == http.MethodGet && rec.Body.String() != body {
					t.Fatalf("%s %s: body %s", c.Key, path, rec.Body)
				}
			}
			rec := w.do(http.MethodGet, "/artifactory/api/storage/"+c.Key+"/"+path, "admin", "")
			wantStatus(t, rec, http.StatusOK)
			var info struct {
				MimeType string `json:"mimeType"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil ||
				info.MimeType != "application/json" {
				t.Fatalf("%s %s: storage %s %v", c.Key, path, rec.Body, err)
			}
		}
	}
}
