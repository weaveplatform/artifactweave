package bowerhttp

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/blobtest"
)

// servedCopyFaults is a remote fixture whose blob store, which the remote
// policy and the item service share, can fail its reads.
func servedCopyFaults(t *testing.T, git bool) (*remoteFixture, *blobtest.ReadFaults) {
	t.Helper()
	f := newRemoteFixture(t, git)
	blobs := &blobtest.ReadFaults{Store: f.items.Blobs}
	f.items.Blobs, f.policy.Blobs = blobs, blobs
	return f, blobs
}

// wantServedCopyFaults fails t unless path, once its metadata is cached,
// answers 500 "internal error" and logs the store's error once wherever a
// read of a cached copy fails: as the cache checks it, or as the route
// reads the copy the cache serves it. Once the store reads again, path
// answers without fetching from the upstream: the cached copies are kept.
func wantServedCopyFaults(t *testing.T, f *remoteFixture, blobs *blobtest.ReadFaults, path string) {
	t.Helper()
	if r := f.get(path, "admin"); r.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", path, r.Code, r.Body)
	}
	fetches := gets(f)
	failed := 0
	for spare := 0; spare < 50; spare++ {
		blobs.CutEvery(spare)
		f.faults = nil
		r := f.get(path, "admin")
		if r.Code == http.StatusOK {
			break
		}
		if r.Code != http.StatusInternalServerError ||
			r.Body.String() != `{"errors":[{"message":"internal error","status":500}]}`+"\n" {
			t.Fatalf("%s, %d sound opens: %d %q", path, spare, r.Code, r.Body)
		}
		if len(f.faults) != 1 || !errors.Is(f.faults[0], blobtest.ErrDropped) {
			t.Fatalf("%s, %d sound opens: faults %v", path, spare, f.faults)
		}
		failed++
	}
	blobs.Mend()
	if failed == 0 {
		t.Fatalf("%s: no read failed", path)
	}
	if got := gets(f); got != fetches {
		t.Fatalf("%s: the upstream was fetched %d times, want %d", path, got, fetches)
	}
}

// gets counts the GET requests the upstream has had.
func gets(f *remoteFixture) int {
	n := 0
	for _, r := range f.up.Requests() {
		if r.Method == http.MethodGet {
			n++
		}
	}
	return n
}

// A Bower remote's catalog, search and refs read the cached copy the
// source service serves them, from an Artifactory source and from a Git
// registry. A failed read of it is the server's storage fault, 500
// "internal error", where these routes answered 502 "Bad Gateway" (#1462).
func TestAccBowerRemoteAnswersAFailedReadOfTheServedCopyAsAServerFault(t *testing.T) {
	t.Run("artifactory", func(t *testing.T) {
		f, blobs := servedCopyFaults(t, false)
		base := "/artifactory/api/bower/bower-remote/"
		f.up.Set("/api/bower/source/packages",
			fakeupstream.Resource{Body: []byte(`[{"name":"hello","url":"art://source/hello"}]`)})
		f.up.Set("/api/bower/source/refs/bower-remote/hello",
			fakeupstream.Resource{Body: []byte(strings.Repeat("a", 44) + " refs/tags/v1.0.0\n")})
		for _, path := range []string{"packages/search/hello", "refs/bower-remote/hello"} {
			wantServedCopyFaults(t, f, blobs, base+path)
		}
	})
	t.Run("git", func(t *testing.T) {
		f, blobs := servedCopyFaults(t, true)
		base := "/artifactory/api/bower/bower-remote/"
		pointer := `{"name":"hello","url":"` + f.up.URL + `/git/team/hello.git"}`
		f.up.Set("/registry/packages/search/hello", fakeupstream.Resource{Body: []byte("[" + pointer + "]")})
		f.up.Set("/registry/packages/hello", fakeupstream.Resource{Body: []byte(pointer)})
		f.up.Set("/git/team/hello.git/info/refs", fakeupstream.Resource{Body: []byte(
			gitPkt("# service=git-upload-pack\n") + "0000" +
				gitPkt(strings.Repeat("a", 40)+" refs/tags/v1.0.0\n") + "0000",
		)})
		for _, path := range []string{"packages/search/hello", "refs/bower-remote/hello"} {
			wantServedCopyFaults(t, f, blobs, base+path)
		}
	})
}
