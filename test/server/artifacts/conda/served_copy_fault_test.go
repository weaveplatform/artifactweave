package conda_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	condafmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/conda"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/blobtest"
)

// errStoreTimeout is a blob store's read that timed out, as a cloud
// store's client wraps its deadline.
var errStoreTimeout = fmt.Errorf("blob read timed out: %w", context.DeadlineExceeded)

// timedOutRead is a served copy whose reads time out.
type timedOutRead struct{}

func (timedOutRead) Read([]byte) (int, error) { return 0, errStoreTimeout }
func (timedOutRead) Close() error             { return nil }

// A Conda remote's repodata.json reads the copy the source service serves
// it, a stream or a cached item. A failed read of it is the server's
// storage fault, 500 "internal error", logged once, even when the store's
// error wraps its timeout, which answered 504 "Gateway Timeout" as though
// the upstream had timed out (#1462).
func TestAccCondaRemoteIndexAnswersAFailedReadOfTheServedCopyAsAServerFault(t *testing.T) {
	encoded, err := condafmt.EncodeRepodata(condafmt.EmptyRepodata("noarch"), "", "noarch")
	if err != nil {
		t.Fatal(err)
	}
	index := encoded["repodata.json"]
	cached := fmt.Sprintf("%x", sha256.Sum256(index))
	served := "stream"
	sources := &source.Service{Resolve: sourceIdentity, HTTP: sourceBackend(
		func(context.Context, source.Request, source.Permit) (source.Result, error) {
			if served == "item" {
				return source.Result{Item: &artmodel.Item{
					RepoKey: "conda-remote", Path: "noarch", Name: "repodata.json", Cached: true,
					Actual: kernel.Checksums{SHA256: cached},
				}}, nil
			}
			return source.Result{Stream: &source.Stream{Body: timedOutRead{}, Status: http.StatusOK}}, nil
		},
	)}
	var logs bytes.Buffer
	blobs := &blobtest.ReadFaults{Err: errStoreTimeout}
	w := newItemWorld(t, false, func(c *Config) {
		c.RemoteSources = sources
		blobs.Store = c.Blobs
		c.Blobs = blobs
		c.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	})
	wantStatus(t, w.do(http.MethodPut, "/artifactory/other-local/conda-cached-index", "admin", string(index)),
		http.StatusCreated)
	remote := *repo.Defaults(&repo.Config{
		Key: "conda-remote", Class: repo.Remote, PackageType: repo.Conda, URL: "https://up.example",
	})
	if err := w.db.Repos().Create(t.Context(), remote); err != nil {
		t.Fatal(err)
	}
	blobs.Cut(cached, 0)
	for _, served = range []string{"stream", "item"} {
		logs.Reset()
		got := w.do(http.MethodGet, "/artifactory/api/conda/conda-remote/noarch/repodata.json", "admin", "")
		if got.Code != http.StatusInternalServerError ||
			got.Body.String() != `{"errors":[{"message":"internal error","status":500}]}`+"\n" {
			t.Errorf("%s: %d %q", served, got.Code, got.Body)
		}
		if n := strings.Count(logs.String(), errStoreTimeout.Error()); n != 1 {
			t.Errorf("%s: the fault was logged %d times, want once:\n%s", served, n, logs.String())
		}
	}
}
