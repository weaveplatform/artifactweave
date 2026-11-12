package docker_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

// dockerDisposition is the Content-Disposition 7.x's Docker API sends: the
// quoted name alone, with no filename* part (RTFACT-29977, RTFACT-30157,
// RTFACT-27199).
func dockerDisposition(name string) string { return `attachment; filename="` + name + `"` }

// indexOf is an OCI image index listing the manifest body for linux/amd64.
func indexOf(body string) string {
	return `{"schemaVersion":2,"mediaType":"` + layout.MediaTypeOCIIndex + `","manifests":[{` +
		`"mediaType":"` + layout.MediaTypeOCIManifest + `","digest":"` + sha256Digest(body) +
		`","size":` + strconv.Itoa(len(body)) + `,"platform":{"os":"linux","architecture":"amd64"}}]}`
}

// dockerLocalWorld is an item world with docker-local and docker-redirect
// (download redirect on), each holding the image app with registryLayer, a
// layer and the manifest body under the tag v1 and an index of it under
// multi.
func dockerLocalWorld(t *testing.T, layer, body string) *itemWorld {
	t.Helper()
	w := newItemWorld(t, false)
	for _, c := range []repo.Config{
		{Key: "docker-local", Class: repo.Local, PackageType: repo.Docker},
		{
			Key: "docker-redirect", Class: repo.Local, PackageType: repo.Docker,
			DownloadRedirect: repo.Flex(true),
		},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
		for _, b := range []string{layer, registryConfig, "registry layer"} {
			wantStatus(t, w.do(http.MethodPost, "/v2/"+c.Key+"/app/blobs/uploads/?digest="+
				sha256Digest(b), "admin", b), http.StatusCreated)
		}
		wantStatus(t, w.do(http.MethodPut, "/v2/"+c.Key+"/app/manifests/v1", "admin", body,
			"Content-Type", layout.MediaTypeOCIManifest), http.StatusCreated)
		wantStatus(t, w.do(http.MethodPut, "/v2/"+c.Key+"/app/manifests/multi", "admin",
			indexOf(body), "Content-Type", layout.MediaTypeOCIIndex), http.StatusCreated)
	}
	return w
}

// A local manifest, by tag and by digest, GET and HEAD, on both routes,
// carries the file headers 7.x sends on one: its checksums, its file name in
// X-Artifactory-Filename and a Content-Disposition with the quoted name
// alone, and Cache-Control: no-store, as 7.5.7 (RTFACT-23106) and 7.33.12
// (RTFACT-27199) answered. An index is list.manifest.json, the file it is
// stored as. A redirecting repository answers a HEAD the same way, and its
// 302 keeps the checksums and the name, as a blob's 302 does (#1336).
func TestAccRegistryManifestSendsFileHeaders(t *testing.T) {
	body := registryManifest("file headers")
	index := indexOf(body)
	w := dockerLocalWorld(t, "a layer", body)
	for _, m := range []struct {
		body, name, mediaType, tag string
	}{
		{body, layout.ManifestFile, layout.MediaTypeOCIManifest, "v1"},
		{index, layout.ListManifestFile, layout.MediaTypeOCIIndex, "multi"},
	} {
		sum := sumsOf(m.body)
		digest := "sha256:" + sum.sha256
		for _, key := range []string{"docker-local", "docker-redirect"} {
			file := map[string]string{
				"Content-Type":                  m.mediaType,
				"Content-Length":                strconv.Itoa(len(m.body)),
				"Docker-Content-Digest":         digest,
				"ETag":                          sum.sha1,
				"X-Checksum-Sha1":               sum.sha1,
				"X-Checksum-Md5":                sum.md5,
				"X-Checksum-Sha256":             sum.sha256,
				"X-Artifactory-Filename":        m.name,
				"Content-Disposition":           dockerDisposition(m.name),
				"Cache-Control":                 "no-store",
				"X-Artifactory-Docker-Registry": key,
				"Accept-Ranges":                 "bytes",
				"Content-Range":                 "",
			}
			for _, base := range []string{
				"/v2/" + key + "/app/manifests/",
				"/artifactory/api/docker/" + key + "/v2/app/manifests/",
			} {
				for _, target := range []string{base + m.tag, base + digest} {
					wantAnswer(t, "HEAD "+target, w.do(http.MethodHead, target, "admin", ""),
						answer{200, "", file})
					if key == "docker-redirect" {
						continue
					}
					wantAnswer(t, "GET "+target, w.do(http.MethodGet, target, "admin", ""),
						answer{200, m.body, file})
					// A 304 keeps the headers that name the file.
					wantAnswer(t, "GET If-None-Match "+target, w.do(http.MethodGet, target,
						"admin", "", "If-None-Match", sum.sha1), answer{
						304,
						"",
						map[string]string{
							"ETag":                   sum.sha1,
							"X-Checksum-Sha1":        sum.sha1,
							"X-Artifactory-Filename": m.name,
							"Content-Disposition":    dockerDisposition(m.name),
							"Cache-Control":          "no-store",
							"Content-Length":         "",
						},
					})
				}
			}
		}
		target := "/v2/docker-redirect/app/manifests/" + m.tag
		got := w.do(http.MethodGet, target, "admin", "")
		wantAnswer(t, "redirected GET "+target, got, answer{302, "", map[string]string{
			"X-Checksum-Sha1": sum.sha1, "X-Checksum-Md5": sum.md5, "X-Checksum-Sha256": sum.sha256,
			"X-Artifactory-Filename": m.name, "Content-Disposition": "", "ETag": "",
			"Cache-Control": "private, no-store",
		}})
		if got.Header().Get("Location") == "" {
			t.Fatalf("no redirect: %v", got.Header())
		}
	}
}

