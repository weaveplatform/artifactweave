package docker_test

import (
	"fmt"
	"maps"
	"net/http"
	"strconv"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
)

// pullCondition is a conditional request on a pull and whether it answers
// 304; any other answers as the plain request does.
type pullCondition struct {
	label       string
	headers     []string
	notModified bool
}

// pullConditions are the conditional requests a pull of content whose ETag
// is etag and whose bare form is bare is sent. The first six answered 412
// before #1437; the rest answered as they do now. A pull answers only
// If-None-Match and If-Modified-Since, as 7.x's download does: 6.20's
// DownloadServiceImpl.isNotModified, RTFACT-26059 for a manifest, and
// RTFACT-17259, deferred, for If-Match and If-Unmodified-Since.
func pullConditions(etag, bare string) []pullCondition {
	return []pullCondition{
		{"If-Match bare", []string{"If-Match", bare}, false},
		{"If-Match other", []string{"If-Match", `"other"`}, false},
		{"If-Unmodified-Since before", []string{"If-Unmodified-Since", beforeChange}, false},
		{"If-Match other If-None-Match", []string{
			"If-Match", `"other"`, "If-None-Match", etag,
		}, true},
		{"If-Unmodified-Since before If-Modified-Since after", []string{
			"If-Unmodified-Since", beforeChange, "If-Modified-Since", afterChange,
		}, true},
		{"If-Match other Range", []string{"If-Match", `"other"`, "Range", "bytes=1-3"}, false},
		// Kept.
		{"plain", nil, false},
		{"If-Match quoted", []string{"If-Match", etag}, false},
		{"If-Match *", []string{"If-Match", "*"}, false},
		{"If-Unmodified-Since after", []string{"If-Unmodified-Since", afterChange}, false},
		{"If-None-Match", []string{"If-None-Match", etag}, true},
		{"If-None-Match other", []string{"If-None-Match", `"other"`}, false},
		{"If-Modified-Since after", []string{"If-Modified-Since", afterChange}, true},
		{"If-Modified-Since before", []string{"If-Modified-Since", beforeChange}, false},
	}
}

// changedConditions are the pullConditions that answered 412 before #1437.
func changedConditions(etag, bare string) []pullCondition {
	return pullConditions(etag, bare)[:6]
}

// A local manifest, by tag and by digest, on /v2/ and on
// /artifactory/api/docker/<repo>/v2/, GET and HEAD, ignores If-Match and
// If-Unmodified-Since, as a local blob does (#1314): it answers as the
// plain request, or 304 when If-None-Match or If-Modified-Since says so. A
// redirecting repository answers those 304s itself and redirects the rest
// of its GETs; its HEAD is answered locally, as ever (#1437).
func TestAccRegistryManifestIgnoresIfMatchAndIfUnmodifiedSince(t *testing.T) {
	w := newItemWorld(t, false)
	ctx := t.Context()
	for _, c := range []repo.Config{
		{Key: "docker-local", Class: repo.Local, PackageType: repo.Docker},
		{
			Key: "docker-redirect", Class: repo.Local, PackageType: repo.Docker,
			DownloadRedirect: repo.Flex(true),
		},
	} {
		if err := w.db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	body := registryManifest("preconditions")
	sum := sumsOf(body)
	digest := "sha256:" + sum.sha256
	for _, key := range []string{"docker-local", "docker-redirect"} {
		for _, b := range []string{registryConfig, "registry layer"} {
			wantStatus(t, w.do(http.MethodPost, "/v2/"+key+"/app/blobs/uploads/?digest="+
				sha256Digest(b), "admin", b), http.StatusCreated)
		}
		wantStatus(t, w.do(http.MethodPut, "/v2/"+key+"/app/manifests/v1", "admin", body,
			"Content-Type", layout.MediaTypeOCIManifest), http.StatusCreated)
	}
	stored := map[string]string{
		"ETag": sum.sha1, "X-Checksum-Sha1": sum.sha1, "Docker-Content-Digest": digest,
		"Content-Type": layout.MediaTypeOCIManifest, "Content-Length": strconv.Itoa(len(body)),
		"Content-Range": "", "Location": "",
	}
	notModified := map[string]string{
		"ETag": sum.sha1, "Docker-Content-Digest": digest, "Content-Length": "", "Location": "",
	}
	redirected := map[string]string{
		"ETag": "", "X-Checksum-Sha1": sum.sha1, "Docker-Content-Digest": digest,
	}
	for _, key := range []string{"docker-local", "docker-redirect"} {
		for _, base := range []string{
			"/v2/" + key + "/app/manifests/",
			"/artifactory/api/docker/" + key + "/v2/app/manifests/",
		} {
			for _, target := range []string{base + "v1", base + digest} {
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					for _, c := range pullConditions(`"`+sum.sha1+`"`, sum.sha1) {
						label := method + " " + target + " " + c.label
						got := w.do(method, target, "admin", "", c.headers...)
						switch {
						case c.notModified:
							wantAnswer(t, label, got, answer{304, "", notModified})
						case method == http.MethodHead:
							wantAnswer(t, label, got, answer{200, "", stored})
						case key == "docker-redirect":
							wantAnswer(t, label, got, answer{302, "", redirected})
							if got.Header().Get("Location") == "" {
								t.Fatalf("%s: no redirect: %v", label, got.Header())
							}
						default:
							wantAnswer(t, label, got, answer{200, body, stored})
						}
					}
				}
			}
		}
	}
}

