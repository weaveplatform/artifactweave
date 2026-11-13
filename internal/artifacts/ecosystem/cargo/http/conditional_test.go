package cargohttp_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// TestAccIndexFileAnswersConditionalsAsTheDownload: the sparse index file
// a local repository builds for a crate answers If-Match and
// If-Unmodified-Since as the plain request and a HEAD's Range with the
// whole file, as 7.x's download answers the index file it keeps at
// index/<prefix>/<name> (RTFACT-30356) (#1438, #1362).
func TestAccIndexFileAnswersConditionalsAsTheDownload(t *testing.T) {
	f := newFixture(t, repo.Local)
	base := "/artifactory/api/cargo/cargo-test/"
	crate := archive(t, "hello_cargo", "1.0.0")
	if got := f.do(http.MethodPut, base+"api/v1/crates/new", "admin",
		publishBody(t, "hello_cargo", "1.0.0", crate)); got.Code != http.StatusOK {
		t.Fatalf("publish: %d %s", got.Code, got.Body)
	}
	path := base + "index/he/ll/hello_cargo"
	kittest.WantDownloadConditionals(t, path,
		func(method string, headers ...string) *httptest.ResponseRecorder {
			r := httptest.NewRequest(method, path, bytes.NewReader(nil))
			r.Header.Set("X-User", "admin")
			for i := 0; i+1 < len(headers); i += 2 {
				r.Header.Set(headers[i], headers[i+1])
			}
			rec := httptest.NewRecorder()
			f.router.ServeHTTP(rec, r)
			return rec
		}, true)
}
