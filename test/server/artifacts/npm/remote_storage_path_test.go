package npm_test

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// TestAccNPMRemoteStoragePathReadsTarballsOnly (#1507): the item route of an
// npm remote reads a tarball its cache does not hold from the upstream, at
// <url>/<name>/-/<file>, and keeps it where the npm route finds it, so the
// npm route then serves the same tarball and the package's document. A
// package's own path, which the upstream answers with the package's
// document, keeps the cache lookup's 404 without an upstream request: kept
// as a file, the document would take the place of the folder holding the
// package's tarballs [I]. The tarball answered 404 "Could not find
// resource" without asking the upstream.
func TestAccNPMRemoteStoragePathReadsTarballsOnly(t *testing.T) {
	const notFound = `{"errors":[{"message":"Could not find resource","status":404}]}` + "\n"
	w, tarballs := remoteTarballWorld(t, true, "application/octet-stream")
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		got := w.Request(method, "/artifactory/remote/pkg", "reader", "")
		harness.WantStatus(t, got, http.StatusNotFound)
		if method == http.MethodGet && got.Body.String() != notFound {
			t.Fatal(got.Body)
		}
	}
	if n := len(w.Upstream.Requests()); n != 0 {
		t.Fatal("asked the upstream for the document", w.Upstream.Requests())
	}

	got := w.Request(http.MethodGet, "/artifactory/remote/pkg/-/pkg-1.0.1.tgz", "reader", "")
	harness.WantStatus(t, got, http.StatusOK)
	if !bytes.Equal(got.Body.Bytes(), tarballs["1.0.1"]) {
		t.Fatal("storage path tarball", got.Body.Len())
	}
	if w.Upstream.Count(http.MethodGet, "/pkg/-/pkg-1.0.1.tgz") != 1 {
		t.Fatal(w.Upstream.Requests())
	}

	got = w.Request(http.MethodGet, "/artifactory/api/npm/remote/pkg/-/pkg-1.0.1.tgz", "reader", "")
	harness.WantStatus(t, got, http.StatusOK)
	if !bytes.Equal(got.Body.Bytes(), tarballs["1.0.1"]) {
		t.Fatal("npm route tarball", got.Body.Len())
	}
	harness.WantStatus(t,
		w.Request(http.MethodGet, "/artifactory/api/npm/remote/pkg", "reader", ""), http.StatusOK)
	got = w.Request(http.MethodGet, "/artifactory/api/npm/remote/pkg/-/pkg-1.0.0.tgz", "reader", "")
	harness.WantStatus(t, got, http.StatusOK)
	if !bytes.Equal(got.Body.Bytes(), tarballs["1.0.0"]) {
		t.Fatal("npm route tarball", got.Body.Len())
	}
}
