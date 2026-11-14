package p2_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/blobtest"
)

// errStoreRead is a blob store's failed read of a copy it opened.
var errStoreRead = errors.New("p2 test: the store failed to read the copy")

// failedRead is a served copy whose reads fail as the store's do.
type failedRead struct{}

func (failedRead) Read([]byte) (int, error) { return 0, errStoreRead }
func (failedRead) Close() error             { return nil }

// An archive-backed P2 remote reads the whole site archive the source
// service serves it, a stream or a cached item, before it extracts a file.
// That copy is the server's own, so a failed read of it is its storage
// fault, 500 "internal error", logged once; the route answered 502 "Bad
// Gateway", as for an archive that does not open (#1488).
func TestAccP2ArchiveAnswersAFailedReadOfTheServedCopyAsAServerFault(t *testing.T) {
	var cached artmodel.Item
	served := "stream"
	backend := sourceBackend(
		func(context.Context, source.Request, source.Permit) (source.Result, error) {
			if served == "item" {
				return source.Result{Item: &cached}, nil
			}
			return source.Result{Stream: &source.Stream{
				Body:           failedRead{},
				Status:         http.StatusOK,
				Representation: source.Representation{Size: 10},
			}}, nil
		},
	)
	var logs bytes.Buffer
	blobs := &blobtest.ReadFaults{Err: errStoreRead}
	w := newItemWorld(t, false, func(c *Config) {
		c.RemoteSources = &source.Service{Resolve: sourceIdentity, HTTP: backend, Cache: backend}
		blobs.Store = c.Blobs
		c.Blobs = blobs
		c.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	})
	if res := w.do(http.MethodPut, "/artifactory/libs-local/site.zip", "admin",
		"archive bytes"); res.Code != http.StatusCreated {
		t.Fatalf("seed archive: %d %s", res.Code, res.Body)
	}
	cached = w.item("libs-local", false, "site.zip")
	cached.RepoKey, cached.Cached, cached.Path, cached.Name = "p2-store", true, "", "p2.index"
	if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
		Key: "p2-store", Class: repo.Remote, PackageType: repo.P2,
		URL: "https://eclipse.example/site.zip!/",
	})); err != nil {
		t.Fatal(err)
	}
	blobs.Cut(cached.Actual.SHA256, 0)
	for _, served = range []string{"stream", "item"} {
		logs.Reset()
		got := w.do(http.MethodGet, "/artifactory/p2-store/p2.index", "admin", "")
		if got.Code != http.StatusInternalServerError ||
			got.Body.String() != `{"errors":[{"message":"internal error","status":500}]}`+"\n" {
			t.Errorf("%s: %d %q", served, got.Code, got.Body)
		}
		if n := strings.Count(logs.String(), errStoreRead.Error()); n != 1 {
			t.Errorf("%s: the fault was logged %d times, want once:\n%s", served, n, logs.String())
		}
	}
}
