package artifacts_test

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/build"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// TestAccBuildUploadAnswers204WithTheChecksum: a build upload answers 204
// with no body and the stored document's SHA-256 in X-Checksum-Sha256,
// whether the project's build-info repository is local or federated
// (#1381). JFrog's uploadbuild page documents 200, but no source records
// 7.x moving from the 204 that Artifactory 6.20's BuildResource.addBuild
// answers, so the 204 stays until P19 compares it with 7.x. The JFrog CLI
// takes either, and reads the header for --detailed-summary.
func TestAccBuildUploadAnswers204WithTheChecksum(t *testing.T) {
	w := newItemWorld(t, false)
	fed := repo.Config{Key: "proj-build-info", Class: repo.Federated}
	if err := w.db.Repos().Create(context.Background(), *repo.Defaults(&fed)); err != nil {
		t.Fatal(err)
	}
	for i, c := range []struct{ query, project string }{
		{"", ""},
		{"?buildRepo=artifactory-build-info", ""},
		{"?project=proj", "proj"},
		{"?project=proj&buildRepo=proj-build-info", "proj"},
	} {
		number := strconv.Itoa(i + 1)
		doc := buildDoc("app", number, testStarted)
		rec := w.do(http.MethodPut, buildsPath+c.query, "admin", doc, "Content-Type", vendorJSON)
		if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 ||
			rec.Header().Get("X-Checksum-Sha256") != sha256Hex(doc) {
			t.Fatalf("%q: %d %q, X-Checksum-Sha256 %q", c.query, rec.Code, rec.Body,
				rec.Header().Get("X-Checksum-Sha256"))
		}
		// The checksum is that of the document stored as an item in the
		// project's build-info repository.
		started, err := build.ParseStarted(testStarted)
		if err != nil {
			t.Fatal(err)
		}
		key, p := artmodel.Build{
			ProjectKey: c.project, Name: "app", Number: number, Started: started,
		}.DocumentItem()
		if it := w.item(key, false, p); it.Actual.SHA256 != sha256Hex(doc) {
			t.Errorf("%q: %s/%s holds %s", c.query, key, p, it.Actual.SHA256)
		}
		// The acceptance scenario finds the document by the checksum jf rt
		// bp --detailed-summary prints; the search must find it here too.
		rec = w.do(
			http.MethodGet,
			"/artifactory/api/search/checksum?sha256="+sha256Hex(doc),
			"admin",
			"",
		)
		if want := "/artifactory/api/storage/" + key + "/" + p; rec.Code != http.StatusOK ||
			!strings.Contains(rec.Body.String(), want) {
			t.Errorf("%q: checksum search %d %s, want %s", c.query, rec.Code, rec.Body, want)
		}
	}
}
