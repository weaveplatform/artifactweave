package rpmhttp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/rpm"
)

// TestAccRPMVirtualRepodataAnswersConditionalsAsTheDownload: a virtual
// repository's repomd.xml and primary answer If-Match and
// If-Unmodified-Since as the plain request and a HEAD's Range with the
// whole file, as 7.x's download answers the repodata it stores in the
// virtual repository (RTFACT-14054) (#1438, #1362).
func TestAccRPMVirtualRepodataAnswersConditionalsAsTheDownload(t *testing.T) {
	w := newItemWorld(t)
	w.rpmRepos(
		repo.Config{Key: "rpm-local", Class: repo.Local},
		repo.Config{Key: "rpm-virtual", Class: repo.Virtual, Repositories: []string{"rpm-local"}},
	)
	w.publish("rpm-local", "")
	res := w.do(http.MethodGet, "/artifactory/rpm-virtual/repodata/repomd.xml", "admin", "")
	primary, _, err := rpm.DecodeRepomd(res.Body.Bytes(), "")
	if err != nil {
		t.Fatalf("virtual repomd.xml answered %d %s: %v", res.Code, res.Body, err)
	}
	for _, path := range []string{"repodata/repomd.xml", primary} {
		target := "/artifactory/rpm-virtual/" + path
		kittest.WantDownloadConditionals(t, target,
			func(method string, headers ...string) *httptest.ResponseRecorder {
				r := httptest.NewRequestWithContext(t.Context(), method, target, nil)
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
