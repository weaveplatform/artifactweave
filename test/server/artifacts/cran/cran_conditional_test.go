package cran_test

import (
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// TestAccCRANIndexAnswersConditionalsAsTheDownload: a local and a virtual
// repository's PACKAGES and PACKAGES.gz answer If-Match and
// If-Unmodified-Since as the plain request and a HEAD's Range with the
// whole index, as 7.x's download answers the index it calculates into the
// repository (#1438, #1362).
func TestAccCRANIndexAnswersConditionalsAsTheDownload(t *testing.T) {
	w := newItemWorld(t, false)
	cranPublish(t, w, "cran-local", func(*repo.Config) {},
		string(cranFixtureBytes(t, "assertthat_0.2.1.tar.gz")))
	cranRepo(t, w, repo.Config{
		Key: "cran-virtual", Class: repo.Virtual, PackageType: repo.Cran,
		Repositories: []string{"cran-local"},
	})
	for _, key := range []string{"cran-local", "cran-virtual"} {
		for _, name := range []string{cranIndex, cranIndex + ".gz"} {
			path := "/artifactory/" + key + "/" + name
			kittest.WantDownloadConditionals(t, path,
				func(method string, headers ...string) *httptest.ResponseRecorder {
					return w.do(method, path, "admin", "", headers...)
				}, true)
		}
	}
}
