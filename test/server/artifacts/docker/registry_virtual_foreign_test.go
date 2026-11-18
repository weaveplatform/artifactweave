package docker_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

func virtualForeignManifest(t *testing.T, w *remoteWorld, media string, descriptor layout.Descriptor) []byte {
	t.Helper()
	body, _ := virtualRegistryPush(t, w, "first", "foreign", "foreign")
	var manifest map[string]any
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["mediaType"] = media
	manifest["layers"] = []layout.Descriptor{descriptor}
	body = registryJSON(t, manifest)
	remoteStatus(t, w.request("PUT", "/v2/first/team/image/manifests/foreign", "admin", string(body), "Content-Type", media), 201)
	return body
}

func TestAccRegistryVirtualForeignLayers(t *testing.T) {
	for _, mode := range []string{"allowed", "flag off", "remote disabled", "pattern", "wrong digest", "wrong size", "zero descriptor", "nested manifest excluded", "hidden first alias", "member manifest excluded", "unreadable manifest", "ordinary layer", "redirect forbidden"} {
		t.Run(mode, func(t *testing.T) {
			w := virtualRegistryWorld(t, repo.Docker)
			foreign := []byte("local-foreign-layer")
			foreignURL := w.up.URL + "/foreign/layer"
			w.c.ExternalDependenciesEnabled = new(true)
			w.c.ExternalDependenciesPatterns = []string{w.up.URL + "/foreign/**"}
			w.c.CustomHTTPHeaders = []repo.CustomHTTPHeader{{Name: "X-Remote-Secret", Value: "secret"}}
			w.c.Username, w.c.Password = "remote-secret", "password"
			if mode == "remote disabled" {
				w.c.ExternalDependenciesEnabled = new(false)
			}
			if mode == "pattern" {
				w.c.ExternalDependenciesPatterns = []string{w.up.URL + "/elsewhere/**"}
			}
			w.update()
			virtualRegistryConfig(t, w, "virtual", func(c *repo.Config) { c.CachingLocalForeignLayersEnabled = new(mode != "flag off") })
			d := layout.Descriptor{
				MediaType: "application/vnd.oci.image.layer.nondistributable.v1.tar",
				Digest:    registryDigest(foreign),
				Size:      int64(len(foreign)),
				URLs:      []string{foreignURL},
			}
			resource := fakeupstream.Resource{Body: foreign}
			if mode == "wrong digest" {
				resource.Body = []byte("different-content!!")
			}
			if mode == "wrong size" {
				d.Size++
			}
			if mode == "zero descriptor" {
				d.Size = 0
			}
			if mode == "ordinary layer" {
				d.MediaType = "application/vnd.oci.image.layer.v1.tar"
				remoteStatus(t, w.request("POST", "/v2/first/team/image/blobs/uploads/?digest="+d.Digest, "admin", string(foreign)), 201)
			}
			forbidden := fakeupstream.New(t, fakeupstream.Options{})
			if mode == "redirect forbidden" {
				resource.Redirect = forbidden.URL + "/layer"
			}
			w.up.Set("/foreign/layer", resource)
			manifest := virtualForeignManifest(t, w, layout.MediaTypeOCIManifest, d)
			if mode == "hidden first alias" {
				remoteStatus(t, w.request("PUT", "/v2/first/team/image/manifests/visible", "admin", string(manifest), "Content-Type", layout.MediaTypeOCIManifest), 201)
				virtualRegistryConfig(t, w, "inner", func(c *repo.Config) { c.ExcludesPattern = new("team/image/foreign/**") })
			}
			if mode == "ordinary layer" {
				hex, _ := layout.SHA256Hex(d.Digest)
				copies, err := layout.FindBlobs(t.Context(), w.rt.Store.Items(), "first", "team/image", hex)
				if err != nil {
					t.Fatal(err)
				}
				for _, it := range copies {
					if _, err = w.rt.Store.Items().Delete(t.Context(), "first", false, it.FullPath()); err != nil {
						t.Fatal(err)
					}
				}
			}
			if mode == "nested manifest excluded" {
				virtualRegistryConfig(t, w, "inner", func(c *repo.Config) { c.ExcludesPattern = new("team/image/foreign/**") })
			}
			if mode == "member manifest excluded" {
				virtualRegistryConfig(t, w, "first", func(c *repo.Config) { c.ExcludesPattern = new("team/image/foreign/**") })
			}
			as := "admin"
			if mode == "unreadable manifest" {
				as = "reader"
			}
			// Pulling the manifest is not required to seed process-local descriptor state.
			r := w.request("GET", "/v2/virtual/team/image/blobs/"+d.Digest, as, "")
			if mode != "allowed" && mode != "hidden first alias" {
				if r.Code == 200 {
					t.Fatal("unsafe fallback accepted", mode, r.Body.String())
				}
				if mode == "nested manifest excluded" || mode == "member manifest excluded" || mode == "unreadable manifest" || mode == "flag off" || mode == "remote disabled" ||
					mode == "pattern" ||
					mode == "ordinary layer" {
					if w.up.Count("GET", "/foreign/layer") != 0 {
						t.Fatal("inadmissible witness contacted URL")
					}
				}
				return
			}
			remoteStatus(t, r, 200)
			if r.Body.String() != string(foreign) || r.Header().Get("Docker-Content-Digest") != d.Digest {
				t.Fatal(r.Header(), r.Body.String())
			}
			for _, request := range w.up.Requests() {
				if request.Path == "/foreign/layer" &&
					(request.Header.Get("Authorization") != "" || request.Header.Get("X-Remote-Secret") != "" || request.Header.Get("Cookie") != "") {
					t.Fatal("foreign credentials leaked", request)
				}
			}
			hex, _ := layout.SHA256Hex(d.Digest)
			if _, err := w.rt.Store.Items().Get(t.Context(), "remote", true, layout.UploadPath("team/image", hex)); err != nil {
				t.Fatal("foreign layer not cached in remote", err)
			}
			if _, err := w.rt.Store.Items().Get(t.Context(), "first", false, layout.UploadPath("team/image", hex)); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("local tree changed", err)
			}
			before := len(w.up.Requests())
			w.c.Offline = new(true)
			w.update()
			remoteStatus(t, w.request("HEAD", "/v2/virtual/team/image/blobs/"+d.Digest, "admin", ""), 200)
			if len(w.up.Requests()) != before {
				t.Fatal("offline foreign cache contacted origin")
			}
			w.c.ExternalDependenciesEnabled = new(false)
			w.update()
			remoteStatus(t, w.request("GET", "/v2/virtual/team/image/blobs/"+d.Digest, "admin", ""), 200)
			if len(w.up.Requests()) != before {
				t.Fatal("warm blob required foreign origin")
			}
			// The original local manifest remains unchanged.
			r = w.request("GET", "/v2/first/team/image/manifests/foreign", "admin", "")
			remoteStatus(t, r, 200)
			if r.Body.String() != string(manifest) {
				t.Fatal("manifest rewritten")
			}
			if len(forbidden.Requests()) != 0 {
				t.Fatal("redirect escaped allowlist")
			}
		})
	}
}

