package pypi_test

import (
	"crypto/sha1" //nolint:gosec // the file's SHA-1, as a client sends it back
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// TestAccPypiRemoteFileIgnoresIfMatchAndIfUnmodifiedSince: a remote
// wheel, on a remote that stores it and one that does not, answers
// If-Match and If-Unmodified-Since with the whole file on the first
// download and again, whether the upstream's stream or the cached copy
// serves it, GET and HEAD alike. 7.x's download reads neither header
// (DownloadServiceImpl.isNotModified, 7.161.26; RTFACT-17259 closed
// without them), and the PyPI route's stream answered 412 where the cached
// copy answered 200 (#1468).
func TestAccPypiRemoteFileIgnoresIfMatchAndIfUnmodifiedSince(t *testing.T) {
	modified := time.Unix(1700000000, 0).UTC()
	before := modified.Add(-time.Hour).Format(http.TimeFormat)
	for _, store := range []bool{true, false} {
		t.Run("store="+strconv.FormatBool(store), func(t *testing.T) {
			w := harness.NewRemoteWorld(t)
			w.Repo = *repo.Defaults(&repo.Config{
				Key: "remote", Class: repo.Remote, PackageType: repo.PyPI, URL: w.Upstream.URL,
				PyPIRegistryURL: w.Upstream.URL, StoreArtifactsLocally: new(store),
			})
			w.Update()
			cases := []struct {
				label   string
				headers func(sha string) []string
			}{
				{
					"If-Match other",
					func(string) []string { return []string{"If-Match", `"other"`} },
				},
				{"If-Match bare", func(sha string) []string { return []string{"If-Match", sha} }},
				{
					"If-Match quoted",
					func(sha string) []string { return []string{"If-Match", `"` + sha + `"`} },
				},
				{
					"If-Unmodified-Since before",
					func(string) []string { return []string{"If-Unmodified-Since", before} },
				},
			}
			version := func(i int, method string) string {
				return "1." + strconv.Itoa(i) + map[string]string{"GET": "0", "HEAD": "1"}[method]
			}
			files := []map[string]any{}
			wheels := map[string][]byte{}
			for i := range cases {
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					v := version(i, method)
					data := pypiWheel(t, "demo", v)
					sum := sha256.Sum256(data)
					name := "demo-" + v + "-py3-none-any.whl"
					w.Upstream.Set("/files/"+name, fakeupstream.Resource{
						Body: data, Modified: modified,
						Header: http.Header{"Content-Type": {"application/octet-stream"}},
					})
					files = append(files, map[string]any{
						"filename": name, "url": "/files/" + name,
						"hashes": map[string]string{"sha256": hex.EncodeToString(sum[:])},
					})
					wheels[v] = data
				}
			}
			doc, _ := json.Marshal(map[string]any{
				"meta": map[string]string{"api-version": "1.1"}, "name": "demo", "files": files,
			})
			w.Upstream.Set("/simple/demo/", fakeupstream.Resource{
				Body:   doc,
				Header: http.Header{"Content-Type": {"application/vnd.pypi.simple.v1+json"}},
			})
			harness.WantStatus(
				t,
				w.Request(http.MethodGet, "/artifactory/api/pypi/remote/simple/demo/",
					"reader", "", "Accept", "application/vnd.pypi.simple.v1+json"),
				http.StatusOK,
			)
			for i, c := range cases {
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					v := version(i, method)
					data := wheels[v]
					sum := sha1.Sum(data) //nolint:gosec // the file's SHA-1
					want := ""
					if method == http.MethodGet {
						want = string(data)
					}
					target := "/artifactory/api/pypi/remote/demo/" + v + "/demo-" + v + "-py3-none-any.whl"
					for _, pass := range []string{"first download", "again"} {
						rec := w.Request(
							method,
							target,
							"reader",
							"",
							c.headers(hex.EncodeToString(sum[:]))...)
						if rec.Code != http.StatusOK || rec.Body.String() != want ||
							rec.Header().Get("Content-Length") != strconv.Itoa(len(data)) {
							t.Errorf(
								"%s %s %s: %d %q, want 200 and %d bytes",
								pass,
								method,
								c.label,
								rec.Code,
								rec.Header().Get("Content-Length"),
								len(data),
							)
						}
					}
				}
			}
		})
	}
}
