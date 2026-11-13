package gems_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
)

// TestAccGemsMetadataAnswersConditionalsAsTheDownload: the compact index
// (names, versions, info) and the JSON API a local and a virtual repository
// build per request answer If-Match and If-Unmodified-Since as the plain
// request and a HEAD's Range with the whole document, as 7.x's download
// answers a file (#1438, #1362). Bundler's ranged GET of versions keeps its
// 206.
func TestAccGemsMetadataAnswersConditionalsAsTheDownload(t *testing.T) {
	w := newWorld(t)
	status(t, w.do(http.MethodPut, "/artifactory/gems-local/gems/rake-12.3.1.gem", "admin", w.gem),
		http.StatusCreated)
	for _, key := range []string{"gems-local", "gems-virtual"} {
		for _, name := range []string{"names", "versions", "info/rake", "api/v1/versions/rake.json"} {
			path := "/artifactory/api/gems/" + key + "/" + name
			kittest.WantDownloadConditionals(t, path,
				func(method string, headers ...string) *httptest.ResponseRecorder {
					return w.do(method, path, "reader", nil, headers...)
				}, true)
		}
	}
}
