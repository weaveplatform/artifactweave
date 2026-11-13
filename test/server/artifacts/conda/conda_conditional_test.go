package conda_test

import (
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// A Conda index the route builds, repodata.json and notices.json, answers
// If-Match and If-Unmodified-Since as the plain request and a HEAD's Range
// with the whole document, as 7.x's download answers the repodata.json it
// calculates into the repository (#1438, #1362).
func TestAccCondaIndexAnswersConditionalsAsTheDownload(t *testing.T) {
	w := newItemWorld(t, false)
	if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
		Key: "conda-local", Class: repo.Local, PackageType: repo.Conda,
	})); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"repodata.json", "notices.json"} {
		path := "/artifactory/api/conda/conda-local/noarch/" + name
		kittest.WantDownloadConditionals(t, path,
			func(method string, headers ...string) *httptest.ResponseRecorder {
				return w.do(method, path, "admin", "", headers...)
			}, true)
	}
}
