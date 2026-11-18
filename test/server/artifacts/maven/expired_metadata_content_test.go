package maven_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	mavenfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/maven"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

func TestAccMavenExpiredMetadataKeepsContentFailurePassover(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		for _, kind := range []repo.PackageType{repo.Maven, repo.Generic} {
			for _, mode := range []string{"healthy", "blob", "entry", "clock"} {
				t.Run(fmt.Sprint(backend, "/", kind, "/", mode), func(t *testing.T) {
					w := metadataFaultWorld(t, kind, backend)
					putMetadata(t, w, "second", virtualMetadataPath, virtualMetadata("2.0"), 100)
					w.Repo.RetrievalCachePeriodSecs = new(0)
					w.Repo.PriorityResolution = repo.Flex(true)
					w.Update()
					c, err := w.Runtime.Store.Repos().Get(t.Context(), "virtual")
					if err != nil {
						t.Fatal(err)
					}
					c.Repositories = []string{"remote", "second"}
					if err = w.Runtime.Store.Repos().Update(t.Context(), c); err != nil {
						t.Fatal(err)
					}
					w.Upstream.Set("/"+virtualMetadataPath, fakeupstream.Resource{Body: []byte(virtualMetadata("1.0"))})
					path := "/artifactory/remote/" + virtualMetadataPath
					rec := w.Request(http.MethodGet, "/artifactory/virtual/"+virtualMetadataPath, "admin", "")
					if rec.Code != 200 {
						t.Fatal(rec.Code, rec.Body)
					}
					entries, _, err := w.Runtime.Store.RemoteCache().Exact(t.Context(), "remote", virtualMetadataPath, kernel.Page{Limit: 10})
					if err != nil || len(entries) != 1 || entries[0].ItemID != 0 {
						t.Fatal("raw cached metadata", entries, err)
					}
					before := entries[0]
					w.Upstream.Set("/"+virtualMetadataPath, fakeupstream.Resource{Status: 404})
					switch mode {
					case "blob":
						if err = w.Runtime.Blobs.Delete(t.Context(), before.Blob.Checksums.SHA256); err != nil {
							t.Fatal(err)
						}
						if _, err = w.Runtime.Blobs.Stat(t.Context(), before.Blob.Checksums.SHA256); !errors.Is(err, blob.ErrNotFound) {
							t.Fatal("real blob deletion did not fail Stat", err)
						}
					case "entry":
						if _, err = w.Runtime.Store.DB().ExecContext(t.Context(), "ALTER TABLE remote_cache RENAME TO remote_cache_held"); err != nil {
							t.Fatal(err)
						}
					case "clock":
						p := w.Runtime.Artifacts.RemoteSources().HTTP.(*mavenfmt.MavenBackend).Policy
						p.Store = retainedSQLFault{Store: p.Store, db: w.Runtime.Store}
					}
					direct := w.Request(http.MethodGet, path, "admin", "")
					virtual := w.Request(http.MethodGet, "/artifactory/virtual/"+virtualMetadataPath, "admin", "")
					if mode == "entry" {
						if _, err = w.Runtime.Store.DB().ExecContext(t.Context(), "ALTER TABLE remote_cache_held RENAME TO remote_cache"); err != nil {
							t.Fatal(err)
						}
					}
					t.Logf("RETAINED_METADATA_PHASE backend=%s mode=%s direct=%d virtual=%d body=%q", backend, mode, direct.Code, virtual.Code, virtual.Body.String())
					wantDirect, wantVirtual := 500, 500
					if mode == "healthy" {
						wantDirect, wantVirtual = 200, 200
					}
					if mode == "blob" {
						wantVirtual = 200
					}
					if kind == repo.Generic && mode != "entry" {
						// The Generic direct item route selects an artifact-kind
						// cache identity, not the metadata-kind representation
						// primed by the virtual interceptor. Its cold origin miss
						// must not alias that raw metadata; the virtual file read
						// below still exercises retention/content/clock behavior.
						wantDirect = http.StatusNotFound
					}
					if direct.Code != wantDirect || virtual.Code != wantVirtual {
						t.Errorf("phase contract: direct%d/%d virtual%d/%d", direct.Code, wantDirect, virtual.Code, wantVirtual)
					}
					if mode == "blob" && virtual.Code == 200 && !strings.Contains(virtual.Body.String(), "<version>2.0</version>") {
						t.Error("healthy later local holder lost")
					}
					if mode == "healthy" && virtual.Code == 200 && !strings.Contains(virtual.Body.String(), "<version>1.0</version>") {
						t.Error("repository metadata file inherited API miss behavior", virtual.Body.String())
					}
					after, err := w.Runtime.Store.RemoteCache().Read(t.Context(), before.Token.Key)
					if err != nil {
						t.Fatal(err)
					}
					if mode != "healthy" && (!after.ValidatedAt.Equal(before.ValidatedAt) || !after.NegativeUntil.IsZero()) {
						t.Fatal("fault changed retained clock/negative", after)
					}
				})
			}
		}
	}
}
