package gems_test

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	gemfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/gems"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

// packageDisposition is the Content-Disposition 7.41.13's gems API named
// a file with (RTFACT-27459): the quoted name alone, without filename*.
const packageDisposition = `attachment; filename="rake-12.3.1.gem"`

// wantNamed fails unless a GET and a HEAD of path answer 200 naming the
// gem with want, the GET with body and the HEAD with none.
func (w *world) wantNamed(path, want string, body []byte) {
	w.t.Helper()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := w.do(method, path, "admin", nil)
		status(w.t, rec, http.StatusOK)
		wantBody := body
		if method == http.MethodHead {
			wantBody = nil
		}
		if got := rec.Header().Get("Content-Disposition"); got != want ||
			rec.Header().Get("X-Artifactory-Filename") != "rake-12.3.1.gem" ||
			!bytes.Equal(rec.Body.Bytes(), wantBody) {
			w.t.Fatalf("%s %s: Content-Disposition %q, X-Artifactory-Filename %q, %d bytes; "+
				"want %q, rake-12.3.1.gem, %d bytes", method, path, got,
				rec.Header().Get("X-Artifactory-Filename"), rec.Body.Len(), want, len(wantBody))
		}
	}
}

// TestAccNativeGemNamedWithoutFilenameStar: a gem downloaded from the gems
// API, GET and HEAD, from a local, a federated and a virtual repository
// and from a remote, on its first fetch and from its cache, directly and
// through a virtual repository, is named by Content-Disposition: attachment; filename="<name>" with no
// filename* part. 7.41.13 named a gems API download so (RTFACT-27459),
// the form 7.161.26's JerseyArtifactoryResponse writes for the package
// download service, while its item route, which keeps filename* here,
// adds it (HttpArtifactoryResponse; RTFACT-25859 on 7.17.11). The gems
// API sent the item route's form (#1505).
func TestAccNativeGemNamedWithoutFilenameStar(t *testing.T) {
	const gem = "gems/rake-12.3.1.gem"
	itemForm := blob.Disposition("rake-12.3.1.gem")

	t.Run("local, federated and virtual", func(t *testing.T) {
		w := newWorld(t)
		for _, key := range []string{"gems-local", "gems-fed"} {
			status(t, w.do(http.MethodPut, "/artifactory/"+key+"/"+gem, "admin", w.gem),
				http.StatusCreated)
		}
		for _, key := range []string{"gems-local", "gems-fed", "gems-virtual"} {
			w.wantNamed("/artifactory/api/gems/"+key+"/"+gem, packageDisposition, w.gem)
		}
		w.wantNamed("/artifactory/gems-local/"+gem, itemForm, w.gem)
	})

	t.Run("remote cache", func(t *testing.T) {
		up := fakeupstream.New(t, fakeupstream.Options{})
		w := newRemoteWorld(t, up)
		p, err := gemfmt.Read(t.Context(), bytes.NewReader(w.gem), int64(len(w.gem)), gem)
		if err != nil {
			t.Fatal(err)
		}
		packages := []gemfmt.Package{p}
		up.Set("/versions", fakeupstream.Resource{Body: gemfmt.CompactVersions(packages)})
		up.Set("/info/rake", fakeupstream.Resource{Body: gemfmt.CompactInfo("rake", packages)})
		up.Set("/"+gem, fakeupstream.Resource{Body: w.gem})
		// The first download fetches the gem into the cache and serves it
		// from there, so it is named as the cached gem is.
		first := w.do(http.MethodGet, "/artifactory/api/gems/gems-remote/"+gem, "admin", nil)
		status(t, first, http.StatusOK)
		if got := first.Header().Get("Content-Disposition"); got != packageDisposition {
			t.Fatalf("first fetch: Content-Disposition %q, want %q", got, packageDisposition)
		}
		for _, key := range []string{"gems-remote", "gems-remote-virtual"} {
			w.wantNamed("/artifactory/api/gems/"+key+"/"+gem, packageDisposition, w.gem)
		}
		w.wantNamed("/artifactory/gems-remote-cache/"+gem, itemForm, w.gem)
	})
}
