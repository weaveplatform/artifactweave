package alpinehttp

import (
	"net/http/httptest"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// TestAccAlpineIndexAnswersConditionalsAsTheDownload: a local and a virtual
// repository's APKINDEX.tar.gz answers If-Match and If-Unmodified-Since as
// the plain request and a HEAD's Range with the whole index, as 7.x's
// download answers the index it calculates into the repository (#1438,
// #1362).
func TestAccAlpineIndexAnswersConditionalsAsTheDownload(t *testing.T) {
	w := newItemWorld(t)
	w.repos(
		alpineRepo(alpineLocal, repo.Local),
		alpineRepo(alpineVirtual, repo.Virtual, alpineLocal),
	)
	w.put(alpineLocal, apkIndexPath, apkIndex(t, "hello"))
	w.put(alpineLocal, apkHelloPath, apkBody)
	for _, key := range []string{alpineLocal, alpineVirtual} {
		path := "/artifactory/" + key + "/" + apkIndexPath
		kittest.WantDownloadConditionals(t, path,
			func(method string, headers ...string) *httptest.ResponseRecorder {
				r := httptest.NewRequestWithContext(t.Context(), method, path, nil)
				for i := 0; i+1 < len(headers); i += 2 {
					r.Header.Set(headers[i], headers[i+1])
				}
				r = r.WithContext(accessapi.WithCaller(r.Context(), kittest.Admin,
					kittest.AdminAccess().Decider))
				rec := httptest.NewRecorder()
				w.kit.GetItem(rec, r)
				return rec
			}, true)
	}
}
