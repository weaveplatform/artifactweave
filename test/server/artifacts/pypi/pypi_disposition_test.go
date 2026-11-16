package pypi_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// wheelName is the distribution these tests download.
const wheelName = "demo-1.0-py3-none-any.whl"

// packageDisposition is the Content-Disposition 7.113.0's PyPI API named
// a wheel with (pypa/pip#13396): the quoted name alone, without filename*.
const packageDisposition = `attachment; filename="` + wheelName + `"`

// wantNamed fails unless a GET and a HEAD that do sends to path answer 200
// naming the wheel with want, the GET with body and the HEAD with none.
func wantNamed(
	t *testing.T,
	do func(method, path string) *httptest.ResponseRecorder,
	path, want, body string,
) {
	t.Helper()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := do(method, path)
		wantStatus(t, rec, http.StatusOK)
		wantBody := body
		if method == http.MethodHead {
			wantBody = ""
		}
		if got := rec.Header().Get("Content-Disposition"); got != want ||
			rec.Header().
				Get("X-Artifactory-Filename") !=
				wheelName || rec.Body.String() != wantBody {
			t.Fatalf("%s %s: Content-Disposition %q, X-Artifactory-Filename %q, %d bytes; "+
				"want %q, %s, %d bytes", method, path, got,
				rec.Header().Get("X-Artifactory-Filename"), rec.Body.Len(), want, wheelName,
				len(wantBody))
		}
	}
}

// TestAccPypiFileNamedWithoutFilenameStar: a distribution downloaded from
// the PyPI API, GET and HEAD, from a local and a federated repository and
// from a remote, on its first fetch and from its cache, is named by
// Content-Disposition: attachment; filename="<name>" with no filename*
// part. (The PyPI API answers a virtual repository 501.) 7.113.0 named a
// PyPI API download so (pypa/pip#13396), the form 7.161.26's
// JerseyArtifactoryResponse writes for the package download service,
// while the item route, which keeps filename* here, adds it
// (HttpArtifactoryResponse). The PyPI API sent the item route's form
// (#1505), and nothing for a remote that stores nothing (#1594).
func TestAccPypiFileNamedWithoutFilenameStar(t *testing.T) {
	const file = "demo/1.0/" + wheelName
	itemForm := itemkit.ItemDisposition(wheelName)

	t.Run("local and federated", func(t *testing.T) {
		w := newItemWorld(t, false)
		wheel := string(pypiWheel(t, "demo", "1.0"))
		do := func(method, path string) *httptest.ResponseRecorder {
			return w.do(method, path, "admin", "")
		}
		for _, c := range []repo.Config{
			{Key: "pypi-local", Class: repo.Local, PackageType: repo.PyPI},
			{Key: "pypi-fed", Class: repo.Federated, PackageType: repo.PyPI},
		} {
			if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
				t.Fatal(err)
			}
			w.put("/artifactory/"+c.Key+"/"+file, wheel)
			wantNamed(t, do, "/artifactory/api/pypi/"+c.Key+"/"+file, packageDisposition, wheel)
			wantNamed(t, do, "/artifactory/"+c.Key+"/"+file, itemForm, wheel)
		}
	})

	t.Run("remote cache", func(t *testing.T) {
		w, data := pypiRemoteWorld(t, true)
		// The first download fetches the wheel into the cache and serves it
		// from there, so it is named as the cached wheel is.
		first := w.Request(http.MethodGet, "/artifactory/api/pypi/remote/"+file, "admin", "")
		harness.WantStatus(t, first, http.StatusOK)
		if got := first.Header().Get("Content-Disposition"); got != packageDisposition {
			t.Fatalf("first fetch: Content-Disposition %q, want %q", got, packageDisposition)
		}
		do := func(method, path string) *httptest.ResponseRecorder {
			return w.Request(method, path, "admin", "")
		}
		wantNamed(t, do, "/artifactory/api/pypi/remote/"+file, packageDisposition, string(data))
		wantNamed(t, do, "/artifactory/remote-cache/"+file, itemForm, string(data))
	})

	// A remote that stores nothing serves the wheel from the upstream's
	// stream, named as the copy it would keep, on a GET, a HEAD, a range
	// and a 304 (#1594).
	t.Run("remote that stores nothing", func(t *testing.T) {
		w, data := pypiRemoteWorld(t, false)
		path := "/artifactory/api/pypi/remote/" + file
		do := func(method, path string) *httptest.ResponseRecorder {
			return w.Request(method, path, "admin", "")
		}
		wantNamed(t, do, path, packageDisposition, string(data))
		sha := w.Request(http.MethodGet, path, "admin", "").Header().Get("X-Checksum-Sha1")
		for _, c := range []struct {
			headers []string
			status  int
		}{
			{[]string{"Range", "bytes=1-2"}, http.StatusPartialContent},
			{[]string{"If-None-Match", sha}, http.StatusNotModified},
		} {
			rec := w.Request(http.MethodGet, path, "admin", "", c.headers...)
			if rec.Code != c.status ||
				rec.Header().Get("Content-Disposition") != packageDisposition ||
				rec.Header().Get("X-Artifactory-Filename") != wheelName {
				t.Errorf("%v: %d, Content-Disposition %q, X-Artifactory-Filename %q",
					c.headers, rec.Code, rec.Header().Get("Content-Disposition"),
					rec.Header().Get("X-Artifactory-Filename"))
			}
		}
	})
}

// pypiRemoteWorld is a PyPI remote over an upstream with the demo 1.0 wheel
// in its simple index, which it has read once, keeping its files when
// store holds. It returns the wheel's bytes.
func pypiRemoteWorld(t *testing.T, store bool) (*harness.RemoteWorld, []byte) {
	t.Helper()
	w := harness.NewRemoteWorld(t)
	w.Repo = *repo.Defaults(&repo.Config{
		Key: "remote", Class: repo.Remote, PackageType: repo.PyPI, URL: w.Upstream.URL,
		PyPIRegistryURL: w.Upstream.URL, StoreArtifactsLocally: new(store),
	})
	w.Update()
	data := pypiWheel(t, "demo", "1.0")
	sum := sha256.Sum256(data)
	w.Upstream.Set("/files/"+wheelName, fakeupstream.Resource{Body: data})
	doc, err := json.Marshal(map[string]any{
		"meta": map[string]string{"api-version": "1.1"}, "name": "demo",
		"files": []map[string]any{{
			"filename": wheelName, "url": "/files/" + wheelName,
			"hashes": map[string]string{"sha256": hex.EncodeToString(sum[:])},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	w.Upstream.Set("/simple/demo/", fakeupstream.Resource{
		Body:   doc,
		Header: http.Header{"Content-Type": {"application/vnd.pypi.simple.v1+json"}},
	})
	harness.WantStatus(t, w.Request(http.MethodGet, "/artifactory/api/pypi/remote/simple/demo/",
		"admin", "", "Accept", "application/vnd.pypi.simple.v1+json"), http.StatusOK)
	return w, data
}
