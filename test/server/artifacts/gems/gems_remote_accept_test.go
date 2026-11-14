package gems_test

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	gemfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/gems"
)

// TestAccRemoteGemSendsNoClientAccept: a remote gem is fetched without the
// client's Accept, and the copy one client's GET cached serves a client
// that accepts something else, with no upstream request. 7.161.26's
// HttpRepo.whiteListHeaders sends upstream no header of a client's request,
// and the cache holds one file per path; the gem route's read keyed the
// copy on the client's Accept and sent it upstream (#1390).
func TestAccRemoteGemSendsNoClientAccept(t *testing.T) {
	up := fakeupstream.New(t, fakeupstream.Options{})
	w := newRemoteWorld(t, up)
	p, err := gemfmt.Read(t.Context(), bytes.NewReader(w.gem), int64(len(w.gem)),
		"gems/rake-12.3.1.gem")
	if err != nil {
		t.Fatal(err)
	}
	packages := []gemfmt.Package{p}
	up.Set("/versions", fakeupstream.Resource{Body: gemfmt.CompactVersions(packages)})
	up.Set("/info/rake", fakeupstream.Resource{Body: gemfmt.CompactInfo("rake", packages)})
	up.Set("/gems/rake-12.3.1.gem", fakeupstream.Resource{Body: w.gem})
	path := "/artifactory/api/gems/gems-remote/gems/rake-12.3.1.gem"
	got := w.do(http.MethodGet, path, "admin", nil, "Accept", "application/x-probe")
	status(t, got, http.StatusOK)
	if !bytes.Equal(got.Body.Bytes(), w.gem) {
		t.Fatal("the gem's bytes changed")
	}
	fetched := 0
	for _, r := range up.Requests() {
		if r.Path != "/gems/rake-12.3.1.gem" {
			continue
		}
		fetched++
		if accept := r.Header.Values("Accept"); len(accept) != 0 {
			t.Fatalf("%s %s reached the upstream with Accept %q", r.Method, r.Path, accept)
		}
	}
	if fetched == 0 {
		t.Fatalf("the gem was not fetched: %v", up.Requests())
	}
	for _, accept := range [][]string{nil, {"Accept", "application/json"}} {
		again := w.do(http.MethodGet, path, "admin", nil, accept...)
		status(t, again, http.StatusOK)
		if !bytes.Equal(again.Body.Bytes(), w.gem) {
			t.Fatalf("Accept %q: the cached gem's bytes changed", accept)
		}
	}
	if after := up.Count("", "/gems/rake-12.3.1.gem"); after != fetched {
		t.Fatalf("a GET with another Accept fetched the gem %d more times", after-fetched)
	}
}
