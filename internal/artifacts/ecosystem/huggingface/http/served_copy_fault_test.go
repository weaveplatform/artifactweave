package huggingfacehttp_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/blobtest"
)

// errStoreTimeout is a blob store's read that timed out, as a cloud
// store's client wraps its deadline.
var errStoreTimeout = fmt.Errorf("blob read timed out: %w", context.DeadlineExceeded)

// timedOutRead is a served copy whose reads time out.
type timedOutRead struct{}

func (timedOutRead) Read([]byte) (int, error) { return 0, errStoreTimeout }
func (timedOutRead) Close() error             { return nil }

// A remote model's revision info reads the copy the source service serves
// it, a stream or a cached item. A failed read of it is the server's
// storage fault: 500 "internal error", logged once as one, even when the
// store's error wraps its timeout. The route answered 500 before, since it
// maps no source error, but logged a bare read error (#1462).
func TestUnitRemoteInfoAnswersAFailedReadOfTheServedCopyAsAServerFault(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unexpected upstream request: "+r.URL.Path, http.StatusBadGateway)
	}))
	defer up.Close()
	var sources *source.Service
	f := newFixture(t, repo.Remote, func(f *fixture) *source.Service {
		sources = remoteSetup(t, up.URL, 3600)(f)
		return sources
	})
	c, err := f.db.Repos().Get(t.Context(), "hf")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := f.items.Deploy(t.Context(), item.Deploy{
		Target: item.Target{Repo: c, Cached: true, Path: "models--acme--demo/api/main.json"},
		Body: bytes.NewBufferString(
			fmt.Sprintf(`{"id":"acme/demo","sha":"%s","siblings":[]}`, sha),
		),
		By: "source",
	})
	if err != nil {
		t.Fatal(err)
	}
	blobs := &blobtest.ReadFaults{Store: f.items.Blobs, Err: errStoreTimeout}
	f.items.Blobs = blobs
	blobs.Cut(stored.Actual.SHA256, 0)
	for name, result := range map[string]source.Result{
		"stream": {Stream: &source.Stream{Body: timedOutRead{}, Status: http.StatusOK}},
		"item":   {Item: &stored},
	} {
		sources.HTTP = sourceBackendFunc(
			func(context.Context, source.Request, source.Permit) (source.Result, error) {
				return result, nil
			},
		)
		r := f.request(http.MethodGet, "api/models/acme/demo/revision/main", "", nil)
		b := readResponse(t, r)
		if r.StatusCode != http.StatusInternalServerError ||
			string(b) != `{"errors":[{"message":"internal error","status":500}]}`+"\n" {
			t.Errorf("%s: %d %q", name, r.StatusCode, b)
		}
		faults := f.takeFaults()
		if len(faults) != 1 || !errors.Is(faults[0], format.ErrStorageFault) ||
			!errors.Is(faults[0], errStoreTimeout) {
			t.Errorf("%s: faults %v", name, faults)
		}
	}
}
