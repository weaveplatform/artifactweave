package npmhttp_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/blob/blobtest"
)

// A remote package document the cache keeps, whose copy the server then
// fails to read as the route reads it, answers 500 "internal error" and logs
// the store's error once. That copy is the server's own, not the
// upstream's, and the route answered 502 "invalid upstream npm metadata",
// as for a document that does not parse (#1488). The cached document is
// kept, so once the store reads again it answers without the upstream.
func TestAccRemotePackageDocumentAnswersAFailedReadOfTheServedCopyAsAServerFault(t *testing.T) {
	f := newRemoteFixture(t)
	blobs := &blobtest.ReadFaults{Store: f.items.Blobs}
	f.items.Blobs, f.policy.Blobs = blobs, blobs
	var faults []error
	f.opts.Fault = func(_ *http.Request, err error) { faults = append(faults, err) }
	f.mount(t, base)
	f.packageDoc(t, "pkg", "1.0.0")
	requireRemote(t, f.do(http.MethodGet, remoteRegistry+"pkg", "reader"), http.StatusOK, "1.0.0")

	blobs.CutEvery(0)
	rec := f.do(http.MethodGet, remoteRegistry+"pkg", "reader")
	if rec.Code != http.StatusInternalServerError ||
		rec.Body.String() != `{"error":"internal error"}`+"\n" {
		t.Fatalf("failed read: %d %q", rec.Code, rec.Body)
	}
	if len(faults) != 1 || !errors.Is(faults[0], blobtest.ErrDropped) {
		t.Fatalf("faults %v, want the store's error once", faults)
	}
	blobs.Mend()
	requireRemote(t, f.do(http.MethodGet, remoteRegistry+"pkg", "reader"), http.StatusOK, "1.0.0")
	if got := f.up.Count(http.MethodGet, "/pkg"); got != 1 {
		t.Fatalf("upstream fetched the document %d times, want 1", got)
	}
}
