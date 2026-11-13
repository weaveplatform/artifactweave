package swifthttp_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// TestUnitSwiftManifestAnswersConditionalsAsTheDownload: the Package.swift a
// local repository reads out of a release's archive answers If-Match and
// If-Unmodified-Since as the plain request and a HEAD's Range with the
// whole manifest, as 7.x's download answers a file (#1438, #1362). An
// If-Modified-Since after the release was published still answers 304.
func TestUnitSwiftManifestAnswersConditionalsAsTheDownload(t *testing.T) {
	f := newFixture(t, repo.Local)
	body, contentType := form(t, archive(t, "Widget/"))
	url := base + "acme/Widget/1.0.0"
	if rec := f.do(http.MethodPut, url, body, contentType, "admin"); rec.Code != http.StatusCreated {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body)
	}
	path := url + "/Package.swift"
	send := func(method string, headers ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("X-User", "admin")
		for i := 0; i+1 < len(headers); i += 2 {
			r.Header.Set(headers[i], headers[i+1])
		}
		rec := httptest.NewRecorder()
		f.r.ServeHTTP(rec, r)
		return rec
	}
	kittest.WantDownloadConditionals(t, path, send, true)
	later := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
	if rec := send(http.MethodGet, "If-Modified-Since", later); rec.Code != http.StatusNotModified {
		t.Errorf("If-Modified-Since later: %d, want 304", rec.Code)
	}
}