// A blob's Content-Disposition on the Docker API is the quoted name alone,
// as 7.55.8 and 7.71.4 sent it (RTFACT-29977, RTFACT-30157), and a blob
// carries no X-Artifactory-Docker-Registry, which no 7.x blob answer shows.
// The item route on the same file keeps the filename* part, as 7.x sends it
// on a Maven download from the same server (#1336).
func TestAccRegistryBlobDispositionIsTheQuotedName(t *testing.T) {
	const layer = "a layer named on download"
	w := dockerLocalWorld(t, layer, registryManifest("disposition"))
	sum := sumsOf(layer)
	digest := "sha256:" + sum.sha256
	name := layout.BlobFile(sum.sha256)
	for _, key := range []string{"docker-local", "docker-redirect"} {
		for _, base := range []string{
			"/v2/" + key + "/app/blobs/",
			"/artifactory/api/docker/" + key + "/v2/app/blobs/",
		} {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				if key == "docker-redirect" && method == http.MethodGet {
					continue
				}
				body := layer
				if method == http.MethodHead {
					body = ""
				}
				wantAnswer(t, method+" "+base, w.do(method, base+digest, "admin", ""),
					answer{200, body, map[string]string{
						"Content-Disposition":           dockerDisposition(name),
						"X-Artifactory-Filename":        name,
						"X-Artifactory-Docker-Registry": "",
						"Cache-Control":                 "",
					}})
			}
		}
		item := "/artifactory/" + key + "/app/_uploads/" + name
		wantAnswer(t, "item route "+item, w.do(http.MethodGet, item, "admin", "",
			"X-JFrog-Download-Redirect-To", "None"), answer{200, layer, map[string]string{
			"Content-Disposition": blob.Disposition(name),
		}})
	}
}

