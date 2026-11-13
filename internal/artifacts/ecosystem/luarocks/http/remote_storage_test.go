package luarockshttp_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

// copyFaults is the remote fixture's blob store, which the remote policy
// and the item service share. Once cut, every open of sha gives content
// whose first sound reads, through Read or ReadAt, are served and whose
// later ones fail with errDropped, as a store whose connection drops does.
// Its uploads fail at Append with write, or at Commit with commit.
type copyFaults struct {
	blob.Store
	mu            sync.Mutex
	sha           string
	sound         int
	write, commit error
}

func (b *copyFaults) BeginUpload(
	ctx context.Context,
	opts ...blob.UploadOption,
) (blob.Upload, error) {
	u, err := b.Store.BeginUpload(ctx, opts...)
	if err != nil {
		return nil, err //nolint:wrapcheck // the store's own error
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return failingUpload{Upload: u, write: b.write, commit: b.commit}, nil
}

type failingUpload struct {
	blob.Upload
	write, commit error
}

func (u failingUpload) Append(ctx context.Context, r io.Reader) (int64, error) {
	if u.write != nil {
		return 0, u.write
	}
	return u.Upload.Append(ctx, r) //nolint:wrapcheck // the store's own error
}

func (u failingUpload) Commit(ctx context.Context, want blob.Digests) (blob.Info, error) {
	if u.commit != nil {
		return blob.Info{}, u.commit
	}
	return u.Upload.Commit(ctx, want) //nolint:wrapcheck // the store's own error
}

// storeFaults is the item service's store whose transaction fails to
// commit with commit, or whose publication of a cache entry fails with
// publish.
type storeFaults struct {
	item.Store
	commit, publish error
}

func (s *storeFaults) Tx(ctx context.Context, fn func(context.Context) error) error {
	return s.Store.Tx(
		ctx,
		func(ctx context.Context) error { //nolint:wrapcheck // the store's own error
			if err := fn(ctx); err != nil {
				return err
			}
			// Failing the inner function rolls back what fn wrote, as a failed
			// commit does.
			return s.commit
		},
	)
}

func (s *storeFaults) RemoteCache() port.RemoteCacheStore {
	return failingPut{RemoteCacheStore: s.Store.RemoteCache(), err: s.publish}
}

type failingPut struct {
	port.RemoteCacheStore
	err error
}

func (f failingPut) Put(
	ctx context.Context,
	token artmodel.RemoteCacheToken,
	e artmodel.RemoteCacheEntry,
) (artmodel.RemoteCacheEntry, error) {
	if f.err != nil {
		return artmodel.RemoteCacheEntry{}, f.err
	}
	return f.RemoteCacheStore.Put(ctx, token, e) //nolint:wrapcheck // the store's own error
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

// faultyRemote is a remote fixture whose blob store can be cut, whose
// item service's store can fail, and whose faults, the errors the router
// kit logs as unexpected, are kept.
type faultyRemote struct {
	*remoteFixture
	blobs  *copyFaults
	store  *storeFaults
	faults []error
}

func newFaultyRemote(t *testing.T) *faultyRemote {
	t.Helper()
	f := &faultyRemote{blobs: &copyFaults{}, store: &storeFaults{}}
	f.remoteFixture = newRemoteFixture(t, func(o *pkgroute.Options, items *item.Service) {
		f.blobs.Store = items.Blobs
		items.Blobs = f.blobs
		f.store.Store = items.Store
		items.Store = f.store
		o.Fault = func(_ *http.Request, err error) { f.faults = append(f.faults, err) }
	})
	f.seed()
	return f
}

// wantStorageFault checks that the router logged the store's error, cause,
// once, as it logs any server fault.
func (f *faultyRemote) wantStorageFault(what string, cause ...error) {
	f.t.Helper()
	want := errDropped
	if len(cause) > 0 {
		want = cause[0]
	}
	if len(f.faults) != 1 || !errors.Is(f.faults[0], want) {
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

// A remote rock the server's storage fails to keep answers 500 "internal
// error", logs the store's error and caches nothing: its blob store fails
// to write or commit the copy, or its store fails to publish the entry or
// to commit the transaction. Each answered 502 "Bad Gateway". The circuit
// is untouched, and once the storage works the next request fetches the
// rock again. A repository that keeps no copy and cannot make its spool
// file answers the same 500.
func TestAccRemoteRockAnswersAStoreFaultAsAServerFault(t *testing.T) {
	const path = "visible-1.0-1.src.rock"
	for name, fail := range map[string]func(*faultyRemote){
		"write":   func(f *faultyRemote) { f.blobs.write = errDropped },
		"commit":  func(f *faultyRemote) { f.blobs.commit = errDropped },
		"publish": func(f *faultyRemote) { f.store.publish = errDropped },
		"tx":      func(f *faultyRemote) { f.store.commit = errDropped },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFaultyRemote(t)
			// The rock's route reads the cached manifest first; it is kept
			// before the storage fails, so the fault meets the rock.
			wantBody(
				t,
				f.do(http.MethodGet, remoteBase+"manifest", nil, "", "admin"),
				[]string{`"1.0-1"`},
				nil,
			)
			fail(f)
			wantError(t, f.do(http.MethodGet, remoteBase+path, nil, "", "admin"),
				http.StatusInternalServerError, "internal error")
			f.wantStorageFault(name)
			if _, err := f.db.Items().Get(t.Context(), "rocks", true, path); err == nil {
				t.Fatal("the rock was cached")
			}
			state, err := f.db.RemoteCache().State(t.Context(), "rocks")
			if err != nil || state.ConnectionFailures != 0 {
				t.Fatalf("circuit %d: %v", state.ConnectionFailures, err)
			}
			f.blobs.write, f.blobs.commit, f.store.publish, f.store.commit = nil, nil, nil, nil
			if rec := f.do(
				http.MethodGet,
				remoteBase+path,
				nil,
				"",
				"admin",
			); rec.Code != http.StatusOK {
				t.Fatalf("cured: %d %s", rec.Code, rec.Body)
			}
			if got := f.up.Count(http.MethodGet, "/rocks/"+path); got != 2 {
				t.Fatalf("upstream fetched the rock %d times, want 2", got)
			}
		})
	}
	t.Run("spool", func(t *testing.T) {
		f := newFaultyRemote(t)
		c, err := f.db.Repos().Get(t.Context(), "rocks")
		if err != nil {
			t.Fatal(err)
		}
		keep := false
		c.StoreArtifactsLocally = &keep
		if err = f.db.Repos().Update(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
		wantError(t, f.do(http.MethodGet, remoteBase+path, nil, "", "admin"),
			http.StatusInternalServerError, "internal error")
		f.wantStorageFault("spool", fs.ErrNotExist)
	})
}
