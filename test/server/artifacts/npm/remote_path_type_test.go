package npm_test

import (
	"crypto/sha1" //nolint:gosec // npm's dist.shasum
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// remoteTarballWorld is a remote npm repository over a fake upstream that
// serves pkg 1.0.0 and 1.0.1, whose tarballs the upstream labels label and
// sends with their X-Checksum-Sha1, as an Artifactory upstream does,
// storing artifacts locally when store is true. It returns the world and
// the tarballs by version.
func remoteTarballWorld(
	t *testing.T,
	store bool,
	label string,
) (*harness.RemoteWorld, map[string][]byte) {
	t.Helper()
	w := harness.NewRemoteWorld(t)
	w.Repo = *repo.Defaults(&repo.Config{
		Key: "remote", Class: repo.Remote, PackageType: repo.NPM, URL: w.Upstream.URL,
		StoreArtifactsLocally: new(store),
	})
	w.Update()
	versions := map[string]any{}
	tarballs := map[string][]byte{}
	for _, version := range []string{"1.0.0", "1.0.1"} {
		data := support.NPMTarballOf("pkg", version)
		sum := sha1.Sum(data) //nolint:gosec // npm's dist.shasum
		file := "/pkg/-/pkg-" + version + ".tgz"
		w.Upstream.Set(file, fakeupstream.Resource{
			Body: data, Header: http.Header{
				"Content-Type": {label}, "X-Checksum-Sha1": {hex.EncodeToString(sum[:])},
			},
		})
		versions[version] = map[string]any{
			"name": "pkg", "version": version,
			"dist": map[string]string{
				"tarball": w.Upstream.URL + file, "shasum": hex.EncodeToString(sum[:]),
			},
		}
		tarballs[version] = data
	}
	doc, _ := json.Marshal(map[string]any{
		"name": "pkg", "versions": versions, "dist-tags": map[string]string{"latest": "1.0.0"},
	})
	w.Upstream.Set("/pkg", fakeupstream.Resource{
		Body: doc, Header: http.Header{"Content-Type": {"application/json"}},
	})
	return w, tarballs
}

// TestAccNPMRemoteTarballTypedByPath: on a remote that stores nothing, a
// tarball its upstream labels application/octet-stream answers a GET and a
// HEAD with the type its kept copy answers on a remote that stores it,
// application/x-gzip, the type of its .tgz path. 7.161.26 serves a remote
// that stores nothing from the RemoteRepoResource, typed by
// NamingUtils.getMimeType of its path, and sends that type on both; the
// tarball route sent the upstream's label (#1477).
func TestAccNPMRemoteTarballTypedByPath(t *testing.T) {
	kept, _ := remoteTarballWorld(t, true, "application/octet-stream")
	path := "/artifactory/api/npm/remote/pkg/-/pkg-1.0.0.tgz"
	harness.WantStatus(t, kept.Request(http.MethodGet, path, "reader", ""), http.StatusOK)
	want := kept.Request(http.MethodGet, path, "reader", "").Header().Get("Content-Type")
	if want != "application/x-gzip" {
		t.Fatalf("the kept tarball answers %q", want)
	}

	w, tarballs := remoteTarballWorld(t, false, "application/octet-stream")
	for version, method := range map[string]string{"1.0.0": http.MethodGet, "1.0.1": http.MethodHead} {
		rec := w.Request(method, "/artifactory/api/npm/remote/pkg/-/pkg-"+version+".tgz",
			"reader", "")
		harness.WantStatus(t, rec, http.StatusOK)
		if got := rec.Header().Get("Content-Type"); got != want ||
			rec.Header().Get("Content-Length") != strconv.Itoa(len(tarballs[version])) {
			t.Fatalf("%s: Content-Type %q, Content-Length %q; want %q and %d bytes", method, got,
				rec.Header().Get("Content-Length"), want, len(tarballs[version]))
		}
	}
	if w.Upstream.Count(http.MethodHead, "/pkg/-/pkg-1.0.1.tgz") != 1 ||
		w.Upstream.Count(http.MethodGet, "/pkg/-/pkg-1.0.1.tgz") != 0 {
		t.Fatalf("the HEAD was not answered from the upstream's HEAD: %v", w.Upstream.Requests())
	}
}
