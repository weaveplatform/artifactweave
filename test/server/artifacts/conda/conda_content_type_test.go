package conda_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
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

// TestAccCondaPackageTypedByContentTypeProperty: a package whose
// artifactory.content-type property is set downloads with the property's
// type from the Conda API, GET and HEAD, from a local, a federated and a
// virtual repository and from a remote's cache, as on the item route.
// 7.33.12 answered the Conda API in its download service's form
// (mamba-org/mamba#2105), and that service types a file by the property
// (DbStoringRepoMixin, 7.161.26); the Conda API sent the path's type
// (#1396).
func TestAccCondaPackageTypedByContentTypeProperty(t *testing.T) {
	const name = "artifactweave-conda-fixture-1.0.0-0.tar.bz2"

	t.Run("local, federated and virtual", func(t *testing.T) {
		w := newCondaShardWorld(t, false, nil)
		for _, c := range []repo.Config{
			{Key: "conda-local", Class: repo.Local, PackageType: repo.Conda},
			{Key: "conda-fed", Class: repo.Federated, PackageType: repo.Conda},
			{
				Key: "conda-virtual", Class: repo.Virtual, PackageType: repo.Conda,
				Repositories: []string{"conda-local"},
			},
		} {
			if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
				t.Fatal(err)
			}
		}
		body := string(condaFixture(t))
		for _, key := range []string{"conda-local", "conda-fed"} {
			wantStatus(t, w.do(http.MethodPut, "/artifactory/"+key+"/noarch/"+name, "admin", body),
				http.StatusCreated)
			wantStatus(t, w.do(http.MethodPut,
				"/artifactory/api/storage/"+key+"/noarch/"+name+setContentTypeQuery, "admin", ""),
				http.StatusNoContent)
		}
		do := func(method, path string) *httptest.ResponseRecorder {
			return w.do(method, path, "admin", "")
		}
		for _, key := range []string{"conda-local", "conda-fed", "conda-virtual"} {
			wantTyped(t, do, "/artifactory/api/conda/"+key+"/noarch/"+name, body)
		}
	})

	t.Run("remote cache", func(t *testing.T) {
		w, _, _ := newCondaRemoteWorld(t)
		if err := w.rt.Store.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
			Key: "conda-virtual", Class: repo.Virtual, PackageType: repo.Conda,
			Repositories: []string{"remote"},
		})); err != nil {
			t.Fatal(err)
		}
		remoteStatus(t,
			w.request(http.MethodGet, "/artifactory/api/conda/remote/noarch/"+name, "admin", ""),
			http.StatusOK)
		remoteStatus(t, w.request(http.MethodPut,
			"/artifactory/api/storage/remote-cache/noarch/"+name+setContentTypeQuery, "admin", ""),
			http.StatusNoContent)
		do := func(method, path string) *httptest.ResponseRecorder {
			return w.request(method, path, "admin", "")
		}
		for _, key := range []string{"remote", "conda-virtual"} {
			wantTyped(t, do, "/artifactory/api/conda/"+key+"/noarch/"+name, string(condaFixture(t)))
		}
	})
}
