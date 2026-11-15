package conda_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

// packageName is the package these tests download.
const packageName = "artifactweave-conda-fixture-1.0.0-0.tar.bz2"

// packageDisposition is the Content-Disposition 7.33.12's Conda API named
// a file with (mamba-org/mamba#2105): the quoted name alone, without
// filename*.
const packageDisposition = `attachment; filename="` + packageName + `"`

// wantNamed fails unless a GET and a HEAD that do sends to path answer 200
// naming the package with want, the GET with body and the HEAD with none.
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
			rec.Header().Get("X-Artifactory-Filename") != packageName ||
			rec.Body.String() != wantBody {
			t.Fatalf("%s %s: Content-Disposition %q, X-Artifactory-Filename %q, %d bytes; "+
				"want %q, %s, %d bytes", method, path, got,
				rec.Header().Get("X-Artifactory-Filename"), rec.Body.Len(), want, packageName,
				len(wantBody))
		}
	}
}

// TestAccCondaPackageNamedWithoutFilenameStar: a package downloaded from
// the Conda API, GET and HEAD, from a local, a federated and a virtual
// repository and from a remote, on its first fetch and from its cache,
// directly and through a virtual repository, is named by
// Content-Disposition: attachment; filename="<name>" with no filename*
// part. 7.33.12 named a Conda API download so (mamba-org/mamba#2105), the
// form 7.161.26's JerseyArtifactoryResponse writes for the package
// download service, while the item route, which keeps filename* here,
// adds it (HttpArtifactoryResponse; RTFACT-30882 on 7.99.2). The Conda
// API sent the item route's form (#1505).
func TestAccCondaPackageNamedWithoutFilenameStar(t *testing.T) {
	itemForm := blob.Disposition(packageName)

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
			wantStatus(t, w.do(http.MethodPut, "/artifactory/"+key+"/noarch/"+packageName, "admin",
				body), http.StatusCreated)
		}
		do := func(method, path string) *httptest.ResponseRecorder {
			return w.do(method, path, "admin", "")
		}
		for _, key := range []string{"conda-local", "conda-fed", "conda-virtual"} {
			wantNamed(t, do, "/artifactory/api/conda/"+key+"/noarch/"+packageName,
				packageDisposition, body)
		}
		wantNamed(t, do, "/artifactory/conda-local/noarch/"+packageName, itemForm, body)
	})

	t.Run("remote cache", func(t *testing.T) {
		w, _, _ := newCondaRemoteWorld(t)
		if err := w.rt.Store.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
			Key: "conda-virtual", Class: repo.Virtual, PackageType: repo.Conda,
			Repositories: []string{"remote"},
		})); err != nil {
			t.Fatal(err)
		}
		// The first download fetches the package into the cache and serves
		// it from there, so it is named as the cached package is.
		first := w.request(http.MethodGet, "/artifactory/api/conda/remote/noarch/"+packageName,
			"admin", "")
		remoteStatus(t, first, http.StatusOK)
		if got := first.Header().Get("Content-Disposition"); got != packageDisposition {
			t.Fatalf("first fetch: Content-Disposition %q, want %q", got, packageDisposition)
		}
		do := func(method, path string) *httptest.ResponseRecorder {
			return w.request(method, path, "admin", "")
		}
		body := string(condaFixture(t))
		for _, key := range []string{"remote", "conda-virtual"} {
			wantNamed(t, do, "/artifactory/api/conda/"+key+"/noarch/"+packageName,
				packageDisposition, body)
		}
		wantNamed(t, do, "/artifactory/remote-cache/noarch/"+packageName, itemForm, body)
	})
}