func TestAccRegistryVirtualForeignRemoteOrder(t *testing.T) {
	w := virtualRegistryWorld(t, repo.Docker)
	body := []byte("foreign")
	w.up.Set("/foreign/layer", fakeupstream.Resource{Body: body})
	d := layout.Descriptor{
		MediaType: "application/vnd.docker.image.rootfs.foreign.diff.tar.gzip",
		Digest:    registryDigest(body),
		Size:      int64(len(body)),
		URLs:      []string{w.up.URL + "/foreign/layer"},
	}
	virtualForeignManifest(t, w, layout.MediaTypeDockerManifest, d)
	w.c.ExternalDependenciesEnabled = new(true)
	w.c.ExternalDependenciesPatterns = []string{w.up.URL + "/foreign/**"}
	w.update()
	blocked := *repo.Defaults(&repo.Config{Key: "blocked", Class: repo.Remote, PackageType: repo.Docker, URL: w.up.URL, ExternalDependenciesEnabled: new(true), ExternalDependenciesPatterns: []string{w.up.URL + "/not-allowed/**"}})
	if err := w.rt.Store.Repos().Create(t.Context(), blocked); err != nil {
		t.Fatal(err)
	}
	virtualRegistryConfig(t, w, "virtual", func(c *repo.Config) {
		c.Repositories = []string{"first", "blocked", "remote"}
		c.CachingLocalForeignLayersEnabled = new(true)
	})
	r := w.request("GET", "/v2/virtual/team/image/blobs/"+d.Digest, "admin", "")
	remoteStatus(t, r, 200)
	if !strings.Contains(r.Body.String(), "foreign") || w.up.Count("GET", "/foreign/layer") != 1 {
		t.Fatal(r.Body.String(), w.up.Requests())
	}
}

