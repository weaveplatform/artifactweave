package bowerhttp

import (
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// TestUnitBowerArchiveAnswersConditionalsAsTheDownload: the archive a local
// and a virtual repository build of a version's stored files answers
// If-Match and If-Unmodified-Since as the plain request and a HEAD's Range
// with the whole archive, as 7.x's download answers a file (#1438, #1362).
func TestUnitBowerArchiveAnswersConditionalsAsTheDownload(t *testing.T) {
	f := newFixture(t)
	l := f.repo("bower-local", repo.Local)
	_ = f.repo("bower-virtual", repo.Virtual, l.Key)
	f.deploy(l, "hello", "1.0.0", "bower.json", `{"name":"hello","version":"1.0.0"}`)
	f.deploy(l, "hello", "1.0.0", "lib/index.js", "from-"+l.Key)
	for _, key := range []string{l.Key, "bower-virtual"} {
		path := "/artifactory/api/bower/" + key + "/binaries/" + key + "/hello.git/v1.0.0"
		kittest.WantDownloadConditionals(t, path,
			func(method string, headers ...string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, path, nil)
				r.Header.Set("X-User", "admin")
				for i := 0; i+1 < len(headers); i += 2 {
					r.Header.Set(headers[i], headers[i+1])
				}
				rec := httptest.NewRecorder()
				f.router.ServeHTTP(rec, r)
				return rec
			}, true)
	}
}
