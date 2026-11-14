package npm_test

import (
	"crypto/sha1" //nolint:gosec // the tarball's SHA-1, as 7.x sends it
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// TestAccNPMRemoteTarballETagIsItsSHA1: a remote tarball answers a GET and
// a HEAD with its SHA-1, bare, as its ETag, beside X-Checksum-Sha1, on the
// first download and again, on a remote that stores it and one that does
// not; the upstream's ETag matches nothing and the SHA-1 answers 304.
// 7.161.26's RequestResponseHelper.setBasicHeaders sets every downloaded
// resource's ETag to its SHA-1; the tarball route sent and matched the
// upstream's (#1484).
func TestAccNPMRemoteTarballETagIsItsSHA1(t *testing.T) {
	for _, store := range []bool{true, false} {
		t.Run("store="+strconv.FormatBool(store), func(t *testing.T) {
			w, tarballs := remoteTarballWorld(t, store, "application/octet-stream")
			for version, method := range map[string]string{
				"1.0.0": http.MethodGet, "1.0.1": http.MethodHead,
			} {
				data := tarballs[version]
				sum := sha1.Sum(data) //nolint:gosec // the tarball's SHA-1
				sha := hex.EncodeToString(sum[:])
				path := "/artifactory/api/npm/remote/pkg/-/pkg-" + version + ".tgz"
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

// TestAccNPMRemoteTarballHeadWithoutChecksumIsMatchedByDate: on a remote
// that stores nothing, a tarball HEAD answered from the upstream's HEAD,
// which sends an ETag and no checksum, answers 200 with no ETag to
// If-None-Match "*", the upstream's ETag and a list on one line, and 304
// to an If-Modified-Since after the upstream's Last-Modified, with or
// without a length. 7.161.26's DownloadServiceImpl.isNotModified matches
// If-None-Match only against the SHA-1, which such a resource lacks; the
// tarball route answered by http.ServeContent against the upstream's ETag
// (#1487).
func TestAccNPMRemoteTarballHeadWithoutChecksumIsMatchedByDate(t *testing.T) {
	w, tarballs := remoteTarballWorld(t, false, "application/octet-stream")
	modified := time.Unix(1700000000, 0).UTC()
	after := modified.Add(time.Hour).Format(http.TimeFormat)
	upstreamTag := fmt.Sprintf(`"%x"`, sha256.Sum256(tarballs["1.0.1"]))
	cases := []struct {
		label   string
		headers []string
		length  bool
		want    int
	}{
		{"If-None-Match *", []string{"If-None-Match", "*"}, true, http.StatusOK},
		{"If-None-Match upstream", []string{"If-None-Match", upstreamTag}, true, http.StatusOK},
		{
			"If-None-Match list",
			[]string{"If-None-Match", `"x", ` + upstreamTag},
			true,
			http.StatusOK,
		},
		{
			"If-Modified-Since after",
			[]string{"If-Modified-Since", after},
			true,
			http.StatusNotModified,
		},
		{
			"If-Modified-Since after, no length",
			[]string{"If-Modified-Since", after},
			false,
			http.StatusNotModified,
		},
	}
	for _, c := range cases {
		header := http.Header{"Content-Type": {"application/octet-stream"}}
		if !c.length {
			// http.ServeContent, which answers the fake upstream's HEAD,
			// sends no Content-Length beside a Content-Encoding.
			header["Content-Encoding"] = []string{"identity"}
		}
		w.Upstream.Set("/pkg/-/pkg-1.0.1.tgz", fakeupstream.Resource{
			Body: tarballs["1.0.1"], Modified: modified, Header: header,
		})
		rec := w.Request(http.MethodHead, "/artifactory/api/npm/remote/pkg/-/pkg-1.0.1.tgz",
			"reader", "", c.headers...)
		if rec.Code != c.want || rec.Header().Get("ETag") != "" {
			t.Errorf("%s: %d, ETag %q; want %d and no ETag", c.label, rec.Code,
				rec.Header().Get("ETag"), c.want)
		}
	}
	if got := w.Upstream.Count(http.MethodGet, "/pkg/-/pkg-1.0.1.tgz"); got != 0 {
		t.Fatalf("a HEAD downloaded the tarball: %v", w.Upstream.Requests())
	}
}

// TestAccNPMRemoteKeysETagIsItsSHA1: a remote's keys document, the
// upstream's bytes the cache keeps, answers with its SHA-1, bare, as its
// ETag on the first GET and from the cache, and the upstream's ETag matches
// nothing. 7.161.26 serves a cached file with its SHA-1 as ETag
// (RequestResponseHelper.setBasicHeaders); the route sent the upstream's
// (#1484).
func TestAccNPMRemoteKeysETagIsItsSHA1(t *testing.T) {
	w := harness.NewRemoteWorld(t)
	w.Repo = *repo.Defaults(&repo.Config{
		Key: w.Repo.Key, Class: repo.Remote, PackageType: repo.NPM, URL: w.Upstream.URL,
	})
	w.Update()
	doc := []byte(`{"keys":[{"expires":null,"keyid":"SHA256:upstream",` +
		`"keytype":"ecdsa-sha2-nistp256","scheme":"ecdsa-sha2-nistp256","key":"MFkw"}]}`)
	w.Upstream.Set("/-/npm/v1/keys", fakeupstream.Resource{
		Body: doc, Header: http.Header{"Content-Type": []string{"application/json"}},
	})
	sum := sha1.Sum(doc) //nolint:gosec // the document's SHA-1
	sha := hex.EncodeToString(sum[:])
	keys := npmBase + w.Repo.Key + "/-/npm/v1/keys"
	for _, pass := range []string{"first download", "again"} {
		rec := w.Request(http.MethodGet, keys, "reader", "")
		harness.WantStatus(t, rec, http.StatusOK)
		if got := rec.Header().
			Get("ETag"); got != sha ||
			rec.Header().Get("X-Checksum-Sha1") != sha {
			t.Fatalf("%s: ETag %q, X-Checksum-Sha1 %q; want %q for both", pass, got,
				rec.Header().Get("X-Checksum-Sha1"), sha)
		}
	}
	upstreamTag := fmt.Sprintf(`"%x"`, sha256.Sum256(doc))
	harness.WantStatus(
		t,
		w.Request(http.MethodGet, keys, "reader", "", "If-None-Match", upstreamTag),
		http.StatusOK,
	)
	harness.WantStatus(t, w.Request(http.MethodGet, keys, "reader", "", "If-None-Match", sha),
		http.StatusNotModified)
}
