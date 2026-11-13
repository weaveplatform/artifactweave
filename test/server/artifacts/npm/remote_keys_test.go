package npm_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// The assembled server answers an npm remote repository's keys document
// with its upstream's, through the application's remote sources: read
// permission at .npm/keys.json first, then the remote's download policy
// there, and only then the upstream.
func TestAccNpmRemoteKeysThroughTheServer(t *testing.T) {
	w := harness.NewRemoteWorld(t)
	w.Repo = *repo.Defaults(&repo.Config{
		Key: w.Repo.Key, Class: repo.Remote, PackageType: repo.NPM, URL: w.Upstream.URL,
	})
	w.Update()
	const doc = `{"keys":[{"expires":null,"keyid":"SHA256:upstream","keytype":"ecdsa-sha2-nistp256",` +
		`"scheme":"ecdsa-sha2-nistp256","key":"MFkw"}]}`
	w.Upstream.Set("/-/npm/v1/keys", fakeupstream.Resource{
		Body: []byte(doc), Header: http.Header{"Content-Type": []string{"application/json"}},
	})
	keys := npmBase + w.Repo.Key + "/-/npm/v1/keys"

	for _, c := range []struct {
		user   string
		status int
		body   string
	}{
		{"denied", http.StatusForbidden, `{"error":"Forbidden"}`},
		{"", http.StatusUnauthorized, ""},
	} {
		rec := w.Request(http.MethodGet, keys, c.user, "")
		if rec.Code != c.status || c.body != "" && strings.TrimSpace(rec.Body.String()) != c.body {
			t.Errorf("%q: %d %s", c.user, rec.Code, rec.Body)
		}
	}
	w.Repo.ExcludesPattern = new(".npm/keys.json")
	w.Update()
	rec := w.Request(http.MethodGet, keys, "reader", "")
	if want := `{"error":"The repository 'remote' rejected the artifact 'remote:.npm/keys.json'` +
		` due to its include/exclude pattern settings."}`; rec.Code != http.StatusNotFound ||
		strings.TrimSpace(rec.Body.String()) != want {
		t.Errorf("excluded: %d %s", rec.Code, rec.Body)
	}
	if n := len(w.Upstream.Requests()); n != 0 {
		t.Fatalf("a refused read asked the upstream %d times", n)
	}

	w.Repo.ExcludesPattern = new("")
	w.Update()
	rec = w.Request(http.MethodGet, keys, "reader", "")
	if rec.Code != http.StatusOK || rec.Body.String() != doc ||
		rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("keys: %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
}
