package docker_test

import (
	"crypto/md5"  //nolint:gosec // the wire checksum 7.x sends
	"crypto/sha1" //nolint:gosec // the wire checksum and ETag 7.x sends
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
)

// The dates the conditional requests below send: one after every file's
// last change, and one before it.
const (
	afterChange  = "Fri, 01 Jan 2100 00:00:00 GMT"
	beforeChange = "Mon, 01 Jan 2001 00:00:00 GMT"
)

// fileSums are the checksums 7.x sends for a stored file, as bare hex.
type fileSums struct{ sha1, md5, sha256 string }

func sumsOf(body string) fileSums {
	s1 := sha1.Sum([]byte(body)) //nolint:gosec // the wire checksum
	m := md5.Sum([]byte(body))   //nolint:gosec // the wire checksum
	s2 := sha256.Sum256([]byte(body))
	return fileSums{hex.EncodeToString(s1[:]), hex.EncodeToString(m[:]), hex.EncodeToString(s2[:])}
}

// answer is what a test expects of a response: its status, its exact body,
// and headers, where "" means the header is absent.
type answer struct {
	status  int
	body    string
	headers map[string]string
}

func wantAnswer(t *testing.T, label string, rec *httptest.ResponseRecorder, want answer) {
	t.Helper()
	if rec.Code != want.status || rec.Body.String() != want.body {
		t.Fatalf("%s: %d %q, want %d %q", label, rec.Code, rec.Body, want.status, want.body)
	}
	for k, v := range want.headers {
		if got := rec.Header().Get(k); got != v {
			t.Fatalf("%s: %s %q, want %q (%v)", label, k, got, v, rec.Header())
		}
	}
}