// A remote manifest and a remote blob, stored or not, on the first fetch and
// from the cache, GET and HEAD, on both routes, carry a local one's file
// headers: the blob X-Artifactory-Filename: sha256__<hex> and the quoted
// Content-Disposition, without X-Artifactory-Docker-Registry, as 7.55.8
// answered a remote blob GET (RTFACT-29977); the manifest its checksums, as
// 6.11.3 sent them (RTFACT-19842), and manifest.json, or list.manifest.json
// for an index, with no-store and the registry's key [I] (#1336).
func TestAccRegistryRemoteSendsFileHeaders(t *testing.T) {
	for _, store := range []bool{true, false} {
		t.Run("store="+strconv.FormatBool(store), func(t *testing.T) {
			w := registryWorld(t)
			if !store {
				w.c.StoreArtifactsLocally = new(false)
				w.update()
			}
			for i, base := range []string{
				"/v2/remote/team/image/",
				"/artifactory/api/docker/remote/v2/team/image/",
			} {
				manifest, layer := registryImage(t, w, "headers "+base[:4])
				index := []byte(indexOf(string(manifest)))
				// A tag of its own on each route, as a tag cached from the
				// first would answer the second.
				tag := "t" + strconv.Itoa(i)
				registryPut(w, "manifests", registryDigest(index), layout.MediaTypeOCIIndex, index)
				registryPut(w, "manifests", tag, layout.MediaTypeOCIManifest, manifest)
				blobSum := sumsOf(string(layer))
				for _, c := range []struct {
					path, body string
					headers    map[string]string
				}{
					{base + "blobs/" + registryDigest(layer), string(layer), map[string]string{
						"X-Artifactory-Filename":        layout.BlobFile(blobSum.sha256),
						"Content-Disposition":           dockerDisposition(layout.BlobFile(blobSum.sha256)),
						"X-Artifactory-Docker-Registry": "",
						"Cache-Control":                 "",
						"X-Checksum-Sha1":               blobSum.sha1,
						"ETag":                          blobSum.sha1,
					}},
					{base + "manifests/" + registryDigest(manifest), string(manifest), manifestHeaders(manifest, layout.ManifestFile)},
					{base + "manifests/" + tag, string(manifest), manifestHeaders(manifest, layout.ManifestFile)},
					{base + "manifests/" + registryDigest(index), string(index), manifestHeaders(index, layout.ListManifestFile)},
				} {
					for _, pass := range []string{"first fetch", "cache"} {
						for _, method := range []string{http.MethodHead, http.MethodGet} {
							body := c.body
							if method == http.MethodHead {
								body = ""
							}
							wantAnswer(
								t,
								pass+" "+method+" "+c.path,
								w.request(
									method,
									c.path,
									"reader",
									"",
								),
								answer{200, body, c.headers},
							)
						}
					}
				}
			}
		})
	}
}

// manifestHeaders are the file headers of a remote manifest body stored as
// name.
func manifestHeaders(body []byte, name string) map[string]string {
	sum := sumsOf(string(body))
	return map[string]string{
		"X-Artifactory-Filename":        name,
		"Content-Disposition":           dockerDisposition(name),
		"Cache-Control":                 "no-store",
		"X-Artifactory-Docker-Registry": "remote",
		"X-Checksum-Sha1":               sum.sha1,
		"X-Checksum-Md5":                sum.md5,
		"X-Checksum-Sha256":             sum.sha256,
		"Docker-Content-Digest":         "sha256:" + sum.sha256,
	}
}

// rangeCases are the Range headers a GET or HEAD sends below: a range, the
// same naming the file's SHA-1 in If-Range bare and quoted, one past the
// end, and a suffix.
func rangeCases(sha1 string) [][]string {
	return [][]string{
		{"Range", "bytes=0-3"},
		{"Range", "bytes=2-5"},
		{"Range", "bytes=0-3", "If-Range", sha1},
		{"Range", "bytes=0-3", "If-Range", `"` + sha1 + `"`},
		{"Range", "bytes=1000-2000"},
		{"Range", "bytes=-2"},
	}
}

// wholeAnswer is the answer to a method that serves body whole.
func wholeAnswer(method, body string) answer {
	if method == http.MethodHead {
		return answer{
			200,
			"",
			map[string]string{
				"Content-Length": strconv.Itoa(
					len(body),
				),
				"Content-Range": "",
				"Accept-Ranges": "bytes",
			},
		}
	}
	return answer{200, body, map[string]string{
		"Content-Length": strconv.Itoa(len(body)), "Content-Range": "", "Accept-Ranges": "bytes",
	}}
}

