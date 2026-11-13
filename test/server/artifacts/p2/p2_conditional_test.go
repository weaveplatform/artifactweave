package p2_test

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// TestAccP2MetadataAnswersConditionalsAsTheDownload: a virtual
// repository's composed p2.index and compositeContent.xml, an entry a
// remote reads from the archive its URL names, and a remote's rewritten
// composite answer If-Match and If-Unmodified-Since as the plain request
// and a HEAD's Range with the whole file, as 7.x's download answers a file
// (#1438, #1362).
func TestAccP2MetadataAnswersConditionalsAsTheDownload(t *testing.T) {
	index := p2Fixture(t, "p2.index")
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	f, err := zw.Create("p2.index")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(index); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	var cached artmodel.Item
	backend := sourceBackend(
		func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			switch {
			case q.Repo.Key == "p2-store" && q.Resource.Path == "p2.index":
				return source.Result{Item: &cached}, nil
			case q.Repo.Key == "p2-external" && q.Resource.Path == "compositeContent.xml":
				body := []byte(p2ExternalComposite)
				return source.Result{
					Metadata: source.MetadataResponse{
						BaseURL: "https://root.example/site/compositeContent.xml",
					},
					Stream: &source.Stream{
						Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
						Representation: source.Representation{Size: int64(len(body))},
					},
				}, nil
			}
			return source.Result{}, kernel.ErrNotFound
		},
	)
	w := newItemWorld(t, false, func(c *Config) {
		c.RemoteSources = &source.Service{Resolve: sourceIdentity, HTTP: backend, Cache: backend}
	})
	if res := w.do(
		http.MethodPut,
		"/artifactory/libs-local/site.zip",
		"admin",
		archive.String(),
	); res.Code != http.StatusCreated {
		t.Fatalf("seed archive: %d %s", res.Code, res.Body)
	}
	cached = w.item("libs-local", false, "site.zip")
	cached.RepoKey, cached.Cached, cached.Path, cached.Name = "p2-store", true, "", "p2.index"
	for _, c := range []repo.Config{
		{Key: "p2-store", Class: repo.Remote, PackageType: repo.P2, URL: "https://eclipse.example/site.zip!/"},
		{
			Key: "p2-external", Class: repo.Remote, PackageType: repo.P2,
			URL: "https://root.example/site/", P2ChildOrigins: []string{"https://child.example"},
		},
		{Key: "p2-virtual", Class: repo.Virtual, PackageType: repo.P2},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{
		"/artifactory/p2-virtual/p2.index",
		"/artifactory/p2-virtual/compositeContent.xml",
		"/artifactory/p2-store/p2.index",
		"/artifactory/p2-external/compositeContent.xml",
	} {
		kittest.WantDownloadConditionals(t, path,
			func(method string, headers ...string) *httptest.ResponseRecorder {
				return w.do(method, path, "admin", "", headers...)
			}, true)
	}
}
