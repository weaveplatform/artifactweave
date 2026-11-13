package opkg_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/opkg"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// TestAccOpkgFeedAnswersConditionalsAsTheDownload: a local feed's Packages
// and a virtual repository's composed Packages and Release answer If-Match
// and If-Unmodified-Since as the plain request and a HEAD's Range with the
// whole file, as 7.x's download answers the feed it calculates into the
// repository (#1438, #1362).
func TestAccOpkgFeedAnswersConditionalsAsTheDownload(t *testing.T) {
	w := newItemWorld(t, false)
	for _, c := range []repo.Config{
		{Key: "opkg-local", Class: repo.Local, PackageType: repo.Opkg},
		{Key: "opkg-virtual", Class: repo.Virtual, PackageType: repo.Opkg, Repositories: []string{"opkg-local"}},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	ipk := repopath.Read(t, "internal/artifacts/ecosystem/opkg/testdata/464xlat_13_x86_64.ipk")
	harness.WantStatus(t, w.do(http.MethodPut, "/artifactory/opkg-local/feed/464xlat_13_x86_64.ipk",
		"admin", string(ipk)), http.StatusCreated)
	items := &item.Service{Store: w.db, Blobs: w.blobs, Formats: ecosystem.For}
	if err := (opkg.Handler{}).Work(t.Context(), items.Env(),
		work.WorkItem{Kind: opkg.KindIndex, Key: "opkg-local"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/artifactory/opkg-local/feed/Packages",
		"/artifactory/opkg-virtual/feed/Packages",
		"/artifactory/opkg-virtual/feed/Release",
	} {
		kittest.WantDownloadConditionals(t, path,
			func(method string, headers ...string) *httptest.ResponseRecorder {
				return w.do(method, path, "admin", "", headers...)
			}, true)
	}
}
