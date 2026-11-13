package luarockshttp_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

// copyFaults is the remote fixture's blob store, which the remote policy
// and the item service share. Once cut, every open of sha gives content
// whose first sound reads, through Read or ReadAt, are served and whose
// later ones fail with errDropped, as a store whose connection drops does.
type copyFaults struct {
	blob.Store
	mu    sync.Mutex
	sha   string
	sound int
}

func (b *copyFaults) cut(sha string, sound int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sha, b.sound = sha, sound
}

func (b *copyFaults) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	r, info, err := b.Store.Open(ctx, sha)
	b.mu.Lock()
	defer b.mu.Unlock()
	if err != nil || sha != b.sha {
		return r, info, err //nolint:wrapcheck // the store's own error
	}
	return &cutCopy{readAtFault: readAtFault{ReadSeekCloser: r, sound: b.sound}}, info, nil
}

// cutCopy is readAtFault whose Read fails as its ReadAt does, sharing one
// count of sound reads.
type cutCopy struct{ readAtFault }

func (c *cutCopy) Read(p []byte) (int, error) {
	if c.sound == 0 {
		return 0, errDropped
	}
	c.sound--
	return c.ReadSeekCloser.Read(p)
}

func shaOf(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// faultyRemote is a remote fixture whose blob store can be cut and whose
// faults, the errors the router kit logs as unexpected, are kept.
type faultyRemote struct {
	*remoteFixture
	blobs  *copyFaults
	faults []error
}

func newFaultyRemote(t *testing.T) *faultyRemote {
	t.Helper()
	f := &faultyRemote{blobs: &copyFaults{}}
	f.remoteFixture = newRemoteFixture(t, func(o *pkgroute.Options, items *item.Service) {
		f.blobs.Store = items.Blobs
		items.Blobs = f.blobs
		o.Fault = func(_ *http.Request, err error) { f.faults = append(f.faults, err) }
	})
	f.seed()
	return f
}

// wantStorageFault checks that the router logged the store's error once,
// as it logs any server fault.
func (f *faultyRemote) wantStorageFault(what string) {
	f.t.Helper()
	if len(f.faults) != 1 || !errors.Is(f.faults[0], errDropped) {
		f.t.Fatalf("%s: faults %v", what, f.faults)
	}
	f.faults = nil
}

// cachedSHA is the SHA-256 of the remote cache's copy of path.
func (f *faultyRemote) cachedSHA(path string) string {
	f.t.Helper()
	entries, _, err := f.db.RemoteCache().List(f.t.Context(), "rocks", "", kernel.Page{})
	if err != nil {
		f.t.Fatal(err)
	}
	for _, e := range entries {
		if e.Token.Key.Path == path {
			return e.Blob.Checksums.SHA256
		}
	}
	f.t.Fatalf("%s is not cached", path)
	return ""
}

// A remote rock whose cached copy fails a read while LuaRocks checks it is
// no bad rock: wherever the read fails, the rock answers 500 "internal
// error", logs the store's error and is not cached, so the next request
// fetches it again. It answered 502 "Bad Gateway". A rock the upstream
// sends that is no ZIP still answers 502 and logs nothing.
func TestAccRemoteRockAnswersAFailedReadOfItsCopyAsAServerFault(t *testing.T) {
	const path = "visible-1.0-1.src.rock"
	f := newFaultyRemote(t)
	body := rock(t, "visible", "1.0-1")
	f.up.Set("/rocks/"+path, fakeupstream.Resource{Body: body})
	failed := 0
	for sound := 0; ; sound++ {
		f.blobs.cut(shaOf(body), sound)
		rec := f.do(http.MethodGet, remoteBase+path, nil, "", "admin")
		if rec.Code == http.StatusOK {
			break
		}
		wantError(t, rec, http.StatusInternalServerError, "internal error")
		f.wantStorageFault(fmt.Sprintf("%d sound reads", sound))
		if _, err := f.db.Items().Get(t.Context(), "rocks", true, path); err == nil {
			t.Fatalf("%d sound reads: the rock was cached", sound)
		}
		failed++
	}
	if failed < 3 {
		t.Fatalf("only %d reads failed: the sniff, the directory and the entry each must", failed)
	}
	if got := f.up.Count(http.MethodGet, "/rocks/"+path); got != failed+1 {
		t.Fatalf("upstream fetched the rock %d times, want %d", got, failed+1)
	}
	bad := newFaultyRemote(t)
	bad.up.Set("/rocks/"+path, fakeupstream.Resource{Body: []byte("bad archive")})
	wantError(t, bad.do(http.MethodGet, remoteBase+path, nil, "", "admin"),
		http.StatusBadGateway, "Bad Gateway")
	if len(bad.faults) != 0 {
		t.Fatalf("an invalid rock logged faults: %v", bad.faults)
	}
}

// A remote manifest past its retrieval period whose cached copy fails a
// read while it is checked answers 500 "internal error": a new manifest
// the upstream sends, and the cached one a 304 re-admits. Both answered
// 502 "Bad Gateway", and neither served the cached manifest. The cached
// manifest is kept, and once the store reads again the refresh succeeds.
func TestAccRemoteManifestRefreshAnswersAFailedReadAsAServerFault(t *testing.T) {
	const updated = `commands = {}
modules = {}
repository = { ["visible"] = { ["2.0-1"] = { { arch = "rockspec" } } } }
`
	for _, changed := range []bool{true, false} {
		t.Run(fmt.Sprintf("changed %v", changed), func(t *testing.T) {
			f := newFaultyRemote(t)
			wantBody(
				t,
				f.do(http.MethodGet, remoteBase+"manifest", nil, "", "admin"),
				[]string{`"1.0-1"`},
				nil,
			)
			f.clock.Advance(11 * time.Second)
			want := `"1.0-1"`
			if changed {
				f.up.Set("/rocks/manifest", fakeupstream.Resource{Body: []byte(updated)})
				f.blobs.cut(shaOf([]byte(updated)), 0)
				want = `"2.0-1"`
			} else {
				// The upstream's ETag is its body's SHA-256, quoted.
				sha := f.cachedSHA("manifest")
				f.up.Set("/rocks/manifest",
					fakeupstream.Resource{Status: http.StatusNotModified, ETag: `"` + sha + `"`})
				f.blobs.cut(sha, 0)
			}
			rec := f.do(http.MethodGet, remoteBase+"manifest", nil, "", "admin")
			wantError(t, rec, http.StatusInternalServerError, "internal error")
			f.wantStorageFault("refresh")
			f.blobs.cut("", 0)
			wantBody(
				t,
				f.do(http.MethodGet, remoteBase+"manifest", nil, "", "admin"),
				[]string{want},
				nil,
			)
		})
	}
}
