package composerhttp

import (
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// TestAccComposerMetadataAnswersConditionalsAsTheDownload: packages.json and
// a package's p2 metadata, which a local and a virtual repository build per
// request, answer If-Match and If-Unmodified-Since as the plain request and
// a HEAD's Range with the whole document, as 7.x's download answers a file
// (#1438, #1362). The ETag they carry still answers If-None-Match.
func TestAccComposerMetadataAnswersConditionalsAsTheDownload(t *testing.T) {
	f := newFixture(t)
	f.deploy(f.create("php", repo.Local), "1.2.3", "first")
	f.create("all", repo.Virtual, "php")
	for _, key := range []string{"php", "all"} {
		for _, name := range []string{"packages.json", "p2/acme/widget.json"} {
			path := "/artifactory/api/composer/" + key + "/" + name
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