// A remote repository's blob and manifest, on /v2/ and on
// /artifactory/api/docker/<repo>/v2/, on the first fetch and again, stored
// or not, GET and HEAD, ignore If-Match and If-Unmodified-Since as a local
// blob does (#1314, #1437). A blob's ETag is its bare SHA-1 and a
// manifest's its quoted digest (#1317).
func TestAccRegistryRemoteIgnoresIfMatchAndIfUnmodifiedSince(t *testing.T) {
	for _, store := range []bool{true, false} {
		t.Run("store="+strconv.FormatBool(store), func(t *testing.T) {
			w := registryWorld(t)
			if !store {
				w.c.StoreArtifactsLocally = new(false)
				w.update()
			}
			n := 0
			for _, base := range []string{
				"/v2/remote/team/image/",
				"/artifactory/api/docker/remote/v2/team/image/",
			} {
				for _, kind := range []string{"blobs", "manifests"} {
					for _, method := range []string{http.MethodGet, http.MethodHead} {
						// The first fetch of a new image, once per condition.
						for i := range len(changedConditions("", "")) {
							n++
							manifest, layer := registryImage(t, w, fmt.Sprintf("first-%d", n))
							p := remotePullOf(base, kind, manifest, layer)
							c := changedConditions(p.etag, p.bare)[i]
							wantAnswer(t, method+" first fetch "+p.target+" "+c.label,
								w.request(method, p.target, "reader", "", c.headers...),
								p.answer(method, c))
						}
					}
				}
				// Again, once the first fetch is over.
				n++
				manifest, layer := registryImage(t, w, fmt.Sprintf("again-%d", n))
				for _, kind := range []string{"blobs", "manifests"} {
					p := remotePullOf(base, kind, manifest, layer)
					wantAnswer(
						t,
						"GET "+p.target,
						w.request(http.MethodGet, p.target, "reader", ""),
						p.answer(http.MethodGet, pullCondition{}),
					)
					for _, method := range []string{http.MethodGet, http.MethodHead} {
						for _, c := range pullConditions(p.etag, p.bare) {
							wantAnswer(t, method+" again "+p.target+" "+c.label,
								w.request(method, p.target, "reader", "", c.headers...),
								p.answer(method, c))
						}
					}
				}
			}
		})
	}
}

// remotePull is a pull of a remote image's layer or manifest: its target,
// the ETag it answers with, and the quoted and bare forms a client sends.
type remotePull struct {
	target, etag, bare string
	content            []byte
	headers            map[string]string
}

// remotePullOf is the pull of kind ("blobs" or "manifests") from base of
// the image of manifest and layer.
func remotePullOf(base, kind string, manifest, layer []byte) remotePull {
	if kind == "manifests" {
		digest := registryDigest(manifest)
		return remotePull{
			target: base + kind + "/" + digest, etag: `"` + digest + `"`, bare: digest,
			content: manifest, headers: map[string]string{
				"ETag": `"` + digest + `"`, "Content-Type": layout.MediaTypeOCIManifest,
				"X-Checksum-Sha1": sumsOf(string(manifest)).sha1,
			},
		}
	}
	sha1 := sumsOf(string(layer)).sha1
	return remotePull{
		target: base + kind + "/" + registryDigest(layer), etag: `"` + sha1 + `"`, bare: sha1,
		content: layer, headers: map[string]string{
			"ETag": sha1, "Content-Type": "application/octet-stream", "X-Checksum-Sha1": sha1,
		},
	}
}

// answer is what the pull answers to method with c.
func (p remotePull) answer(method string, c pullCondition) answer {
	digest := registryDigest(p.content)
	if c.notModified {
		return answer{304, "", map[string]string{
			"ETag": p.headers["ETag"], "Docker-Content-Digest": digest, "Content-Length": "",
		}}
	}
	headers := map[string]string{
		"Docker-Content-Digest": digest, "Content-Length": strconv.Itoa(len(p.content)),
		"Content-Range": "",
	}
	maps.Copy(headers, p.headers)
	if method == http.MethodHead {
		return answer{200, "", headers}
	}
	return answer{200, string(p.content), headers}
}