// A local blob's HEAD sends the headers its GET sends through the
// download: the bare SHA-1 as ETag, equal to X-Checksum-Sha1, beside
// X-Checksum-Md5 and X-Checksum-Sha256, as 7.x answers a blob GET
// (RTFACT-29977, RTFACT-30157) and a HEAD since RTFACT-29977's fix. A HEAD
// answers If-None-Match, in any form the item route takes, and
// If-Modified-Since with 304, as 6.20's download does for a HEAD, and still
// ignores Range, If-Range, If-Match and If-Unmodified-Since. A HEAD is never
// redirected and never counted (#1317). A list on one line matches
// nothing, as on the item route since #1469.
func TestAccRegistryBlobHeadSendsTheGetHeaders(t *testing.T) {
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
	const layer = "a layer with an etag"
	sum := sumsOf(layer)
	digest := "sha256:" + sum.sha256
	name := "sha256__" + sum.sha256
	for _, key := range []string{"docker-local", "docker-redirect"} {
		wantStatus(t, w.do(http.MethodPost, "/v2/"+key+"/app/blobs/uploads/?digest="+digest,
			"admin", layer), http.StatusCreated)
		it := w.item(key, false, "app/_uploads/"+name)
		file := map[string]string{
			"ETag":                   sum.sha1,
			"X-Checksum-Sha1":        sum.sha1,
			"X-Checksum-Md5":         sum.md5,
			"X-Checksum-Sha256":      sum.sha256,
			"Docker-Content-Digest":  digest,
			"Content-Type":           "application/octet-stream",
			"Content-Length":         strconv.Itoa(len(layer)),
			"Content-Range":          "",
			"Accept-Ranges":          "bytes",
			"Last-Modified":          it.Modified.UTC().Format(http.TimeFormat),
			"X-Artifactory-Filename": name,
			// The quoted name alone, as 7.x's Docker API sends it (#1336).
			"Content-Disposition": `attachment; filename="` + name + `"`,
			"Location":            "",
		}
		notModified := map[string]string{
			"ETag":                  sum.sha1,
			"X-Checksum-Sha1":       sum.sha1,
			"Docker-Content-Digest": digest,
			"Content-Length":        "",
			"Content-Type":          "",
		}
		for _, base := range []string{
			"/v2/" + key + "/app/blobs/",
			"/artifactory/api/docker/" + key + "/v2/app/blobs/",
		} {
			target := base + digest
			for _, c := range []struct {
				label   string
				headers []string
				want    answer
			}{
				{"plain", nil, answer{200, "", file}},
				{"If-None-Match bare", []string{"If-None-Match", sum.sha1}, answer{304, "", notModified}},
				{"If-None-Match quoted", []string{"If-None-Match", `"` + sum.sha1 + `"`}, answer{304, "", notModified}},
				{"If-None-Match weak", []string{"If-None-Match", "W/" + sum.sha1}, answer{304, "", notModified}},
				// 7.x's download compares the line as a whole (#1469).
				{"If-None-Match list", []string{"If-None-Match", `"x", ` + sum.sha1}, answer{200, "", file}},
				{"If-None-Match digest", []string{"If-None-Match", `"` + digest + `"`}, answer{200, "", file}},
				{"If-None-Match other", []string{"If-None-Match", `"other"`}, answer{200, "", file}},
				{"If-Modified-Since after", []string{"If-Modified-Since", afterChange}, answer{304, "", notModified}},
				{"If-Modified-Since before", []string{"If-Modified-Since", beforeChange}, answer{200, "", file}},
				// What 6.20's download leaves unevaluated on a HEAD answers
				// as the HEAD did before: the whole length, never 206 or 412.
				{"Range", []string{"Range", "bytes=1-3"}, answer{200, "", file}},
				{"Range If-Range", []string{"Range", "bytes=1-3", "If-Range", sum.sha1}, answer{200, "", file}},
				{"Range unsatisfiable", []string{"Range", "bytes=1000-2000"}, answer{200, "", file}},
				{"If-Match other", []string{"If-Match", `"other"`}, answer{200, "", file}},
				{"If-Match bare", []string{"If-Match", sum.sha1}, answer{200, "", file}},
				{"If-Unmodified-Since before", []string{"If-Unmodified-Since", beforeChange}, answer{200, "", file}},
			} {
				wantAnswer(t, key+" HEAD "+target+" "+c.label,
					w.do(http.MethodHead, target, "admin", "", c.headers...), c.want)
			}
		}
		if stats, err := w.db.Items().Stats(ctx, it.ID); err != nil || stats.DownloadCount != 0 {
			t.Fatalf("%s: HEAD counted: %+v %v", key, stats, err)
		}
	}
	// The GET it mirrors, and its conditional answers, as they were.
	target := "/v2/docker-local/app/blobs/" + digest
	got := w.do(http.MethodGet, target, "admin", "")
	wantAnswer(t, "GET", got, answer{200, layer, map[string]string{
		"ETag": sum.sha1, "X-Checksum-Sha1": sum.sha1, "X-Checksum-Md5": sum.md5,
		"X-Checksum-Sha256": sum.sha256,
	}})
	wantAnswer(t, "GET If-None-Match", w.do(http.MethodGet, target, "admin", "",
		"If-None-Match", sum.sha1), answer{304, "", map[string]string{"ETag": sum.sha1}})
	// The Docker API answers a range with the whole blob, as 7.71.4 does
	// (RTFACT-30157, #1337).
	wantAnswer(
		t,
		"GET Range",
		w.do(http.MethodGet, target, "admin", "", "Range", "bytes=1-3"),
		answer{
			200,
			layer,
			map[string]string{"Content-Range": "", "Content-Length": strconv.Itoa(len(layer))},
		},
	)
	// A redirecting repository still redirects the GET.
	wantAnswer(t, "redirected GET", w.do(http.MethodGet,
		"/v2/docker-redirect/app/blobs/"+digest, "admin", ""),
		answer{302, "", map[string]string{"ETag": "", "X-Checksum-Sha1": sum.sha1}})
}

