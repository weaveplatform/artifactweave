package cocoapodshttp

import (
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	podfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/cocoapods"
)

// TestUnitCDNDocumentsAnswerConditionalsAsTheDownload: the CDN documents a
// local and a virtual repository build (CocoaPods-version.yml, all_pods.txt,
// a shard and a podspec) answer If-Match and If-Unmodified-Since as the
// plain request and a HEAD's Range with the whole document, as 7.x's
// download answers a file (#1438, #1362). The ETag they carry still answers
// If-None-Match.
func TestUnitCDNDocumentsAnswerConditionalsAsTheDownload(t *testing.T) {
	f := newRouteFixture(t)
	f.seedLocal("LocalPod", "1.0.0")
	for _, key := range []string{"pods-local", "pods-virtual"} {
		for _, name := range []string{
			"CocoaPods-version.yml", "all_pods.txt",
			podfmt.ShardFile("LocalPod"), podfmt.SpecPath("LocalPod", "1.0.0"),
		} {
			path := "/artifactory/api/pods/" + key + "/" + name
			send := func(method string, headers ...string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, path, nil)
				r.Header.Set("X-User", "admin")
				for i := 0; i+1 < len(headers); i += 2 {
					r.Header.Set(headers[i], headers[i+1])
				}
				rec := httptest.NewRecorder()
				f.router.ServeHTTP(rec, r)
				return rec
			}
			kittest.WantDownloadConditionals(t, path, send, true)
			if etag := send("GET").Header().Get("ETag"); etag == "" ||
				send("GET", "If-None-Match", etag).Code != 304 {
				t.Errorf("%s: If-None-Match %q no longer answers 304", path, etag)
			}
		}
	}
}
