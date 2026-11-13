package bazelmoduleshttp_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	bazelmoduleshttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/bazelmodules/http"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/blobtest"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
)

// A Bazel remote's metadata.json, MODULE.bazel and source.json read the
// cached copy the source service serves them. A failed read of it, as the
// cache checks it or as the route reads it, is the server's storage fault:
// 500 "internal error", logged once, where the route's read answered 502
// (#1462). Once the store reads again the route answers from the kept copy,
// without fetching from the upstream.
func TestAccRemoteBazelAnswersAFailedReadOfTheServedCopyAsAServerFault(t *testing.T) {
	up := fakeupstream.New(t, fakeupstream.Options{})
	blobs := &blobtest.ReadFaults{}
	var svc *source.Service
	f := newFixtureWith(t, func(db *inmem.Store, items *item.Service) *source.Service {
		blobs.Store = items.Blobs
		items.Blobs = blobs
		svc = remoteService(t, db, items)
		return svc
	})
	// The fixture's router drops the kit's faults; this one, over the same
	// store and remote service, keeps them.
	var faults []error
	r := chi.NewRouter()
	r.Use(baseurl.Middleware("http://registry.example"))
	r.Use(accesstest.Over(t, f.db).Middleware(t, true, func(*http.Request) kernel.Principal {
		return kernel.Principal{Name: "admin", Admin: true}
	}))
	pkgroute.Mount(r, pkgroute.Options{
		Store: f.db, Items: func() *item.Service { return f.items },
		Fault: func(_ *http.Request, err error) { faults = append(faults, err) },
	}, bazelmoduleshttp.Protocol(svc))
	f.router = r
	c := *repo.Defaults(&repo.Config{
		Key: "bazel-remote", Class: repo.Remote, PackageType: repo.BazelModules, URL: up.URL + "/registry/",
	})
	if err := f.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(archive(t))
	sourceDoc, _ := json.Marshal(map[string]any{
		"type": "archive", "url": up.URL + "/registry/archives/hello.tar.gz",
		"integrity":    "sha256-" + base64.StdEncoding.EncodeToString(digest[:]),
		"strip_prefix": "hello-1.0.0",
	})
	up.Set(
		"/registry/modules/hello/metadata.json",
		fakeupstream.Resource{Body: []byte(`{"versions":["1.0.0"]}`)},
	)
	up.Set("/registry/modules/hello/1.0.0/MODULE.bazel",
		fakeupstream.Resource{Body: []byte("module(name = \"hello\", version = \"1.0.0\")\n")})
	up.Set("/registry/modules/hello/1.0.0/source.json", fakeupstream.Resource{Body: sourceDoc})
	fetches := func() int {
		n := 0
		for _, r := range up.Requests() {
			if r.Method == http.MethodGet {
				n++
			}
		}
		return n
	}
	root := "/artifactory/api/bazelmodules/" + c.Key + "/modules/hello/"
	for _, tail := range []string{"metadata.json", "1.0.0/MODULE.bazel", "1.0.0/source.json"} {
		if w := f.request(http.MethodGet, root+tail); w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", tail, w.Code, w.Body)
		}
		fetched := fetches()
		failed := 0
		for spare := 0; spare < 50; spare++ {
			blobs.CutEvery(spare)
			faults = nil
			w := f.request(http.MethodGet, root+tail)
			if w.Code == http.StatusOK {
				break
			}
			if w.Code != http.StatusInternalServerError ||
				w.Body.String() != `{"errors":[{"message":"internal error","status":500}]}`+"\n" {
				t.Fatalf("%s, %d sound opens: %d %q", tail, spare, w.Code, w.Body)
			}
			if len(faults) != 1 || !errors.Is(faults[0], blobtest.ErrDropped) {
				t.Fatalf("%s, %d sound opens: faults %v", tail, spare, faults)
			}
			failed++
		}
		blobs.Mend()
		if failed == 0 {
			t.Fatalf("%s: no read failed", tail)
		}
		if got := fetches(); got != fetched {
			t.Fatalf("%s: the upstream was fetched %d times, want %d", tail, got, fetched)
		}
	}
}
