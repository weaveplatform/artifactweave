package remote_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

// readOnly is q read by "reader", who may read the remote but not fill its
// cache.
func readOnly(q source.Request) source.Request {
	q.Subject = accessapi.Subject{Name: "reader"}
	q.Access = &accessapitest.Decider{AllowsFunc: func(
		_ accessapi.ResourceType, _ accessapi.Repo, _ string, want accessapi.Actions,
	) bool {
		return want == accessapi.ActionRead
	}}
	return q
}

// wantFillDenied fails t unless err refuses reader's fill of path.
func wantFillDenied(t *testing.T, err error, path string) {
	t.Helper()
	var denied *source.CacheFillDenied
	want := "User reader is not permitted to deploy '" + path + "' into 'remote-cache:" + path + "'."
	if !errors.As(err, &denied) || denied.Message() != want {
		t.Fatalf("%v, want %q", err, want)
	}
}

// TestAccPolicyReadAloneDoesNotFillTheCache (#1587): a caller with Read
// alone is answered from the upstream's info, never its download: a file
// it holds is refused, with the upstream asked a HEAD only and nothing
// kept, as a HEAD is; a file it misses is the remembered miss; and the
// copy a caller with Deploy/Cache fills is then served to it.
func TestAccPolicyReadAloneDoesNotFillTheCache(t *testing.T) {
	w := policyFixture(t)
	w.up.Set("/lib/a.jar", fakeupstream.Resource{Body: []byte("jar bytes")})
	q := readOnly(w.request("lib/a.jar", source.ResourceKindArtifact))
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodGet} {
		q.Resource.Method = method
		_, err := w.read(q)
		wantFillDenied(t, err, "lib/a.jar")
	}
	if w.up.Count(http.MethodHead, "/lib/a.jar") != 3 ||
		w.up.Count(http.MethodGet, "/lib/a.jar") != 0 {
		t.Fatalf("upstream requests %v, want three HEADs", w.up.Requests())
	}

	w.mustRead(w.request("lib/a.jar", source.ResourceKindArtifact), "jar bytes")
	asked := len(w.up.Requests())
	q.Resource.Method = http.MethodGet
	w.mustRead(q, "jar bytes")
	if len(w.up.Requests()) != asked {
		t.Fatalf("a cached file asked the upstream: %v", w.up.Requests())
	}

	missing := readOnly(w.request("lib/missing.jar", source.ResourceKindArtifact))
	for range 2 {
		var denied *source.CacheFillDenied
		if _, err := w.read(
			missing,
		); !errors.Is(err, kernel.ErrNotFound) ||
			errors.As(err, &denied) {
			t.Fatalf("upstream miss: %v", err)
		}
	}
	if w.up.Count("", "/lib/missing.jar") != 1 {
		t.Fatalf("the miss was not remembered: %v", w.up.Requests())
	}

	bypass := readOnly(w.request("lib/b.jar", source.ResourceKindArtifact))
	bypass.Repo.BypassHeadRequests = ptr(true)
	w.up.Set("/lib/b.jar", fakeupstream.Resource{Body: []byte("b")})
	_, err := w.read(bypass)
	wantFillDenied(t, err, "lib/b.jar")
	if w.up.Count(http.MethodGet, "/lib/b.jar") != 1 ||
		w.up.Count(http.MethodHead, "/lib/b.jar") != 0 {
		t.Fatalf("bypassed HEAD: %v, want one GET for the info", w.up.Requests())
	}
	if _, err = w.db.RemoteCache().
		Read(t.Context(), w.p.CacheKey(bypass, "credential-1")); err == nil {
		t.Fatal("the info GET's body was kept")
	}
}

