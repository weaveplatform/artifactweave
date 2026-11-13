package cocoapodshttp

import (
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	podfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/cocoapods"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/blobtest"
)

// A CocoaPods remote's podspec, its shard index and its source archive
// read the cached CDN copy the source service serves them, through the
// remote and through a virtual repository. A failed read of it, as the
// cache checks it or as the route reads it, is the server's storage fault:
// 500 "internal error", logged once, where the route's read answered 502
// "Bad Gateway" (#1462). Once the store reads again the route answers from
// the kept copies, without fetching from the upstream.
func TestUnitRemoteCDNAnswersAFailedReadOfTheServedCopyAsAServerFault(t *testing.T) {
	for _, key := range []string{"pods-remote", "pods-virtual"} {
		for _, path := range []string{
			podfmt.SpecPath("RemotePod", "2.0.0"),
			podfmt.ShardFile("RemotePod"),
			"sources/RemotePod/2.0.0/source.tar.gz",
		} {
			t.Run(key+"/"+path, func(t *testing.T) {
				f := newRouteFixture(t)
				f.seedRemote(t)
				policy := f.source.Cache.(*remote.Policy)
				blobs := &blobtest.ReadFaults{Store: f.blobs}
				policy.Blobs, policy.Items.Blobs = blobs, blobs
				if r := f.get(key, path); r.Code != http.StatusOK {
					t.Fatalf("first read: %d %s", r.Code, r.Body)
				}
				fetches := func() int {
					n := 0
					for _, r := range f.up.Requests() {
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
					r := f.get(key, path)
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
}
