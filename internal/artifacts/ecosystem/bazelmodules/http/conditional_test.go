package bazelmoduleshttp_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	fmtbazel "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/bazelmodules"
)

// TestAccRegistryDocumentsAnswerConditionalsAsTheDownload: the metadata.json
// and source.json a local repository builds, and a remote's MODULE.bazel and
// metadata.json, answer If-Match and If-Unmodified-Since as the plain
// request and a HEAD's Range with the whole document, as 7.x's download
// answers a file (#1438, #1362).
func TestAccRegistryDocumentsAnswerConditionalsAsTheDownload(t *testing.T) {
	up := fakeupstream.New(t, fakeupstream.Options{})
	f := newFixtureWith(t, func(db *inmem.Store, items *item.Service) *source.Service {
		return remoteService(t, db, items)
	})
	f.publish(f.local, "modules/hello/1.0.0/MODULE.bazel",
		[]byte("module(name = \"hello\", version = \"1.0.0\")\n"))
	f.publish(f.local, "modules/hello/1.0.0/hello-1.0.0.tar.gz", archive(t))
	if err := (fmtbazel.Handler{}).Reindex(t.Context(),
		format.Env{Store: f.db, Blobs: f.items.Blobs}, f.local); err != nil {
		t.Fatal(err)
	}
	c := *repo.Defaults(&repo.Config{
		Key: "bazel-remote", Class: repo.Remote, PackageType: repo.BazelModules,
		URL: up.URL + "/registry/",
	})
	if err := f.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	archiveBytes := archive(t)
	digest := sha256.Sum256(archiveBytes)
	sourceBytes, _ := json.Marshal(map[string]any{
		"type": "archive", "url": up.URL + "/registry/archives/hello.tar.gz",
		"integrity":    "sha256-" + base64.StdEncoding.EncodeToString(digest[:]),
		"strip_prefix": "hello-1.0.0",
	})
	up.Set("/registry/modules/hello/metadata.json",
		fakeupstream.Resource{Body: []byte(`{"versions":["1.0.0"]}`)})
	up.Set("/registry/modules/hello/1.0.0/MODULE.bazel",
		fakeupstream.Resource{Body: []byte("module(name = \"hello\", version = \"1.0.0\")\n")})
	up.Set("/registry/modules/hello/1.0.0/source.json", fakeupstream.Resource{Body: sourceBytes})
	up.Set("/registry/archives/hello.tar.gz", fakeupstream.Resource{Body: archiveBytes})
	for _, path := range []string{
		"/artifactory/api/bazelmodules/bazel-local/modules/hello/metadata.json",
		"/artifactory/api/bazelmodules/bazel-local/modules/hello/1.0.0/source.json",
		"/artifactory/api/bazelmodules/bazel-remote/modules/hello/metadata.json",
		"/artifactory/api/bazelmodules/bazel-remote/modules/hello/1.0.0/MODULE.bazel",
	} {
		kittest.WantDownloadConditionals(t, path,
			func(method string, headers ...string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, path, nil)
				r.Header.Set("X-User", "admin")
				for i := 0; i+1 < len(headers); i += 2 {
					r.Header.Set(headers[i], headers[i+1])
				}
				rec := httptest.NewRecorder()
				f.router.ServeHTTP(rec, r)
				return rec
			}, true)
	}
}