// TestAccPolicyReadAloneRefreshesAnUnchangedCopy (#1587): an expired copy
// the upstream has not changed is served to a caller with Read alone and
// made fresh again, as 7.x's getInfo keeps such a copy without a download;
// one the upstream has changed is refused and left as it was; an upstream
// failure serves the expired copy as it would to any caller.
func TestAccPolicyReadAloneRefreshesAnUnchangedCopy(t *testing.T) {
	w := policyFixture(t)
	w.up.Set("/meta.txt", fakeupstream.Resource{Body: []byte("old"), ETag: `"one"`})
	admin := w.request("meta.txt", source.ResourceKindMetadata)
	w.mustRead(admin, "old")
	key := w.p.CacheKey(admin, "credential-1")
	q := readOnly(admin)

	w.clock.Advance(time.Minute)
	w.mustRead(q, "old")
	if e, err := w.db.RemoteCache().
		Read(t.Context(), key); err != nil ||
		!e.ValidatedAt.Equal(w.clock.Now()) {
		t.Fatalf("an unchanged copy was not made fresh: %+v, %v", e, err)
	}
	asked := len(w.up.Requests())
	w.mustRead(q, "old")
	if len(w.up.Requests()) != asked {
		t.Fatalf("a fresh copy asked the upstream: %v", w.up.Requests())
	}

	w.clock.Advance(time.Minute)
	w.up.Set("/meta.txt", fakeupstream.Resource{Body: []byte("newer"), ETag: `"two"`})
	_, err := w.read(q)
	wantFillDenied(t, err, "meta.txt")
	if e, err := w.db.RemoteCache().Read(t.Context(), key); err != nil || e.ETag != `"one"` {
		t.Fatalf("a refused refresh changed the copy: %+v, %v", e, err)
	}
	if w.up.Count(http.MethodGet, "/meta.txt") != 1 {
		t.Fatalf("a refused refresh downloaded: %v", w.up.Requests())
	}

	w.up.Set("/meta.txt", fakeupstream.Resource{Status: http.StatusServiceUnavailable})
	w.mustRead(q, "old")
}

// TestAccPolicyReadAloneKeepsWhatStoresNothing (#1587): a read 7.x keeps
// no file for needs Read alone: a remote that stores nothing, a checksum
// request and a document the cache keeps without an item.
func TestAccPolicyReadAloneKeepsWhatStoresNothing(t *testing.T) {
	w := policyFixture(t)
	w.up.Set("/file.bin", fakeupstream.Resource{Body: []byte("bytes")})
	noStore := readOnly(w.request("file.bin", source.ResourceKindArtifact))
	noStore.Repo.StoreArtifactsLocally = ptr(false)
	w.mustRead(noStore, "bytes")

	checksum := readOnly(w.request("file.bin", source.ResourceKindArtifact))
	checksum.Resource.ChecksumRequest = true
	checksum.Resource.Method = http.MethodHead
	if _, err := w.read(checksum); err != nil {
		t.Fatal("checksum request:", err)
	}

	w.up.Set("/pkg/.index", fakeupstream.Resource{Body: []byte("index")})
	listing := readOnly(w.request("pkg/.index", source.ResourceKindMetadata))
	listing.Resource.NoCacheFile = true
	w.mustRead(listing, "index")
}

