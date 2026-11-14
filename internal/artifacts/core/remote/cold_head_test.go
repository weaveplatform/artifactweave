package remote_test

import (
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// TestAccPolicyColdHEADFillsTheCache (#1391): on a remote that stores
// artifacts locally a cold HEAD downloads the file as a GET does, the
// upstream asked as for a GET (its HEAD, then its GET), and is answered
// with the file kept, typed by its path whatever the upstream labels it;
// the GET after it makes no upstream request. A raw metadata document is
// kept the same way and its HEAD answered from the entry, without a body.
// 7.161.26's DownloadServiceImpl.respond downloads on such a HEAD rather
// than answer from the upstream's info.
func TestAccPolicyColdHEADFillsTheCache(t *testing.T) {
	w := policyFixture(t)
	w.up.Set("/lib/file.jar", fakeupstream.Resource{
		Body: []byte("jar bytes"), Header: http.Header{"Content-Type": {"application/zip"}},
	})
	q := w.request("lib/file.jar", source.ResourceKindArtifact)
	q.Resource.Method = http.MethodHead
	r, err := w.read(q)
	if err != nil || r.Stream != nil || r.Item == nil || r.Item.Size != 9 ||
		r.Item.MimeType != "application/java-archive" {
		t.Fatalf("cold HEAD: %+v, %v; want the kept jar", r, err)
	}
	if w.up.Count(http.MethodHead, "/lib/file.jar") != 1 ||
		w.up.Count(http.MethodGet, "/lib/file.jar") != 1 {
		t.Fatalf("upstream requests %v, want a GET's HEAD and GET", w.up.Requests())
	}
	q.Resource.Method = http.MethodGet
	w.mustRead(q, "jar bytes")
	if w.up.Count("", "/lib/file.jar") != 2 {
		t.Fatalf("the GET after the HEAD asked the upstream: %v", w.up.Requests())
	}

	w.up.Set("/meta.txt", fakeupstream.Resource{Body: []byte("index"), ETag: `"one"`})
	meta := w.request("meta.txt", source.ResourceKindMetadata)
	meta.Resource.Method = http.MethodHead
	r, err = w.read(meta)
	if err != nil || r.Stream == nil || r.Stream.Body != http.NoBody || r.Stream.Size != 5 ||
		r.Stream.ETag != `"one"` {
		t.Fatalf("cold metadata HEAD: %+v, %v", r, err)
	}
	if _, err = w.db.RemoteCache().
		Read(t.Context(), w.p.CacheKey(meta, "credential-1")); err != nil {
		t.Fatalf("cold metadata HEAD kept nothing: %v", err)
	}
	meta.Resource.Method = http.MethodGet
	w.mustRead(meta, "index")
	if w.up.Count(http.MethodGet, "/meta.txt") != 1 {
		t.Fatalf("the GET after the HEAD asked the upstream: %v", w.up.Requests())
	}
}

// TestAccPolicyExpiredHEADRefreshesTheCache (#1391): a HEAD of an expired
// entry whose upstream has changed keeps the new bytes, as 7.x downloads
// an expired file on such a HEAD (RemoteRepoBase.downloadAndSave expires
// it), so the GET after it is answered from the cache.
func TestAccPolicyExpiredHEADRefreshesTheCache(t *testing.T) {
	w := policyFixture(t)
	w.up.Set("/meta.txt", fakeupstream.Resource{Body: []byte("old"), ETag: `"one"`})
	q := w.request("meta.txt", source.ResourceKindMetadata)
	w.mustRead(q, "old")
	w.clock.Advance(time.Minute)
	w.up.Set("/meta.txt", fakeupstream.Resource{Body: []byte("newer"), ETag: `"two"`})
	q.Resource.Method = http.MethodHead
	r, err := w.read(q)
	if err != nil || r.Stream == nil || r.Stream.Size != 5 || r.Stream.ETag != `"two"` {
		t.Fatalf("expired HEAD: %+v, %v", r, err)
	}
	gets := w.up.Count(http.MethodGet, "/meta.txt")
	q.Resource.Method = http.MethodGet
	w.mustRead(q, "newer")
	if w.up.Count(http.MethodGet, "/meta.txt") != gets {
		t.Fatalf("the GET after the HEAD asked the upstream: %v", w.up.Requests())
	}
}

// TestAccPolicyColdHEADSharesTheGETsExchange: a HEAD and a GET of the same
// cold path share one upstream exchange, R14's single fill.
func TestAccPolicyColdHEADSharesTheGETsExchange(t *testing.T) {
	w := policyFixture(t)
	gate := new(fakeupstream.Barrier)
	w.up.Set("/shared.bin", fakeupstream.Resource{
		Body: []byte("shared"), Fault: fakeupstream.Fault{BeforeBody: gate},
	})
	q := w.request("shared.bin", source.ResourceKindArtifact)
	q.Repo.BypassHeadRequests = ptr(true)
	key := w.p.FlightKey(w.p.CacheKey(q, "credential-1"), w.permit.Identity)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, method := range []string{http.MethodGet, http.MethodHead} {
		read := q
		read.Resource.Method = method
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := w.read(read)
			if e == nil && r.Item == nil {
				e = errors.New(method + ": no kept file")
			}
			errs[i] = e
		}()
		if i == 0 {
			<-gate.Reached()
		}
	}
	waitFor(t, func() bool { return w.p.FlightWaiters(key) == 2 })
	gate.Release()
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	if w.up.Count("", "/shared.bin") != 1 {
		t.Fatalf("upstream requests %v, want one GET", w.up.Requests())
	}
	if _, err := w.db.Items().
		Get(t.Context(), "remote", true, "shared.bin"); errors.Is(
		err,
		kernel.ErrNotFound,
	) {
		t.Fatal("nothing kept")
	}
}
