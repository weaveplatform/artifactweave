package maven_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func TestAccVirtualMetadataDatabaseFaultIsInternal(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			for _, kind := range []repo.PackageType{repo.Maven, repo.Generic, repo.NPM, repo.NuGet} {
				t.Run(string(kind), func(t *testing.T) {
					w := metadataFaultWorld(t, kind, backend)
					putMetadata(t, w, "local", virtualMetadataPath, virtualMetadata("1.0"), 100)
					w.Upstream.Set("/"+virtualMetadataPath, fakeupstream.Resource{Status: http.StatusServiceUnavailable})
					path := "/artifactory/virtual/" + virtualMetadataPath
					harness.WantStatus(t, w.Request("GET", path, "admin", ""), http.StatusOK)
					if w.Upstream.Count("", "/"+virtualMetadataPath) == 0 {
						t.Fatal("upstream failure control did not reach its source")
					}
					// Reduce the fault case to one local holder. The successful control
					// above separately proves that an upstream 503 remains passable.
					root, err := w.Runtime.Store.Repos().Get(t.Context(), "virtual")
					if err != nil {
						t.Fatal(err)
					}
					root.Repositories = []string{"local"}
					if err := w.Runtime.Store.Repos().Update(t.Context(), root); err != nil {
						t.Fatal(err)
					}
					db := w.Runtime.Store.DB()
					if _, err := db.ExecContext(t.Context(), `ALTER TABLE items RENAME TO items_held`); err != nil {
						t.Fatal(err)
					}
					response := w.Request("GET", path, "admin", "")
					if _, err := db.ExecContext(t.Context(), `ALTER TABLE items_held RENAME TO items`); err != nil {
						t.Fatal(err)
					}
					t.Logf("kind=%s valid-local/upstream503=200 isolated-local-item-statement-fault=%d body=%s", kind, response.Code, response.Body.String())
					harness.WantStatus(t, response, http.StatusInternalServerError)
				})
			}
		})
	}
}

func metadataFaultWorld(t *testing.T, kind repo.PackageType, backend string) *harness.RemoteWorld {
	t.Helper()
	if backend == "sqlite" {
		return metadataWorld(t, kind)
	}
	dsn := sqltest.ScratchDSN(t, sqltest.PostgresDSN(t))
	return metadataWorld(t, kind, func(c *config.Config) {
		c.Database = config.Database{Driver: "postgres", DSN: dsn}
	})
}

type metadataFaultBlobs struct {
	blob.Store
	digest  string
	failure error
	read    bool
	closed  *int
}

func (b metadataFaultBlobs) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	if sha != b.digest {
		return b.Store.Open(ctx, sha)
	}
	if !b.read {
		return nil, blob.Info{}, b.failure
	}
	body, info, err := b.Store.Open(ctx, sha)
	if err != nil {
		return body, info, err
	}
	return &metadataFaultReader{ReadSeekCloser: body, failure: b.failure, closed: b.closed}, info, nil
}

type metadataFaultReader struct {
	io.ReadSeekCloser
	failure error
	closed  *int
}

func (r *metadataFaultReader) Read([]byte) (int, error) { return 0, r.failure }
func (r *metadataFaultReader) Close() error             { (*r.closed)++; return r.ReadSeekCloser.Close() }

func metadataFaultRouter(w *harness.RemoteWorld, blobs blob.Store) {
	w.H = harness.Router(
		harness.Config{
			Store:         w.Runtime.Store,
			Blobs:         blobs,
			Auth:          w.Runtime.Auth,
			Tokens:        w.Runtime.Tokens,
			Keys:          w.Runtime.Keys,
			RemoteSources: w.Runtime.Artifacts.RemoteSources(),
			RemoteHTTP:    w.Runtime.Artifacts.RemoteHTTP(),
		},
	)
}

func TestAccVirtualMetadataContentFailuresKeepCompatiblePassover(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			for _, kind := range []repo.PackageType{repo.Maven, repo.Generic} {
				t.Run(string(kind), func(t *testing.T) {
					for _, fault := range []string{"deleted", "open-not-found", "open-store", "open-marked-storage", "read", "read-deadline", "read-store", "read-marked-storage"} {
						for _, broken := range []string{"local", "second"} {
							t.Run(fault+"-"+broken, func(t *testing.T) { checkMetadataContentFailure(t, backend, kind, fault, broken) })
						}
					}
				})
			}
		})
	}
}

func checkMetadataContentFailure(t *testing.T, backend string, kind repo.PackageType, fault, broken string) {
	t.Helper()
	w := metadataFaultWorld(t, kind, backend)
	putMetadata(t, w, "local", virtualMetadataPath, virtualMetadata("1.0"), 100)
	putMetadata(t, w, "second", virtualMetadataPath, virtualMetadata("2.0"), 200)
	c, err := w.Runtime.Store.Repos().Get(t.Context(), "virtual")
	if err != nil {
		t.Fatal(err)
	}
	c.Repositories = []string{"local", "second"}
	if err = w.Runtime.Store.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	path := "/artifactory/virtual/" + virtualMetadataPath
	harness.WantStatus(t, w.Request("GET", path, "admin", ""), 200)
	it, err := w.Runtime.Store.Items().Get(t.Context(), broken, false, virtualMetadataPath)
	if err != nil {
		t.Fatal(err)
	}
	closed := 0
	if fault == "deleted" {
		// An unchanged Artifactweave control: the external vendor binary-not-found
		// exception hierarchy is unavailable, so this is not a captured parity claim.
		if err = w.Runtime.Blobs.Delete(t.Context(), it.Actual.SHA256); err != nil {
			t.Fatal(err)
		}
	} else {
		var cause error = kernel.ErrNotFound
		switch fault {
		case "read":
			cause = io.ErrUnexpectedEOF
		case "read-deadline":
			cause = fmt.Errorf("stored copy: %w", context.DeadlineExceeded)
		case "read-store", "open-store":
			cause = fmt.Errorf("stored copy: %w", kernel.ErrStore)
		case "read-marked-storage", "open-marked-storage":
			cause = fmt.Errorf("stored copy: %w", format.ErrStorageFault)
		}
		metadataFaultRouter(w, metadataFaultBlobs{Store: w.Runtime.Blobs, digest: it.Actual.SHA256, failure: cause, read: strings.HasPrefix(fault, "read"), closed: &closed})
	}
	healthy := "1.0"
	if broken == "local" {
		healthy = "2.0"
	}
	for _, suffix := range []string{"", ".sha1", ".md5", ".sha256"} {
		response := w.Request("GET", path+suffix, "admin", "")
		harness.WantStatus(t, response, 200)
		if suffix == "" && !strings.Contains(response.Body.String(), "<version>"+healthy+"</version>") {
			t.Fatal("healthy member lost", response.Body.String())
		}
	}
	if strings.HasPrefix(fault, "read") && closed != 4 {
		t.Fatal("failed content readers not closed", closed)
	}
	c.Repositories = []string{broken}
	if err = w.Runtime.Store.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "HEAD"} {
		harness.WantStatus(t, w.Request(method, path, "admin", ""), 404)
	}
	if strings.HasPrefix(fault, "read") && closed != 6 {
		t.Fatal("isolated failed content readers not closed", closed)
	}
}