// A blob or manifest GET through the Docker API answers Range with 200 and
// the whole content, still with Accept-Ranges: bytes, as 7.71.4 answered a
// blob GET (RTFACT-30157, open); If-Range changes nothing. A HEAD answers 200
// with the whole length (#1338). The item route on the same blob still
// applies the range, as 7.x's does. A ranged GET is a whole download, so it
// is counted (#1337).
func TestAccRegistryAnswersRangeWithTheWholeContent(t *testing.T) {
	const layer = "a layer asked for in part"
	body := registryManifest("range")
	w := dockerLocalWorld(t, layer, body)
	sum := sumsOf(layer)
	digest := "sha256:" + sum.sha256
	msum := sumsOf(body)
	mdigest := "sha256:" + msum.sha256
	for _, key := range []string{"docker-local", "docker-redirect"} {
		for _, base := range []string{"/v2/" + key + "/app/", "/artifactory/api/docker/" + key + "/v2/app/"} {
			for _, c := range []struct{ path, body, sha1 string }{
				{base + "blobs/" + digest, layer, sum.sha1},
				{base + "manifests/v1", body, msum.sha1},
				{base + "manifests/" + mdigest, body, msum.sha1},
			} {
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					for _, h := range rangeCases(c.sha1) {
						if key == "docker-redirect" && method == http.MethodGet {
							// Streamed rather than redirected, to see the answer.
							h = append(h, "X-JFrog-Download-Redirect-To", "None")
						}
						wantAnswer(t, method+" "+c.path+" "+strings.Join(h, " "),
							w.do(method, c.path, "admin", "", h...), wholeAnswer(method, c.body))
					}
				}
			}
		}
	}
	// The item route on the same blob applies the range.
	item := "/artifactory/docker-local/app/_uploads/" + layout.BlobFile(sum.sha256)
	wantAnswer(t, "item route", w.do(http.MethodGet, item, "admin", "", "Range", "bytes=0-3"),
		answer{206, layer[:4], map[string]string{
			"Content-Range": "bytes 0-3/" + strconv.Itoa(len(layer)), "Content-Length": "4",
		}})
	// A ranged GET that does not start at byte 0 is a whole download, and
	// counted, streamed or redirected.
	for _, key := range []string{"docker-local", "docker-redirect"} {
		b := "a layer counted whole " + key
		d := sha256Digest(b)
		wantStatus(t, w.do(http.MethodPost, "/v2/"+key+"/app/blobs/uploads/?digest="+d, "admin", b),
			http.StatusCreated)
		got := w.do(http.MethodGet, "/v2/"+key+"/app/blobs/"+d, "admin", "", "Range", "bytes=2-5")
		if got.Code != http.StatusOK && got.Code != http.StatusFound {
			t.Fatalf("%s: %d %q", key, got.Code, got.Body)
		}
		it := w.item(key, false, "app/_uploads/"+layout.BlobFile(strings.TrimPrefix(d, "sha256:")))
		if stats, err := w.db.Items().
			Stats(t.Context(), it.ID); err != nil ||
			stats.DownloadCount != 1 {
			t.Fatalf("%s: a ranged GET not counted once: %+v %v", key, stats, err)
		}
	}
}

// A remote blob or manifest, stored or not, on the first fetch and from the
// cache, answers Range with the whole content on GET and HEAD alike
// (#1337, #1338).
func TestAccRegistryRemoteAnswersRangeWithTheWholeContent(t *testing.T) {
	for _, store := range []bool{true, false} {
		t.Run("store="+strconv.FormatBool(store), func(t *testing.T) {
			w := registryWorld(t)
			if !store {
				w.c.StoreArtifactsLocally = new(false)
				w.update()
			}
			for _, base := range []string{
				"/v2/remote/team/image/",
				"/artifactory/api/docker/remote/v2/team/image/",
			} {
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					for i := range rangeCases("") {
						manifest, layer := registryImage(t, w,
							"range "+base[:4]+" "+method+" "+strconv.Itoa(i))
						for _, c := range []struct{ path, body string }{
							{base + "blobs/" + registryDigest(layer), string(layer)},
							{base + "manifests/" + registryDigest(manifest), string(manifest)},
						} {
							h := rangeCases(sumsOf(c.body).sha1)[i]
							for _, pass := range []string{"first fetch", "cache"} {
								wantAnswer(
									t,
									pass+" "+method+" "+c.path+" "+strings.Join(h, " "),
									w.request(
										method,
										c.path,
										"reader",
										"",
										h...),
									wholeAnswer(method, c.body),
								)
							}
						}
					}
				}
			}
		})
	}
}