// A remote blob's ETag is its bare SHA-1, equal to X-Checksum-Sha1, as
// 7.55.8 answered a remote blob GET (RTFACT-29977), on its first fetch and
// from the cache, stored or not, GET and HEAD alike. If-None-Match takes
// it in the forms the item route takes; the digest, the ETag before, no
// longer matches, nor does a list on one line (#1469). A range gets the
// whole blob whatever If-Range names (#1337). A remote manifest keeps its
// digest as ETag: no capture shows what 7.x sends for one (#1317).
func TestAccRegistryRemoteBlobETagIsTheBareSHA1(t *testing.T) {
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
					_, cold := registryImage(t, w, "cold "+method+" "+base[:4])
					sum := sumsOf(string(cold))
					body := string(cold)
					if method == http.MethodHead {
						body = ""
					}
					wantAnswer(t, method+" first fetch "+base,
						w.request(method, base+"blobs/"+registryDigest(cold), "reader", ""),
						answer{
							200,
							body,
							map[string]string{
								"ETag":                  sum.sha1,
								"X-Checksum-Sha1":       sum.sha1,
								"X-Checksum-Md5":        sum.md5,
								"X-Checksum-Sha256":     sum.sha256,
								"Docker-Content-Digest": registryDigest(cold),
								"Content-Length":        strconv.Itoa(len(cold)),
							},
						})
				}
				manifest, layer := registryImage(t, w, "warm "+base[:4])
				sum := sumsOf(string(layer))
				digest := registryDigest(layer)
				target := base + "blobs/" + digest
				file := map[string]string{
					"ETag": sum.sha1, "X-Checksum-Sha1": sum.sha1, "X-Checksum-Md5": sum.md5,
					"X-Checksum-Sha256": sum.sha256, "Docker-Content-Digest": digest,
					"Content-Type": "application/octet-stream", "Content-Range": "",
				}
				notModified := map[string]string{
					"ETag": sum.sha1, "Docker-Content-Digest": digest, "Content-Length": "",
				}
				wantStatus(t, w.request(http.MethodGet, target, "reader", ""), http.StatusOK)
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					whole := string(layer)
					if method == http.MethodHead {
						whole = ""
					}
					for _, c := range []struct {
						label   string
						headers []string
						want    answer
					}{
						{"plain", nil, answer{200, whole, file}},
						{"If-None-Match bare", []string{"If-None-Match", sum.sha1}, answer{304, "", notModified}},
						{"If-None-Match quoted", []string{"If-None-Match", `"` + sum.sha1 + `"`}, answer{304, "", notModified}},
						{"If-None-Match weak", []string{"If-None-Match", `W/"` + sum.sha1 + `"`}, answer{304, "", notModified}},
						// 7.x's download compares the line as a whole (#1469).
						{"If-None-Match list", []string{"If-None-Match", `"x", ` + sum.sha1}, answer{200, whole, file}},
						{"If-None-Match digest", []string{"If-None-Match", `"` + digest + `"`}, answer{200, whole, file}},
						// The Docker API answers a range with the whole
						// blob (RTFACT-30157, #1337), and a HEAD ignores
						// Range (#1338), whatever If-Range names.
						{"If-Range bare", []string{"Range", "bytes=1-3", "If-Range", sum.sha1}, answer{200, whole, file}},
						{"If-Range quoted", []string{"Range", "bytes=1-3", "If-Range", `"` + sum.sha1 + `"`}, answer{200, whole, file}},
						{"If-Range digest", []string{"Range", "bytes=1-3", "If-Range", `"` + digest + `"`}, answer{200, whole, file}},
					} {
						wantAnswer(t, method+" "+target+" "+c.label,
							w.request(method, target, "reader", "", c.headers...), c.want)
					}
				}
				// The manifest keeps the digest as its ETag, and answers it.
				manifestTarget := base + "manifests/" + registryDigest(manifest)
				wantAnswer(t, "manifest", w.request(http.MethodGet, manifestTarget, "reader", ""),
					answer{200, string(manifest), map[string]string{
						"ETag": `"` + registryDigest(manifest) + `"`,
					}})
				wantAnswer(
					t,
					"manifest If-None-Match",
					w.request(http.MethodGet, manifestTarget,
						"reader", "", "If-None-Match", `"`+registryDigest(manifest)+`"`),
					answer{
						304,
						"",
						map[string]string{"ETag": `"` + registryDigest(manifest) + `"`},
					},
				)
			}
		})
	}
}

