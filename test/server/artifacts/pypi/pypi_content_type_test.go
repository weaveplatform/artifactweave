package pypi_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// customType is the artifactory.content-type value these tests set.
const customType = "text/x-custom"

// setContentTypeQuery sets a file's artifactory.content-type property to
// customType, after the file's /artifactory/api/storage/<repo>/<path>.
const setContentTypeQuery = "?properties=artifactory.content-type=text%2Fx-custom"

// wantTyped fails unless a GET and a HEAD that do sends to path answer 200
// with customType, the GET with body and the HEAD with none.
func wantTyped(
	t *testing.T,
	do func(method, path string) *httptest.ResponseRecorder,
	path, body string,
) {
	t.Helper()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := do(method, path)
		wantStatus(t, rec, http.StatusOK)
		want := body
		if method == http.MethodHead {
			want = ""
		}
		if got := rec.Header().Get("Content-Type"); got != customType || rec.Body.String() != want {
			t.Fatalf("%s %s: Content-Type %q, %d bytes; want %q, %d bytes",
				method, path, got, rec.Body.Len(), customType, len(want))
		}
	}
}

// TestAccPypiFileTypedByContentTypeProperty: a distribution whose
// artifactory.content-type property is set downloads with the property's
// type from the PyPI API, GET and HEAD, from a local and a federated
// repository and from a remote's cache, as on the item route. (The PyPI
// API answers a virtual repository 501.) 7.113.0
// answered the PyPI API's file route in its download service's form
// (pypa/pip#13396), and that service types a file by the property
// (DbStoringRepoMixin, 7.161.26); the PyPI API sent the path's type
// (#1396).
func TestAccPypiFileTypedByContentTypeProperty(t *testing.T) {
	const file = "demo/1.0/demo-1.0-py3-none-any.whl"

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
			wantStatus(t, w.do(http.MethodPut,
				"/artifactory/api/storage/"+c.Key+"/"+file+setContentTypeQuery, "admin", ""),
				http.StatusNoContent)
			wantTyped(t, do, "/artifactory/api/pypi/"+c.Key+"/"+file, wheel)
		}
	})

	t.Run("remote cache", func(t *testing.T) {
		w := harness.NewRemoteWorld(t)
		w.Repo = *repo.Defaults(&repo.Config{
			Key: "remote", Class: repo.Remote, PackageType: repo.PyPI, URL: w.Upstream.URL,
			PyPIRegistryURL: w.Upstream.URL,
		})
		w.Update()
		data := pypiWheel(t, "demo", "1.0")
		sum := sha256.Sum256(data)
		w.Upstream.Set("/files/demo-1.0-py3-none-any.whl", fakeupstream.Resource{Body: data})
		doc, err := json.Marshal(map[string]any{
			"meta": map[string]string{"api-version": "1.1"}, "name": "demo",
			"files": []map[string]any{{
				"filename": "demo-1.0-py3-none-any.whl", "url": "/files/demo-1.0-py3-none-any.whl",
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
		harness.WantStatus(t, w.Request(http.MethodGet, "/artifactory/api/pypi/remote/"+file,
			"admin", ""), http.StatusOK)
		harness.WantStatus(t, w.Request(http.MethodPut,
			"/artifactory/api/storage/remote-cache/"+file+setContentTypeQuery, "admin", ""),
			http.StatusNoContent)
		do := func(method, path string) *httptest.ResponseRecorder {
			return w.Request(method, path, "admin", "")
		}
		wantTyped(t, do, "/artifactory/api/pypi/remote/"+file, string(data))
	})
}
