package gems_test

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	gemfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/gems"
)

// customType is the artifactory.content-type value these tests set.
const customType = "text/x-custom"

// setContentType sets the artifactory.content-type property of the file at
// path in the repository key, a remote's cache included, to customType.
func (w *world) setContentType(key, path string) {
	w.t.Helper()
	status(w.t, w.do(http.MethodPut, "/artifactory/api/storage/"+key+"/"+path+
		"?properties=artifactory.content-type=text%2Fx-custom", "admin", nil),
		http.StatusNoContent)
}

// wantTyped fails unless a GET and a HEAD of path answer 200 with
// customType, the GET with body and the HEAD with none.
func (w *world) wantTyped(path string, body []byte) {
	w.t.Helper()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := w.do(method, path, "admin", nil)
		status(w.t, rec, http.StatusOK)
		want := body
		if method == http.MethodHead {
			want = nil
		}
		if got := rec.Header().Get("Content-Type"); got != customType ||
			!bytes.Equal(rec.Body.Bytes(), want) {
			w.t.Fatalf("%s %s: Content-Type %q, %d bytes; want %q, %d bytes",
				method, path, got, rec.Body.Len(), customType, len(want))
		}
	}
}

// TestAccNativeGemTypedByContentTypeProperty: a gem whose
// artifactory.content-type property is set downloads with the property's
// type from the gems API, GET and HEAD, from a local, a federated and a
// virtual repository and from a remote's cache, as on the item route.
// 7.41.13 answered the gems API in its download service's form
// (RTFACT-27459), and that service types a file by the property
// (DbStoringRepoMixin, 7.161.26); the gems API sent the path's type
// (#1396).
func TestAccNativeGemTypedByContentTypeProperty(t *testing.T) {
	const gem = "gems/rake-12.3.1.gem"

	t.Run("local, federated and virtual", func(t *testing.T) {
		w := newWorld(t)
		for _, key := range []string{"gems-local", "gems-fed"} {
			status(t, w.do(http.MethodPut, "/artifactory/"+key+"/"+gem, "admin", w.gem),
				http.StatusCreated)
		}
		w.setContentType("gems-local", gem)
		w.setContentType("gems-fed", gem)
		for _, key := range []string{"gems-local", "gems-fed", "gems-virtual"} {
			w.wantTyped("/artifactory/api/gems/"+key+"/"+gem, w.gem)
		}
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
		status(t, w.do(http.MethodGet, "/artifactory/api/gems/gems-remote/"+gem, "admin", nil),
			http.StatusOK)
		w.setContentType("gems-remote-cache", gem)
		for _, key := range []string{"gems-remote", "gems-remote-virtual"} {
			w.wantTyped("/artifactory/api/gems/"+key+"/"+gem, w.gem)
		}
	})
}
