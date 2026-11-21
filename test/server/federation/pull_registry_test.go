package federation_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

func TestAccSinglePullRegistryProcessesSQLite(t *testing.T) {
	qualifyPullRegistryProcesses(t, "sqlite", false)
}

func TestAccSinglePullRegistryProcessesPostgres(t *testing.T) {
	qualifyPullRegistryProcesses(t, "postgres", false)
}

func TestAccSinglePullRegistryVirtualProcessesSQLite(t *testing.T) {
	qualifyPullRegistryProcesses(t, "sqlite", true)
}

func TestAccSinglePullRegistryVirtualProcessesPostgres(t *testing.T) {
	qualifyPullRegistryProcesses(t, "postgres", true)
}

func registryDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func nativeRegistryRequest(t *testing.T, w *pushWorld, origin, method, path, contentType string, body []byte, want int) ([]byte, http.Header) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), method, origin+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth("admin", "pw")
	request.Header.Set("Content-Type", contentType)
	request.Header.Set(
		"Accept",
		"application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json, application/vnd.docker.distribution.manifest.list.v2+json",
	)
	response, err := w.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != want {
		t.Fatalf("native %s %s got%d want%d: %s err=%v", method, path, response.StatusCode, want, data, err)
	}
	return data, response.Header.Clone()
}

func putRegistryBlob(t *testing.T, w *pushWorld, body []byte) string {
	t.Helper()
	base := "/artifactory/api/docker/target/v2/team/image/blobs/uploads/"
	_, header := nativeRegistryRequest(t, w, w.target, http.MethodPost, base, "application/octet-stream", nil, http.StatusAccepted)
	location := header.Get("Location")
	address, err := url.Parse(location)
	origin, originErr := url.Parse(w.target)
	if err != nil || originErr != nil || address.IsAbs() && (address.Scheme != origin.Scheme || address.Host != origin.Host) {
		t.Fatal("native fixture upload escaped its owned application")
	}
	location = address.RequestURI()
	if !strings.HasPrefix(location, "/artifactory/api/docker/target/") {
		t.Fatal("native fixture upload returned an unexpected location")
	}
	digest := registryDigest(body)
	separator := "?"
	if strings.Contains(location, "?") {
		separator = "&"
	}
	nativeRegistryRequest(t, w, w.target, http.MethodPut, location+separator+"digest="+digest, "application/octet-stream", body, http.StatusCreated)
	return digest
}