// TestUnitUnchanged: an upstream's 200 describes the cached copy when its
// ETag is the copy's, weak or strong, or, without one on either side, its
// Last-Modified is no later than the copy's; neither, an incomplete copy
// or a remembered miss is a change.
func TestUnitUnchanged(t *testing.T) {
	stamp := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	copyOf := func(etag string) model.RemoteCacheEntry {
		e := model.RemoteCacheEntry{ETag: etag, LastModified: stamp.Format(http.TimeFormat)}
		e.Blob = blob.Blob{Checksums: kernel.Checksums{SHA256: "sha"}}
		return e
	}
	answer := func(etag string, modified time.Time) *http.Response {
		h := http.Header{}
		if etag != "" {
			h.Set("ETag", etag)
		}
		if !modified.IsZero() {
			h.Set("Last-Modified", modified.Format(http.TimeFormat))
		}
		return &http.Response{StatusCode: http.StatusOK, Header: h}
	}
	negative := copyOf(`"a"`)
	negative.NegativeUntil = stamp
	for _, tc := range []struct {
		name string
		e    model.RemoteCacheEntry
		res  *http.Response
		want bool
	}{
		{"same tag", copyOf(`"a"`), answer(`W/"a"`, stamp.Add(time.Hour)), true},
		{"other tag", copyOf(`"a"`), answer(`"b"`, stamp), false},
		{"not later", copyOf(""), answer(`"b"`, stamp), true},
		{"later", copyOf(`"a"`), answer("", stamp.Add(time.Second)), false},
		{"neither", copyOf(""), answer("", time.Time{}), false},
		{"incomplete", model.RemoteCacheEntry{ETag: `"a"`}, answer(`"a"`, stamp), false},
		{"remembered miss", negative, answer(`"a"`, stamp), false},
	} {
		if got := remote.Unchanged(tc.e, tc.res); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestAccPolicyAssumedOfflineServesTheCacheOrMisses (#1601): while the
// upstream is assumed offline a read asks it nothing and serves the copy
// the cache holds, expired or not, whatever hardFail says, or misses with
// source.ErrAssumedOffline, on a remote that stores nothing and for a
// caller with Read alone too, as 7.x's internalGetInfo answers while
// isOffline holds.
func TestAccPolicyAssumedOfflineServesTheCacheOrMisses(t *testing.T) {
	w := policyFixture(t)
	w.up.Set("/meta.txt", fakeupstream.Resource{Body: []byte("old")})
	meta := w.request("meta.txt", source.ResourceKindMetadata)
	w.mustRead(meta, "old")
	w.clock.Advance(time.Minute)
	state, err := w.db.RemoteCache().State(t.Context(), w.c.Key)
	if err != nil {
		t.Fatal(err)
	}
	state.ConnectionFailures = 10
	state.OfflineUntil = w.clock.Now().Add(time.Hour)
	state.SettingsRevision = "credential-1"
	if err = w.db.RemoteCache().PutState(t.Context(), w.c.Key, state); err != nil {
		t.Fatal(err)
	}
	if !w.p.AssumedOffline(t.Context(), meta) {
		t.Fatal("AssumedOffline: false")
	}
	asked := len(w.up.Requests())

	meta.Repo.HardFail = ptr(true)
	w.mustRead(meta, "old")
	w.mustRead(readOnly(meta), "old")

	w.up.Set("/cold.bin", fakeupstream.Resource{Body: []byte("cold")})
	cold := w.request("cold.bin", source.ResourceKindArtifact)
	noStore := cold
	noStore.Repo.StoreArtifactsLocally = ptr(false)
	for name, q := range map[string]source.Request{
		"fill": cold, "read alone": readOnly(cold), "no store": noStore,
	} {
		if _, err = w.read(q); !errors.Is(err, source.ErrAssumedOffline) {
			t.Fatalf("%s: %v, want the assumed-offline miss", name, err)
		}
	}
	if len(w.up.Requests()) != asked {
		t.Fatalf("an assumed-offline upstream was asked: %v", w.up.Requests())
	}

	// A policy that cannot read the repository's options reports false,
	// leaving the read that follows to meet the failure.
	options := w.p.Options
	w.p.Options = func(context.Context, repo.Config) (remote.Options, error) {
		return remote.Options{}, errDropped
	}
	if w.p.AssumedOffline(t.Context(), meta) ||
		(&remote.Policy{}).AssumedOffline(t.Context(), meta) {
		t.Fatal("AssumedOffline without options: true")
	}
	w.p.Options = options

	w.clock.Advance(2 * time.Hour)
	if w.p.AssumedOffline(t.Context(), meta) {
		t.Fatal("AssumedOffline after the period: true")
	}
	w.mustRead(cold, "cold")
}

// TestAccPolicyReadAloneInfoAnswers (#1587): the info exchange of a caller
// with Read alone keeps every answer 7.x's getInfo gives without a
// download. An upstream's 200 that describes the copy (here the same ETag,
// with conditionals ignored) makes it fresh; a dropped connection and a
// 429 pause serve the expired copy, the pause without asking again; and a
// CocoaPods Git checkout, which has no info request, is refused unasked.
func TestAccPolicyReadAloneInfoAnswers(t *testing.T) {
	w := policyFixture(t)
	w.up.Set("/meta.txt", fakeupstream.Resource{Body: []byte("old"), ETag: `"one"`})
	admin := w.request("meta.txt", source.ResourceKindMetadata)
	w.mustRead(admin, "old")
	key := w.p.CacheKey(admin, "credential-1")
	q := readOnly(admin)

	w.clock.Advance(time.Minute)
	w.up.Set(
		"/meta.txt",
		fakeupstream.Resource{Body: []byte("old"), ETag: `"one"`, Status: http.StatusOK},
	)
	w.mustRead(q, "old")
	if e, err := w.db.RemoteCache().
		Read(t.Context(), key); err != nil ||
		!e.ValidatedAt.Equal(w.clock.Now()) {
		t.Fatalf("a described copy was not made fresh: %+v, %v", e, err)
	}

	w.clock.Advance(time.Minute)
	w.up.Set("/meta.txt", fakeupstream.Resource{Fault: fakeupstream.Fault{DropConnection: true}})
	w.mustRead(q, "old")

	w.clock.Advance(time.Minute)
	w.up.Set("/meta.txt", fakeupstream.Resource{
		Status: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"60"}},
	})
	w.mustRead(q, "old")
	asked := len(w.up.Requests())
	w.mustRead(q, "old")
	if len(w.up.Requests()) != asked {
		t.Fatalf("a paused upstream was asked: %v", w.up.Requests()[asked:])
	}

	git := readOnly(w.request("pods/Hello.git", source.ResourceKindArtifact))
	git.Resource.CocoaPodsGit = &source.CocoaPodsGitSource{
		URL: w.up.URL + "/Hello.git",
		Tag: "1.0.0",
	}
	asked = len(w.up.Requests())
	_, err := w.read(git)
	wantFillDenied(t, err, "pods/Hello.git")
	if len(w.up.Requests()) != asked {
		t.Fatalf("a refused Git checkout asked the upstream: %v", w.up.Requests()[asked:])
	}
}

// TestAccPolicyReadAloneFailedStateIsAStorageFault (#1587): the info
// exchange of a caller with Read alone meets a failure of the availability
// state as a fill does (#1489): a failed read asks the upstream nothing,
// and a failed write after the upstream's answer or its dropped connection
// is the server's storage fault.
func TestAccPolicyReadAloneFailedStateIsAStorageFault(t *testing.T) {
	stored := errors.Join(kernel.ErrStore, errDropped)
	for name, upstream := range map[string]fakeupstream.Resource{
		"read":             {Body: []byte("content")},
		"write after 200":  {Body: []byte("content")},
		"write after drop": {Fault: fakeupstream.Fault{DropConnection: true}},
	} {
		t.Run(name, func(t *testing.T) {
			w := policyFixture(t)
			faults := &stateFaults{Store: w.db}
			w.p.Store = faults
			w.up.Set("/file.bin", upstream)
			if name == "read" {
				faults.read = stored
			} else {
				faults.write = stored
			}
			_, err := w.read(readOnly(w.request("file.bin", source.ResourceKindArtifact)))
			wantStorageFault(t, err)
			if asked := len(w.up.Requests()); name == "read" && asked != 0 {
				t.Fatalf("the upstream was asked %d times while the state could not be read", asked)
			}
		})
	}
}

// TestAccPolicyReadAloneMIMERefusalComesFirst (#1587): the info exchange
// of a caller with Read alone checks the label before the deploy path, as
// 7.x's HttpRepo.handleGetInfoResponse refuses a mismatching label before
// downloadAndSave checks the deploy path, so the answer is the 409 refusal
// a caller who may fill the cache gets.
func TestAccPolicyReadAloneMIMERefusalComesFirst(t *testing.T) {
	w := policyFixture(t)
	w.up.Set("/a.jar", fakeupstream.Resource{
		Body: []byte("<html>login</html>"), Header: http.Header{"Content-Type": {"text/html"}},
	})
	q := readOnly(w.request("a.jar", source.ResourceKindArtifact))
	q.Resource.ClientMIME = &source.ClientMIME{}
	_, err := w.read(q)
	var refused *source.MIMERefusal
	if !errors.As(err, &refused) || errors.As(err, new(*source.CacheFillDenied)) {
		t.Fatalf("%v, want the MIME refusal", err)
	}
	if w.up.Count(http.MethodGet, "/a.jar") != 0 {
		t.Fatalf("a refused label was downloaded: %v", w.up.Requests())
	}
}
