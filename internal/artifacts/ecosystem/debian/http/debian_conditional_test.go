package debianhttp

import (
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// TestAccDebianIndexAnswersConditionalsAsTheDownload: a local repository's
// Release and Packages, and a virtual repository's composed ones, answer
// If-Match and If-Unmodified-Since as the plain request and a HEAD's Range
// with the whole document, as 7.x's download answers the Debian metadata
// it stores in the repository, a virtual one's included (RTDEV-50452)
// (#1438, #1362).
func TestAccDebianIndexAnswersConditionalsAsTheDownload(t *testing.T) {
	w := newOverlayWorld(t)
	w.debRepos(
		repo.Config{Key: "deb-local", Class: repo.Local},
		repo.Config{Key: "deb-virtual", Class: repo.Virtual, Repositories: []string{"deb-local"}},
	)
	w.publish("deb-local", packagesIndex("alpha 1.0 pool/alpha_1.0_amd64.deb"))
	for _, key := range []string{"deb-local", "deb-virtual"} {
		for _, path := range []string{debRelease, debPackages} {
			kittest.WantDownloadConditionals(t, key+"/"+path,
				func(method string, headers ...string) *httptest.ResponseRecorder {
					r := w.request(key, path, "admin")
					r.Method = method
					for i := 0; i+1 < len(headers); i += 2 {
						r.Header.Set(headers[i], headers[i+1])
					}
					rec := httptest.NewRecorder()
					w.kit.GetItem(rec, r)
					return rec
				}, true)
		}
	}
}