func helmPullLayer(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	archive := tar.NewWriter(gz)
	chart := []byte("apiVersion: v2\nname: chart\nversion: 1.0.0\n")
	if err := archive.WriteHeader(&tar.Header{Name: "chart/Chart.yaml", Mode: 0o644, Size: int64(len(chart))}); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Write(chart); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func qualifyPullRegistryProcesses(t *testing.T, backend string, virtual bool) {
	t.Helper()
	for _, kind := range []string{"docker", "oci", "helmoci"} {
		t.Run(kind, func(t *testing.T) {
			var localDSN, peerDSN string
			if backend == "postgres" {
				dsn := sqltest.PostgresDSN(t)
				localDSN, peerDSN = sqltest.ScratchDSN(t, dsn), sqltest.ScratchDSN(t, dsn)
			}
			w := newPushWorld(t, backend, localDSN, peerDSN, kind)
			config, layer := []byte(`{"os":"linux","architecture":"amd64"}`), []byte("verified native registry layer")
			configType, layerType, manifestType := "application/vnd.oci.image.config.v1+json", "application/vnd.oci.image.layer.v1.tar+gzip", "application/vnd.oci.image.manifest.v1+json"
			if kind == "docker" {
				configType, layerType, manifestType = "application/vnd.docker.container.image.v1+json", "application/vnd.docker.image.rootfs.diff.tar.gzip", "application/vnd.docker.distribution.manifest.v2+json"
			} else if kind == "helmoci" {
				config, layer = []byte(`{"name":"chart","version":"1.0.0"}`), helmPullLayer(t)
				configType, layerType = "application/vnd.cncf.helm.config.v1+json", "application/vnd.cncf.helm.chart.content.v1.tar+gzip"
			}
			configDigest, layerDigest := putRegistryBlob(t, w, config), putRegistryBlob(t, w, layer)
			manifest, err := json.Marshal(map[string]any{
				"schemaVersion": 2, "mediaType": manifestType,
				"config": map[string]any{"mediaType": configType, "digest": configDigest, "size": len(config)},
				"layers": []any{map[string]any{"mediaType": layerType, "digest": layerDigest, "size": len(layer)}},
			})
			if err != nil {
				t.Fatal(err)
			}
			tag := "latest"
			if kind == "helmoci" {
				tag = "1.0.0"
			}
			nativeRegistryRequest(t, w, w.target, http.MethodPut, "/artifactory/api/docker/target/v2/team/image/manifests/"+tag, manifestType, manifest, http.StatusCreated)
			var index []byte
			indexType := "application/vnd.oci.image.index.v1+json"
			if kind == "docker" {
				indexType = "application/vnd.docker.distribution.manifest.list.v2+json"
			}
			if kind != "helmoci" {
				digest := registryDigest(manifest)
				nativeRegistryRequest(t, w, w.target, http.MethodPut, "/artifactory/api/docker/target/v2/team/image/manifests/"+digest, manifestType, manifest, http.StatusCreated)
				index, err = json.Marshal(
					map[string]any{
						"schemaVersion": 2,
						"mediaType":     indexType,
						"manifests": []any{
							map[string]any{
								"mediaType": manifestType,
								"digest":    digest,
								"size":      len(manifest),
								"platform":  map[string]string{"os": "linux", "architecture": "amd64"},
							},
						},
					},
				)
				if err != nil {
					t.Fatal(err)
				}
				nativeRegistryRequest(t, w, w.target, http.MethodPut, "/artifactory/api/docker/target/v2/team/image/manifests/multi", indexType, index, http.StatusCreated)
			}
			upstreamKey := "target"
			if virtual {
				upstreamKey = "target-view"
				body, err := json.Marshal(map[string]any{"rclass": "virtual", "packageType": kind, "repositories": []string{"target"}})
				if err != nil {
					t.Fatal(err)
				}
				w.request(w.target, http.MethodPut, "/artifactory/api/repositories/"+upstreamKey, string(body), http.StatusOK)
			}
			configuration, err := json.Marshal(map[string]any{
				"rclass": "remote", "packageType": kind, "url": w.bridge.URL + "/artifactory/api/docker/" + upstreamKey, "username": "receiver", "password": "pw",
			})
			if err != nil {
				t.Fatal(err)
			}
			w.request(w.source, http.MethodPut, "/artifactory/api/repositories/native-remote", string(configuration), http.StatusOK)
			w.request(w.source, http.MethodPut, replicationPath+"native-remote", `{"enabled":true,"syncProperties":false}`, http.StatusCreated)
			targets, err := w.runtime.Store.Replications().ForRepo(t.Context(), "native-remote")
			if err != nil || len(targets) != 1 {
				t.Fatal("native pull configuration", err)
			}
			principal, err := w.runtime.Auth.Basic(t.Context(), "admin", "pw")
			if err != nil {
				t.Fatal(err)
			}
			pull, err := w.runtime.Federation.Puller()
			if err != nil {
				t.Fatal(err)
			}
			run, err := pull.Start(accessapi.WithCaller(t.Context(), principal, nil), replication.PullRequest{
				ID: "native-cache-pull", TargetKey: targets[0].Key, TargetRevision: targets[0].Revision, Root: "team/image",
			}, nil)
			if err != nil {
				t.Fatal("native pull admission", err)
			}
			for step := 0; step < 40 && !run.Phase.Terminal(); step++ {
				run, err = pull.Step(t.Context(), run.Reference, w.fence)
				if err != nil {
					t.Fatal("native pull step", err)
				}
			}
			items, _, err := w.runtime.Store.PullProgress().Items(t.Context(), run.Reference, false, kernel.Page{Limit: 20})
			if err != nil || run.Phase != model.PullComplete || len(items) == 0 {
				t.Fatal("native pull did not settle retained progress", err)
			}
			for _, item := range items {
				if item.Outcome != model.PullSucceeded || item.Cache == nil {
					t.Fatal("native selected file has no verified cache receipt", item.Path, item.Outcome, item.Reason)
				}
			}
			manifestRoute := "/artifactory/api/docker/" + upstreamKey + "/v2/team/image/manifests/" + tag
			layerRoute := "/artifactory/api/docker/" + upstreamKey + "/v2/team/image/blobs/" + layerDigest
			beforeManifest, beforeLayer := w.bridge.count(http.MethodGet, manifestRoute), w.bridge.count(http.MethodGet, layerRoute)
			warm, err := pull.Start(
				accessapi.WithCaller(t.Context(), principal, nil),
				replication.PullRequest{ID: "native-cache-warm", TargetKey: targets[0].Key, TargetRevision: targets[0].Revision, Root: "team/image"},
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			for step := 0; step < 40 && !warm.Phase.Terminal(); step++ {
				warm, err = pull.Step(t.Context(), warm.Reference, w.fence)
				if err != nil {
					t.Fatal(err)
				}
			}
			if warm.Phase != model.PullComplete || w.bridge.count(http.MethodGet, manifestRoute) != beforeManifest || w.bridge.count(http.MethodGet, layerRoute) != beforeLayer {
				t.Fatal("warm native pull refetched payloads or failed to settle")
			}
			warmItems, continuation, err := w.runtime.Store.PullProgress().Items(t.Context(), warm.Reference, false, kernel.Page{Limit: 20})
			if err != nil || continuation != "" || len(warmItems) != len(items) {
				t.Fatal("warm native pull lost selected intents", err)
			}
			for index, item := range warmItems {
				if item.Path != items[index].Path || item.Outcome != model.PullSucceeded || item.Cache == nil {
					t.Fatal("warm native selection has no successful verified cache receipt", item.Path, item.Outcome, item.Reason)
				}
			}
			if index != nil && !virtual {
				ctx := accessapi.WithCaller(t.Context(), principal, nil)
				source := w.runtime.Artifacts.API().PullSource
				candidates, _, err := source.CachedItems(ctx, run.Binding.Source, run.Binding.Selection(), kernel.Page{Limit: 30})
				if err != nil {
					t.Fatal(err)
				}
				var candidate artifactsapi.PullCacheCandidate
				for _, entry := range candidates {
					if entry.Path == "team/image/multi/manifest.json" {
						candidate = entry
					}
				}
				if candidate.Version == "" {
					t.Fatal("native index has no normal cache reconciliation identity")
				}
				publication := artifactsapi.PullPublication{Check: func(context.Context) error { return nil }, Fence: w.fence}
				// The storage spelling manifest.json is absent; list.manifest.json
				// still serves the native tag and must protect the cached index.
				_, err = source.Remove(ctx, candidate, publication, func(context.Context, []string) error {
					t.Fatal("live native index deletion reached publication")
					return nil
				})
				if !errors.Is(err, artifactsapi.ErrPullChanged) {
					t.Fatal("storage alias miss was accepted as native absence", err)
				}
				for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusBadGateway, http.StatusMethodNotAllowed} {
					w.bridge.intercept(func(out http.ResponseWriter, request *http.Request) bool {
						if request.Method == http.MethodHead && strings.HasSuffix(request.URL.Path, "/manifests/multi") {
							out.WriteHeader(status)
							return true
						}
						return false
					})
					_, err = source.Remove(ctx, candidate, publication, func(context.Context, []string) error { t.Fatal("failed native probe published deletion"); return nil })
					if err == nil {
						t.Fatal("failed native absence probe authorized deletion", status)
					}
				}
				w.bridge.intercept(nil)
			}
			w.request(w.source, http.MethodPost, "/artifactory/api/repositories/native-remote", `{"offline":true}`, http.StatusOK)
			var forbiddenNetwork atomic.Int64
			w.bridge.intercept(func(out http.ResponseWriter, _ *http.Request) bool {
				forbiddenNetwork.Add(1)
				out.WriteHeader(http.StatusBadGateway)
				return true
			})
			key := "native-remote"
			actual, _ := nativeRegistryRequest(t, w, w.source, http.MethodGet, "/artifactory/api/docker/"+key+"/v2/team/image/manifests/"+tag, manifestType, nil, http.StatusOK)
			if !bytes.Equal(actual, manifest) {
				t.Fatal("offline native read did not reuse verified manifest bytes")
			}
			actual, _ = nativeRegistryRequest(
				t,
				w,
				w.source,
				http.MethodGet,
				"/artifactory/api/docker/"+key+"/v2/team/image/blobs/"+layerDigest,
				"application/octet-stream",
				nil,
				http.StatusOK,
			)
			if !bytes.Equal(actual, layer) {
				t.Fatal("native layer read did not reuse verified pull bytes")
			}
			if index != nil {
				actual, _ := nativeRegistryRequest(
					t,
					w,
					w.source,
					http.MethodGet,
					"/artifactory/api/docker/native-remote/v2/team/image/manifests/multi",
					indexType,
					nil,
					http.StatusOK,
				)
				if !bytes.Equal(actual, index) {
					t.Fatal("offline native index lost its validated descriptor graph")
				}
			}
			// Cache aliases use the ordinary storage route. They are not native
			// registry repository names and must never create a second identity.
			for path, want := range map[string][]byte{
				"team/image/" + tag + "/manifest.json":                                      manifest,
				"team/image/_uploads/sha256__" + strings.TrimPrefix(layerDigest, "sha256:"): layer,
			} {
				actual, _ := w.request(w.source, http.MethodGet, itemRoute("native-remote-cache", path), "", http.StatusOK)
				if !bytes.Equal(actual, want) {
					t.Fatal("cache alias did not return verified native storage bytes")
				}
			}
			if forbiddenNetwork.Load() != 0 {
				t.Fatal("offline/alias cache reads contacted upstream")
			}
		})
	}
}
