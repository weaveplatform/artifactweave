package pypi_test

import (
	"crypto/sha1" //nolint:gosec // the wheel's SHA-1, as 7.x sends it
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// TestAccPypiRemoteFileETagIsItsSHA1: a remote wheel whose upstream sends
// its own ETag and its X-Checksum-Sha1 answers a GET and a HEAD with that
// SHA-1, bare, as its ETag, beside X-Checksum-Sha1, on the first download
// and again, on a remote that stores it and one that does not; the
// upstream's ETag matches nothing and the SHA-1 answers 304. 7.161.26's
// RequestResponseHelper.setBasicHeaders sets every downloaded resource's
// ETag to its SHA-1; the file route sent and matched the upstream's
// (#1484).
func TestAccPypiRemoteFileETagIsItsSHA1(t *testing.T) {
	for _, store := range []bool{true, false} {
		t.Run("store="+strconv.FormatBool(store), func(t *testing.T) {
			w := harness.NewRemoteWorld(t)
			w.Repo = *repo.Defaults(&repo.Config{
				Key: "remote", Class: repo.Remote, PackageType: repo.PyPI, URL: w.Upstream.URL,
				PyPIRegistryURL: w.Upstream.URL, StoreArtifactsLocally: new(store),
			})
			w.Update()
			methods := map[string]string{"1.0": http.MethodGet, "1.1": http.MethodHead}
			files := []map[string]any{}
			wheels := map[string][]byte{}
			for version := range methods {
				data := pypiWheel(t, "demo", version)
				sum := sha256.Sum256(data)
				sha1sum := sha1.Sum(data) //nolint:gosec // the wheel's SHA-1
				name := "demo-" + version + "-py3-none-any.whl"
				w.Upstream.Set(
					"/files/"+name,
					fakeupstream.Resource{Body: data, Header: http.Header{
						"Content-Type":    {"application/octet-stream"},
						"X-Checksum-Sha1": {hex.EncodeToString(sha1sum[:])},
					}},
				)
				files = append(files, map[string]any{
					"filename": name, "url": "/files/" + name,
					"hashes": map[string]string{"sha256": hex.EncodeToString(sum[:])},
				})
				wheels[version] = data
			}
			doc, _ := json.Marshal(map[string]any{
				"meta": map[string]string{"api-version": "1.1"}, "name": "demo", "files": files,
			})
			w.Upstream.Set("/simple/demo/", fakeupstream.Resource{
				Body:   doc,
				Header: http.Header{"Content-Type": {"application/vnd.pypi.simple.v1+json"}},
			})
			harness.WantStatus(t,
				w.Request(http.MethodGet, "/artifactory/api/pypi/remote/simple/demo/",
					"reader", "", "Accept", "application/vnd.pypi.simple.v1+json"),
				http.StatusOK)
			for version, method := range methods {
				data := wheels[version]
				sum := sha1.Sum(data) //nolint:gosec // the wheel's SHA-1
				sha := hex.EncodeToString(sum[:])
				path := "/artifactory/api/pypi/remote/demo/" + version + "/demo-" + version +
					"-py3-none-any.whl"
				for _, pass := range []string{"first download", "again"} {
					rec := w.Request(method, path, "reader", "")
					harness.WantStatus(t, rec, http.StatusOK)
					if got := rec.Header().Get("ETag"); got != sha ||
						rec.Header().Get("X-Checksum-Sha1") != sha {
						t.Fatalf("%s %s: ETag %q, X-Checksum-Sha1 %q; want %q for both", pass,
							method, got, rec.Header().Get("X-Checksum-Sha1"), sha)
					}
				}
				upstreamTag := fmt.Sprintf(`"%x"`, sha256.Sum256(data))
				harness.WantStatus(t,
					w.Request(method, path, "reader", "", "If-None-Match", upstreamTag),
					http.StatusOK)
				harness.WantStatus(t, w.Request(method, path, "reader", "", "If-None-Match", sha),
					http.StatusNotModified)
			}
		})
	}
}
