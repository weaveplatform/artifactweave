package luarockshttp_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/blobtest"
)

// A remote manifest the cache keeps, whose copy the server then fails to
// read, answers 500 "internal error" and logs the store's error once,
// wherever the read fails: as the cache checks the copy, or as the route
// reads the copy the cache serves it to answer. That copy is the server's
// own, not the upstream's, and the route's read answered 502 "Bad Gateway"
// (#1462). The cached manifest is kept, so once the store reads again it
// answers without the upstream.
func TestAccRemoteManifestAnswersAFailedReadOfTheServedCopyAsAServerFault(t *testing.T) {
	blobs := &blobtest.ReadFaults{}
	var faults []error
	f := newRemoteFixture(t, func(o *pkgroute.Options, items *item.Service) {
		blobs.Store = items.Blobs
		items.Blobs = blobs
		o.Fault = func(_ *http.Request, err error) { faults = append(faults, err) }
	})
	f.seed()
	wantBody(t, f.do(http.MethodGet, remoteBase+"manifest", nil, "", "admin"), []string{`"1.0-1"`}, nil)
	failed := 0
	for spare := 0; ; spare++ {
		blobs.Cut(cachedSHA(t, f, "manifest"), spare)
		rec := f.do(http.MethodGet, remoteBase+"manifest", nil, "", "admin")
		if rec.Code == http.StatusOK {
			break
		}
		wantError(t, rec, http.StatusInternalServerError, "internal error")
		if len(faults) != 1 || !errors.Is(faults[0], blobtest.ErrDropped) {
			t.Fatalf("%d sound opens: faults %v", spare, faults)
		}
		faults = nil
		failed++
	}
	if failed < 2 {
		t.Fatalf("only %d opens failed: the check's and the route's each must", failed)
	}
	blobs.Mend()
	if got := f.up.Count(http.MethodGet, "/rocks/manifest"); got != 1 {
		t.Fatalf("upstream fetched the manifest %d times, want 1", got)
	}
}

// cachedSHA is the SHA-256 of the remote cache's copy of path.
func cachedSHA(t *testing.T, f *remoteFixture, path string) string {
	t.Helper()
	return (&faultyRemote{remoteFixture: f}).cachedSHA(path)
}
