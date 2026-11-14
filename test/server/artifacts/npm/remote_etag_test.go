package npm_test

import (
	"crypto/sha1" //nolint:gosec // the tarball's SHA-1, as 7.x sends it
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"testing"

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
