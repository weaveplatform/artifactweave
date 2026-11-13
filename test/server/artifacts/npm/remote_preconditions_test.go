package npm_test

import (
	"crypto/sha1" //nolint:gosec // npm's dist.shasum
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// TestAccNPMRemoteTarballIgnoresIfMatchAndIfUnmodifiedSince: a remote
// tarball, on a remote that stores it and one that does not, answers
// If-Match and If-Unmodified-Since with the whole tarball on the first
// download and again, whether the upstream's stream or the cached copy
// serves it, GET and HEAD alike. 7.x's download reads neither header
// (DownloadServiceImpl.isNotModified, 7.161.26; RTFACT-17259 closed
// without them), and the npm route's stream answered 412 where the cached
// copy answered 200 (#1468).
func TestAccNPMRemoteTarballIgnoresIfMatchAndIfUnmodifiedSince(t *testing.T) {
	modified := time.Unix(1700000000, 0).UTC()
	before := modified.Add(-time.Hour).Format(http.TimeFormat)
	for _, store := range []bool{true, false} {
		t.Run("store="+strconv.FormatBool(store), func(t *testing.T) {
			w := harness.NewRemoteWorld(t)
			w.Repo = *repo.Defaults(&repo.Config{
				Key: "remote", Class: repo.Remote, PackageType: repo.NPM, URL: w.Upstream.URL,
				StoreArtifactsLocally: new(store),
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
			versions := map[string]any{}
			tarballs := map[string][]byte{}
			for i := range cases {
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					version := "1." + strconv.Itoa(
						i,
					) + "." + map[string]string{"GET": "0", "HEAD": "1"}[method]
					data := support.NPMTarballOf("pkg", version)
					sum := sha1.Sum(data) //nolint:gosec // npm's dist.shasum
					file := "/pkg/-/pkg-" + version + ".tgz"
					w.Upstream.Set(file, fakeupstream.Resource{Body: data, Modified: modified})
					versions[version] = map[string]any{
						"name": "pkg", "version": version,
						"dist": map[string]string{
							"tarball": w.Upstream.URL + file, "shasum": hex.EncodeToString(sum[:]),
						},
					}
					tarballs[version] = data
				}
			}
			doc, _ := json.Marshal(map[string]any{
				"name":      "pkg",
				"versions":  versions,
				"dist-tags": map[string]string{"latest": "1.0.0"},
			})
			w.Upstream.Set("/pkg", fakeupstream.Resource{
				Body: doc, Header: http.Header{"Content-Type": {"application/json"}},
			})
			for i, c := range cases {
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					version := "1." + strconv.Itoa(
						i,
					) + "." + map[string]string{"GET": "0", "HEAD": "1"}[method]
					data := tarballs[version]
					sum := sha1.Sum(data) //nolint:gosec // the file's SHA-1
					want := ""
					if method == http.MethodGet {
						want = string(data)
					}
					for _, pass := range []string{"first download", "again"} {
						rec := w.Request(
							method,
							"/artifactory/api/npm/remote/pkg/-/pkg-"+version+".tgz",
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