func TestAccRegistryVirtualForeignChunkedZero(t *testing.T) {
	w := virtualRegistryWorld(t, repo.Docker)
	router := chi.NewRouter()
	router.Get("/foreign/layer", func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(200)
		rw.(http.Flusher).Flush()
		_, _ = rw.Write([]byte("nonempty chunked body"))
	})
	up := httptest.NewServer(router)
	t.Cleanup(up.Close)
	w.c.URL = up.URL
	w.c.ExternalDependenciesEnabled = new(true)
	w.c.ExternalDependenciesPatterns = []string{up.URL + "/foreign/**"}
	w.update()
	virtualRegistryConfig(t, w, "virtual", func(c *repo.Config) { c.CachingLocalForeignLayersEnabled = new(true) })
	d := layout.Descriptor{MediaType: "application/vnd.oci.image.layer.nondistributable.v1.tar", Digest: registryDigest(nil), Size: 0, URLs: []string{up.URL + "/foreign/layer"}}
	virtualForeignManifest(t, w, layout.MediaTypeOCIManifest, d)
	r := w.request("GET", "/v2/virtual/team/image/blobs/"+d.Digest, "admin", "")
	if r.Code == 200 {
		t.Fatal("nonempty chunked body passed zero descriptor", r.Body.String())
	}
	resource, err := layout.ForeignLayerResource("team/image", d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.rt.Store.Items().Get(t.Context(), "remote", true, resource.Path); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("invalid bytes published", err)
	}
}

func TestAccRegistryVirtualForeignSourcePolicy(t *testing.T) {
	for _, mode := range []string{"no origin", "offline miss", "read only cold", "non storing"} {
		t.Run(mode, func(t *testing.T) {
			w := virtualRegistryWorld(t, repo.Docker)
			body := []byte("policy-foreign-layer")
			w.up.Set("/foreign/layer", fakeupstream.Resource{Body: body})
			w.c.ExternalDependenciesEnabled = new(true)
			w.c.ExternalDependenciesPatterns = []string{w.up.URL + "/foreign/**"}
			if mode == "offline miss" {
				w.c.Offline = new(true)
			}
			if mode == "non storing" {
				w.c.StoreArtifactsLocally = new(false)
			}
			w.update()
			virtualRegistryConfig(t, w, "virtual", func(c *repo.Config) { c.CachingLocalForeignLayersEnabled = new(true) })
			d := layout.Descriptor{
				MediaType: "application/vnd.oci.image.layer.nondistributable.v1.tar",
				Digest:    registryDigest(body),
				Size:      int64(len(body)),
				URLs:      []string{w.up.URL + "/foreign/layer"},
			}
			virtualForeignManifest(t, w, layout.MediaTypeOCIManifest, d)
			as := "admin"
			headers := []string{}
			if mode == "no origin" {
				headers = []string{"X-Artifactory-Originated", "another-node"}
			}
			if mode == "read only cold" {
				as = "read-only"
				if err := w.rt.Store.Users().Create(t.Context(), accesstest.User{Name: as, PasswordHash: support.CheapHash}); err != nil {
					t.Fatal(err)
				}
				if err := w.rt.Store.Permissions().
					Create(t.Context(), accesstest.Permission{Name: as, Resources: map[accessapi.ResourceType]accesstest.Resource{accessapi.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]accessapi.Actions{as: accessapi.ActionRead}}, Targets: map[string]accesstest.Target{"first": {}, "remote": {}}}}}); err != nil {
					t.Fatal(err)
				}
			}
			r := w.request("GET", "/v2/virtual/team/image/blobs/"+d.Digest, as, "", headers...)
			if mode != "non storing" {
				remoteStatus(t, r, 404)
				if mode != "read only cold" && w.up.Count("GET", "/foreign/layer") != 0 {
					t.Fatal("source policy allowed a foreign fetch")
				}
				resource, err := layout.ForeignLayerResource("team/image", d)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = w.rt.Store.Items().Get(t.Context(), "remote", true, resource.Path); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatal("refused source published bytes", err)
				}
				return
			}
			remoteStatus(t, r, 200)
			resource, err := layout.ForeignLayerResource("team/image", d)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = w.rt.Store.Items().Get(t.Context(), "remote", true, resource.Path); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("non-storing remote published bytes", err)
			}
			remoteStatus(t, w.request("GET", "/v2/virtual/team/image/blobs/"+d.Digest, "admin", ""), 200)
			if w.up.Count("GET", "/foreign/layer") != 2 {
				t.Fatal("non-storing source reused unpublished content")
			}
		})
	}
}
