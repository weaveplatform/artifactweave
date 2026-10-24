package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

func registryWorld(t *testing.T) *remoteWorld {
	t.Helper()
	w := newRemoteWorld(t)
	w.c.PackageType = repo.Docker
	w.update()
	w.h = server.New(
		server.Config{
			Store:           w.rt.store,
			Blobs:           w.rt.blobs,
			Auth:            w.rt.auth,
			Tokens:          w.rt.tokens,
			Keys:            w.rt.keys,
			RegistrySources: w.rt.registrySources,
			Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	)
	return w
}

func registryJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func registryDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func registryPut(w *remoteWorld, operation, reference, media string, body []byte) {
	w.up.Set(
		"/v2/team/image/"+operation+"/"+reference,
		fakeupstream.Resource{
			Body: body,
			Header: http.Header{
				"Content-Type":          {media},
				"Docker-Content-Digest": {registryDigest(body)},
			},
		},
	)
}

func registryImage(t *testing.T, w *remoteWorld, marker string) ([]byte, []byte) {
	t.Helper()
	config := []byte(
		`{"os":"linux","architecture":"amd64","config":{"Labels":{"fixture":"` + marker + `"}}}`,
	)
	layer := []byte("layer-content-" + marker)
	registryPut(w, "blobs", registryDigest(config), "application/octet-stream", config)
	registryPut(w, "blobs", registryDigest(layer), "application/octet-stream", layer)
	manifest := registryJSON(t, map[string]any{
		"schemaVersion": 2,
		"mediaType":     layout.MediaTypeOCIManifest,
		"config": layout.Descriptor{
			MediaType: "application/vnd.oci.image.config.v1+json",
			Digest:    registryDigest(config),
			Size:      int64(len(config)),
		},
		"layers": []layout.Descriptor{
			{
				MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
				Digest:    registryDigest(layer),
				Size:      int64(len(layer)),
			},
		},
	})
	registryPut(w, "manifests", registryDigest(manifest), layout.MediaTypeOCIManifest, manifest)
	return manifest, layer
}

func TestAccRegistryRemotePullRefreshAndOffline(t *testing.T) {
	w := registryWorld(t)
	manifest, layer := registryImage(t, w, "one")
	registryPut(w, "manifests", "latest", layout.MediaTypeOCIManifest, manifest)
	path := "/v2/remote/team/image/manifests/latest"
	first := w.request("GET", path, "reader", "")
	remoteStatus(t, first, 200)
	if first.Body.String() != string(manifest) ||
		first.Header().Get("Docker-Content-Digest") != registryDigest(manifest) {
		t.Fatal("manifest bytes or digest changed")
	}
	before := len(w.up.Requests())
	remoteStatus(t, w.request("HEAD", path, "reader", ""), 200)
	if len(w.up.Requests()) != before {
		t.Fatal("warm HEAD used network")
	}
	blobPath := "/v2/remote/team/image/blobs/" + registryDigest(layer)
	ranged := w.request("GET", blobPath, "reader", "", "Range", "bytes=2-6")
	remoteStatus(t, ranged, 206)
	if ranged.Body.String() != string(layer[2:7]) {
		t.Fatal("range changed bytes")
	}
	// Pull and retain the original immutable address before moving its tag.
	remoteStatus(
		t,
		w.request("GET", "/v2/remote/team/image/manifests/"+registryDigest(manifest), "reader", ""),
		200,
	)
	next, _ := registryImage(t, w, "two")
	registryPut(w, "manifests", "latest", layout.MediaTypeOCIManifest, next)
	if got := w.request("GET", path, "reader", ""); got.Body.String() != string(manifest) {
		t.Fatal("fresh tag unexpectedly changed")
	}
	service := item.Service{Store: w.rt.store, Blobs: w.rt.blobs}
	if _, err := service.Zap(
		t.Context(),
		item.Target{Repo: w.c, Cached: true, Path: "team/image"},
	); err != nil {
		t.Fatal(err)
	}
	refreshed := w.request("GET", path, "reader", "")
	remoteStatus(t, refreshed, 200)
	if refreshed.Body.String() != string(next) {
		t.Fatal("zap did not refresh tag")
	}
	w.up.Close()
	for _, target := range []string{blobPath, "/v2/remote/team/image/manifests/" + registryDigest(manifest), path} {
		remoteStatus(t, w.request("GET", target, "reader", ""), 200)
	}
	offline := true
	w.c.Offline = &offline
	w.update()
	remoteStatus(t, w.request("GET", blobPath, "reader", ""), 200)
	remoteStatus(t, w.request("GET", path, "reader", ""), 200)
	remoteStatus(t, w.request("PUT", path, "admin", string(next)), 405)
}

func TestAccRegistryRemoteIndexAndReferrers(t *testing.T) {
	w := registryWorld(t)
	first, _ := registryImage(t, w, "amd64")
	second, _ := registryImage(t, w, "arm64")
	index := registryJSON(
		t,
		map[string]any{
			"schemaVersion": 2,
			"mediaType":     layout.MediaTypeOCIIndex,
			"manifests": []layout.Descriptor{
				{
					MediaType: layout.MediaTypeOCIManifest,
					Digest:    registryDigest(first),
					Size:      int64(len(first)),
					Platform:  &layout.Platform{OS: "linux", Architecture: "amd64"},
				},
				{
					MediaType: layout.MediaTypeOCIManifest,
					Digest:    registryDigest(second),
					Size:      int64(len(second)),
					Platform:  &layout.Platform{OS: "linux", Architecture: "arm64"},
				},
			},
		},
	)
	registryPut(w, "manifests", "multi", layout.MediaTypeOCIIndex, index)
	response := w.request("GET", "/v2/remote/team/image/manifests/multi", "reader", "")
	remoteStatus(t, response, 200)
	if response.Body.String() != string(index) {
		t.Fatal("index rewritten")
	}
	it, err := w.rt.store.Items().Get(t.Context(), "remote", true, "team/image/multi/manifest.json")
	if err != nil {
		t.Fatal("remote index native item missing", err)
	}
	props, err := w.rt.store.Items().Properties(t.Context(), it.ID)
	if err != nil || len(props[layout.ManifestTypeProperty]) != 1 ||
		props[layout.ManifestTypeProperty][0] != layout.MediaTypeOCIIndex {
		t.Fatal("index media properties missing", props, err)
	}
	for _, body := range [][]byte{first, second} {
		if w.up.Count("GET", "/v2/team/image/manifests/"+registryDigest(body)) != 1 {
			t.Fatal("index child not verified")
		}
	}
	listing := registryJSON(
		t,
		map[string]any{
			"schemaVersion": 2,
			"mediaType":     layout.MediaTypeOCIIndex,
			"manifests": []layout.Descriptor{
				{
					MediaType:    layout.MediaTypeOCIManifest,
					Digest:       registryDigest(first),
					Size:         int64(len(first)),
					ArtifactType: "application/example",
				},
			},
		},
	)
	registryPut(w, "referrers", registryDigest(second), layout.MediaTypeOCIIndex, listing)
	ref := w.request(
		"GET",
		"/v2/remote/team/image/referrers/"+registryDigest(
			second,
		)+"?artifactType=application/example",
		"reader",
		"",
	)
	remoteStatus(t, ref, 200)
	if !strings.Contains(ref.Body.String(), registryDigest(first)) {
		t.Fatal("referrer lost")
	}
	w.up.Set(
		"/v2/team/image/tags/list",
		fakeupstream.Resource{
			Body:   []byte(`{"name":"team/image","tags":["z","a"]}`),
			Header: http.Header{"Content-Type": {"application/json"}},
		},
	)
	tags := w.request("GET", "/v2/remote/team/image/tags/list?n=1", "reader", "")
	remoteStatus(t, tags, 200)
	if !strings.Contains(tags.Body.String(), `"tags":["a"]`) ||
		!strings.Contains(tags.Header().Get("Link"), "last=a") {
		t.Fatal(tags.Body, tags.Header())
	}
}

func TestAccRegistryRemoteNoStoreAndNativeRoutes(t *testing.T) {
	w := registryWorld(t)
	w.c.StoreArtifactsLocally = new(false)
	w.update()
	manifest, layer := registryImage(t, w, "transient")
	registryPut(w, "manifests", "latest", layout.MediaTypeOCIManifest, manifest)
	path := "/artifactory/api/docker/remote/v2/team/image/manifests/latest"
	for _, method := range []string{"GET", "HEAD"} {
		got := w.request(method, path, "reader", "")
		remoteStatus(t, got, 200)
		if got.Header().Get("Docker-Content-Digest") != registryDigest(manifest) {
			t.Fatal("transient digest lost")
		}
		if method == "GET" && got.Body.String() != string(manifest) {
			t.Fatal("transient manifest changed")
		}
	}
	remoteStatus(t, w.request("GET", path, "reader", "", "Accept", layout.MediaTypeDockerList), 404)
	remoteStatus(
		t,
		w.request("GET", path, "reader", "", "If-None-Match", `"`+registryDigest(manifest)+`"`),
		304,
	)
	got := w.request(
		"GET",
		"/v2/remote/team/image/blobs/"+registryDigest(layer),
		"reader",
		"",
		"Range",
		"bytes=1-4",
	)
	remoteStatus(t, got, 206)
	if got.Body.String() != string(layer[1:5]) {
		t.Fatal("transient range changed")
	}
	entries, _, err := w.rt.store.RemoteCache().List(t.Context(), "remote", "", store.Page{})
	if err != nil || len(entries) != 0 {
		t.Fatal("no-store populated cache", entries, err)
	}
	remoteStatus(t, w.request("GET", "/v2/remote/team/image/manifests/missing", "reader", ""), 404)
	remoteStatus(
		t,
		w.request(
			"GET",
			"/v2/remote/team/image/blobs/sha256:"+strings.Repeat("0", 64),
			"reader",
			"",
		),
		404,
	)
}

func TestAccRegistryRemoteRejectsInvalidIndexChildren(t *testing.T) {
	for _, mode := range []string{"size", "media", "cached media", "missing", "child count"} {
		t.Run(mode, func(t *testing.T) {
			w := registryWorld(t)
			child, _ := registryImage(t, w, "child")
			if mode == "cached media" {
				remoteStatus(
					t,
					w.request(
						"GET",
						"/v2/remote/team/image/manifests/"+registryDigest(child),
						"reader",
						"",
					),
					200,
				)
			}
			d := layout.Descriptor{
				Digest:    registryDigest(child),
				Size:      int64(len(child)),
				MediaType: layout.MediaTypeOCIManifest,
			}
			switch mode {
			case "size":
				d.Size++
			case "media", "cached media":
				d.MediaType = layout.MediaTypeDockerManifest
			case "missing":
				d.Digest = "sha256:" + strings.Repeat("0", 64)
			}
			descriptors := []layout.Descriptor{d}
			if mode == "child count" {
				for range 64 {
					descriptors = append(descriptors, d)
				}
			}
			index := registryJSON(
				t,
				map[string]any{
					"schemaVersion": 2,
					"mediaType":     layout.MediaTypeOCIIndex,
					"manifests":     descriptors,
				},
			)
			registryPut(w, "manifests", "bad", layout.MediaTypeOCIIndex, index)
			got := w.request("GET", "/v2/remote/team/image/manifests/bad", "reader", "")
			if got.Code == 200 {
				t.Fatal("invalid index published")
			}
			if _, err := w.rt.store.Items().
				Get(t.Context(), "remote", true, "team/image/bad/manifest.json"); err == nil {
				t.Fatal("invalid index item published")
			}
		})
	}
}

func TestAccRegistryRemoteIntegrityAndDeniedPaths(t *testing.T) {
	for _, fault := range []string{"digest", "length", "schema", "unauthorized", "rate", "denied"} {
		t.Run(fault, func(t *testing.T) {
			w := registryWorld(t)
			manifest, layer := registryImage(t, w, "fault")
			path := "/v2/remote/team/image/manifests/latest"
			resource := fakeupstream.Resource{
				Body: manifest,
				Header: http.Header{
					"Content-Type":          {layout.MediaTypeOCIManifest},
					"Docker-Content-Digest": {registryDigest(manifest)},
				},
			}
			want := 502
			switch fault {
			case "digest":
				resource.Header.Set("Docker-Content-Digest", registryDigest(layer))
			case "length":
				resource.Fault.LengthDelta = 3
			case "schema":
				resource.Body = []byte(`{"schemaVersion":1}`)
				resource.Header.Del("Docker-Content-Digest")
			case "unauthorized":
				resource.Status = 401
				want = 502
			case "rate":
				resource.Status = 429
				resource.Header.Set("Retry-After", "4")
				want = 429
			case "denied":
				want = 403
			}
			w.up.Set("/v2/team/image/manifests/latest", resource)
			user := "reader"
			if fault == "denied" {
				user = "denied"
			}
			remoteStatus(t, w.request("GET", path, user, ""), want)
			if fault == "denied" && len(w.up.Requests()) != 0 {
				t.Fatal("denial touched upstream")
			}
		})
	}
}