// A local manifest's ETag is the manifest file's bare SHA-1, as 7.5.7
// answered a local manifest GET (RTFACT-23106) and 7.33.12 a HEAD
// (RTFACT-27199), on GET and HEAD, by tag and by digest. If-None-Match
// takes it in the forms the item route takes, a redirecting repository
// answers it before it signs a URL, and the digest no longer matches
// (#1317). A range gets the whole manifest whatever If-Range names (#1337).
func TestAccRegistryManifestETagIsTheBareSHA1(t *testing.T) {
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
	body := registryManifest("etag")
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
		"ETag": sum.sha1, "Docker-Content-Digest": digest,
		"Content-Type": layout.MediaTypeOCIManifest, "Content-Length": strconv.Itoa(len(body)),
	}
	notModified := map[string]string{"ETag": sum.sha1, "Docker-Content-Digest": digest}
	for _, base := range []string{
		"/v2/docker-local/app/manifests/",
		"/artifactory/api/docker/docker-local/v2/app/manifests/",
	} {
		for _, target := range []string{base + "v1", base + digest} {
			wantAnswer(t, "GET "+target, w.do(http.MethodGet, target, "admin", ""),
				answer{200, body, stored})
			wantAnswer(t, "HEAD "+target, w.do(http.MethodHead, target, "admin", ""),
				answer{200, "", stored})
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				for _, value := range []string{sum.sha1, `"` + sum.sha1 + `"`, "W/" + sum.sha1} {
					wantAnswer(t, method+" If-None-Match "+value, w.do(method, target, "admin", "",
						"If-None-Match", value), answer{304, "", notModified})
				}
			}
			wantAnswer(t, "GET If-None-Match digest", w.do(http.MethodGet, target, "admin", "",
				"If-None-Match", `"`+digest+`"`), answer{200, body, stored})
			// The Docker API answers a range with the whole manifest, as
			// it does a blob's (RTFACT-30157, #1337).
			wantAnswer(t, "GET If-Range bare", w.do(
				http.MethodGet,
				target,
				"admin",
				"",
				"Range",
				"bytes=1-3",
				"If-Range",
				sum.sha1,
			), answer{200, body, stored})
			wantAnswer(t, "GET If-Range digest", w.do(http.MethodGet, target, "admin", "",
				"Range", "bytes=1-3", "If-Range", `"`+digest+`"`), answer{200, body, stored})
		}
	}
	// A redirecting repository answers the bare SHA-1 itself, and redirects
	// a request naming the digest.
	target := "/v2/docker-redirect/app/manifests/v1"
	wantAnswer(t, "redirected If-None-Match", w.do(http.MethodGet, target, "admin", "",
		"If-None-Match", sum.sha1), answer{304, "", notModified})
	got := w.do(http.MethodGet, target, "admin", "", "If-None-Match", `"`+digest+`"`)
	wantAnswer(
		t,
		"redirected If-None-Match digest",
		got,
		answer{302, "", map[string]string{"ETag": ""}},
	)
	if got.Header().Get("Location") == "" {
		t.Fatalf("no redirect: %v", got.Header())
	}
	wantAnswer(t, "redirected HEAD", w.do(http.MethodHead, target, "admin", ""),
		answer{200, "", stored})
}
