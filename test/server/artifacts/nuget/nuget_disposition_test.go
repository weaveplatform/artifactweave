package nuget_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// packageDisposition is the Content-Disposition 7.35.2's NuGet v2
// Download named a package with (chocolatey/choco#3022): the quoted name
// alone, without filename*.
func packageDisposition(name string) string { return `attachment; filename="` + name + `"` }

// wantNamed fails unless a GET and a HEAD that do sends to path answer 200
// naming the file name with want, typed contentType, the GET with body and
// the HEAD with none.
func wantNamed(
	t *testing.T,
	do func(method, path string) *httptest.ResponseRecorder,
	path, name, want, contentType string,
	body []byte,
) {
	t.Helper()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := do(method, path)
		remoteStatus(t, rec, http.StatusOK)
		wantBody := string(body)
		if method == http.MethodHead {
			wantBody = ""
		}
		h := rec.Header()
		if got := h.Get("Content-Disposition"); got != want ||
			h.Get("X-Artifactory-Filename") != name || h.Get("Content-Type") != contentType ||
			rec.Body.String() != wantBody {
			t.Fatalf("%s %s: Content-Disposition %q, X-Artifactory-Filename %q, "+
				"Content-Type %q, %d bytes; want %q, %s, %s, %d bytes", method, path, got,
				h.Get("X-Artifactory-Filename"), h.Get("Content-Type"), rec.Body.Len(), want,
				name, contentType, len(wantBody))
		}
	}
}

// TestAccNuGetPackageNamedWithoutFilenameStar: a package downloaded
// through the NuGet API, GET and HEAD, by v2 Download, the flat container
// and the registration's Download, from a local and a federated
// repository and from a remote, on its first fetch and from its cache, is
// named by Content-Disposition: attachment; filename="<name>" with no
// filename* part, and typed by its artifactory.content-type property.
// (The NuGet API answers a virtual repository 501.) 7.35.2's v2 Download
// answered so (chocolatey/choco#3022), in the form 7.161.26's
// JerseyArtifactoryResponse writes for the package download service,
// whose DownloadServiceImpl.process types the file by the property
// (decision 0032). The item route keeps the filename* part
// HttpArtifactoryResponse adds. The NuGet API sent the item route's form
// and the path's type (#1505).
func TestAccNuGetPackageNamedWithoutFilenameStar(t *testing.T) {
	t.Run("local and federated", func(t *testing.T) {
		h, db, _ := nugetServer(t)
		fed := repo.Config{Key: "nuget-fed", Class: repo.Federated, PackageType: repo.NuGet}
		if err := db.Repos().Create(context.Background(), *repo.Defaults(&fed)); err != nil {
			t.Fatal(err)
		}
		pkg := nugetPackage("Acme.Lib", "1.0.0", "")
		const name = "Acme.Lib.1.0.0.nupkg"
		get := func(method, path string) *httptest.ResponseRecorder {
			return do(t, h, method, path, basic("admin", "pw"), "")
		}
		for _, key := range []string{"nuget-local", "nuget-fed"} {
			headers := map[string]string{"X-NuGet-ApiKey": "admin:pw"}
			var form string
			headers["Content-Type"], form = contentTypeOf(nugetForm(pkg))
			remoteStatus(
				t,
				do(t, h, http.MethodPut, "/artifactory/api/nuget/"+key+"/", headers, form),
				http.StatusCreated,
			)
			native := []string{
				"/artifactory/api/nuget/" + key + "/Download/Acme.Lib/1.0.0",
				nugetV3 + key + "/flatcontainer/acme.lib/1.0.0/acme.lib.1.0.0.nupkg",
				nugetV3 + key + "/registration-semver2/Download/acme.lib/1.0.0",
			}
			item := "/artifactory/" + key + "/" + name
			for _, contentType := range []string{"application/x-nupkg", "text/x-custom"} {
				if contentType != "application/x-nupkg" {
					remoteStatus(t, get(http.MethodPut, "/artifactory/api/storage/"+key+"/"+name+
						"?properties=artifactory.content-type=text%2Fx-custom"), http.StatusNoContent)
				}
				for _, path := range native {
					wantNamed(t, get, path, name, packageDisposition(name), contentType, pkg)
				}
				wantNamed(t, get, item, name, itemkit.ItemDisposition(name), contentType, pkg)
			}
			// A resumed download and a revalidation keep the name.
			ranged := basic("admin", "pw")
			ranged["Range"] = "bytes=0-3"
			rec := do(t, h, http.MethodGet, native[0], ranged, "")
			remoteStatus(t, rec, http.StatusPartialContent)
			if got := rec.Header().Get("Content-Disposition"); got != packageDisposition(name) {
				t.Fatalf("206: Content-Disposition %q, want %q", got, packageDisposition(name))
			}
			revalidate := basic("admin", "pw")
			revalidate["If-None-Match"] = rec.Header().Get("ETag")
			rec = do(t, h, http.MethodGet, native[0], revalidate, "")
			remoteStatus(t, rec, http.StatusNotModified)
			if got := rec.Header().Get("Content-Disposition"); got != packageDisposition(name) {
				t.Fatalf("304: Content-Disposition %q, want %q", got, packageDisposition(name))
			}
		}
	})

	t.Run("remote", func(t *testing.T) {
		w := newRemoteWorld(t)
		w.c = *repo.Defaults(&repo.Config{
			Key: "remote", Class: repo.Remote, PackageType: repo.NuGet, URL: w.up.URL,
			V3FeedURL: w.up.URL + "/v3/index.json",
		})
		w.update()
		body := reindexNupkg(t)
		seedReindexFeed(w, body)
		const name = "reindex.demo.1.0.0.nupkg"
		download := "/artifactory/api/nuget/remote/Download/Reindex.Demo/1.0.0"
		// The first download fetches the package into the cache and serves
		// it from there, so it is named as the cached package is.
		first := w.request(http.MethodGet, download, "reader", "")
		remoteStatus(t, first, http.StatusOK)
		if got := first.Header().Get("Content-Disposition"); got != packageDisposition(name) {
			t.Fatalf("first fetch: Content-Disposition %q, want %q", got, packageDisposition(name))
		}
		get := func(method, path string) *httptest.ResponseRecorder {
			return w.request(method, path, "reader", "")
		}
		wantNamed(t, get, download, name, packageDisposition(name), "application/x-nupkg", body)
		wantNamed(t, get, nugetV3+"remote/flatcontainer/reindex.demo/1.0.0/"+name, name,
			packageDisposition(name), "application/x-nupkg", body)
		wantNamed(t, get, "/artifactory/remote-cache/reindex.demo/1.0.0/"+name, name,
			itemkit.ItemDisposition(name), "application/x-nupkg", body)
	})
}
