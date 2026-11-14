package ansiblehttp_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/blobtest"
)

// A Galaxy remote's version list and version document read the cached copy
// the source service serves them. A failed read of it, as the cache checks
// it or as the route reads it, is the server's storage fault: 500
// "internal error", logged once, where the route's read answered 502 "Bad
// Gateway" (#1462). Once the store reads again the route answers from the
// kept copy, without fetching from the upstream.
func TestAccGalaxyRemoteAnswersAFailedReadOfTheServedCopyAsAServerFault(t *testing.T) {
	for _, page := range []string{"/api/v3/collections/weave/sample/versions/", "/api/v3/collections/weave/sample/versions/1.2.3/"} {
		t.Run(page, func(t *testing.T) {
			f, up, _, base := remoteFixture(t)
			blobs := &blobtest.ReadFaults{Store: f.items.Blobs}
			f.items.Blobs = blobs
			f.source.Cache.(*remote.Policy).Blobs = blobs
			requireStatus(t, f.do(http.MethodGet, base+page, nil, "", ""), http.StatusOK)
			fetches := func() int {
				n := 0
				for _, r := range up.Requests() {
					if r.Method == http.MethodGet {
						n++
					}
				}
				return n
			}
			fetched := fetches()
			failed := 0
			for spare := 0; spare < 50; spare++ {
				blobs.CutEvery(spare)
				f.faults = nil
				r := f.do(http.MethodGet, base+page, nil, "", "")
				if r.Code == http.StatusOK {
					break
				}
				if r.Code != http.StatusInternalServerError ||
					r.Body.String() != `{"errors":[{"message":"internal error","status":500}]}`+"\n" {
					t.Fatalf("%d sound opens: %d %q", spare, r.Code, r.Body)
				}
				if len(f.faults) != 1 || !errors.Is(f.faults[0], blobtest.ErrDropped) {
					t.Fatalf("%d sound opens: faults %v", spare, f.faults)
				}
				failed++
			}
			blobs.Mend()
			if failed == 0 {
				t.Fatal("no read failed")
			}
			if got := fetches(); got != fetched {
				t.Fatalf("the upstream was fetched %d times, want %d", got, fetched)
			}
		})
	}
}
