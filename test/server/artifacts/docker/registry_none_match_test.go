package docker_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// noneMatchRow is a conditional pull: its header lines, in order, and
// whether 7.x's download finds the file unchanged.
type noneMatchRow struct {
	label       string
	lines       [][2]string
	notModified bool
}

// noneMatchRows are the conditional requests a pull of a file whose SHA-1
// is sha1, MD5 md5 and digest digest is sent. The first four answered the
// other way before #1483. 7.x's DownloadServiceImpl.isNotModified reads each
// If-None-Match line on its own, less a W/ prefix and every quote, and
// compares it with the SHA-1, or the MD5 when X-Artifactory-If-None-Match-MD5
// is true; If-Modified-Since counts only without If-None-Match (7.161.26).
func noneMatchRows(sha1, md5, digest string) []noneMatchRow {
	return []noneMatchRow{
		{"If-None-Match *", [][2]string{{"If-None-Match", "*"}}, false},
		{"If-None-Match list", [][2]string{{"If-None-Match", `"other", ` + sha1}}, false},
		{"If-None-Match second line", [][2]string{
			{"If-None-Match", `"other"`}, {"If-None-Match", sha1},
		}, true},
		{"If-None-Match MD5", [][2]string{
			{"If-None-Match", md5}, {"X-Artifactory-If-None-Match-MD5", "true"},
		}, true},
		// Kept.
		{"plain", nil, false},
		{"If-None-Match MD5 without the flag", [][2]string{{"If-None-Match", md5}}, false},
		{"If-None-Match bare", [][2]string{{"If-None-Match", sha1}}, true},
		{"If-None-Match quoted", [][2]string{{"If-None-Match", `"` + sha1 + `"`}}, true},
		{"If-None-Match weak", [][2]string{{"If-None-Match", "W/" + sha1}}, true},
		{"If-None-Match digest", [][2]string{{"If-None-Match", `"` + digest + `"`}}, false},
		{"If-Modified-Since after", [][2]string{{"If-Modified-Since", afterChange}}, true},
		{"If-Modified-Since before", [][2]string{{"If-Modified-Since", beforeChange}}, false},
		{"If-None-Match other If-Modified-Since after", [][2]string{
			{"If-None-Match", `"other"`}, {"If-Modified-Since", afterChange},
		}, false},
	}
}

// doLines sends a request as admin with lines as its header lines, a name
// that comes twice on two lines.
func (w *itemWorld) doLines(method, target string, lines [][2]string) *httptest.ResponseRecorder {
	w.t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range support.Basic("admin", "pw") {
		req.Header.Set(k, v)
	}
	for _, l := range lines {
		req.Header.Add(l[0], l[1])
	}
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, req)
	return rec
}

// A local manifest, by tag and by digest, on /v2/ and on
// /artifactory/api/docker/<repo>/v2/, GET and HEAD, answers If-None-Match
// and If-Modified-Since as 7.x's download answers them, as a local blob and
// a remote blob or manifest do since #1469 (#1483). A redirecting
// repository decides them before it signs a URL, and redirects a GET they
// do not answer.
func TestAccRegistryManifestNoneMatchAsTheDownload(t *testing.T) {
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
	body := registryManifest("none match")
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
		"Cache-Control": "no-store", "Location": "",
	}
	notModified := map[string]string{
		"ETag": sum.sha1, "X-Checksum-Sha1": sum.sha1, "Docker-Content-Digest": digest,
		"Cache-Control": "no-store", "X-Artifactory-Filename": layout.ManifestFile,
		"Content-Type": "", "Content-Length": "", "Last-Modified": "", "Location": "",
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
					for _, c := range noneMatchRows(sum.sha1, sum.md5, digest) {
						label := method + " " + target + " " + c.label
						got := w.doLines(method, target, c.lines)
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
