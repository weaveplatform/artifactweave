package item

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

var (
	errDown = errors.New("the store is down")
	// errUpstream stands for a failed read of the upstream's body.
	errUpstream = errors.New("the upstream reset the connection")
)

// writeFaultBlobs fails BeginUpload with begin, and the uploads it begins
// at Append with write or at Commit with commit.
type writeFaultBlobs struct {
	blob.Store
	begin, write, commit error
}

func (b writeFaultBlobs) BeginUpload(
	ctx context.Context,
	opts ...blob.UploadOption,
) (blob.Upload, error) {
	if b.begin != nil {
		return nil, b.begin
	}
	u, err := b.Store.BeginUpload(ctx, opts...)
	if err != nil {
		return nil, err
	}
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
	return u.Upload.Append(ctx, r)
}

func (u failingUpload) Commit(ctx context.Context, want blob.Digests) (blob.Info, error) {
	if u.commit != nil {
		return blob.Info{}, u.commit
	}
	return u.Upload.Commit(ctx, want)
}

// storeFaults is the item service's store whose transaction fails to begin
// with begin or to commit with commit, and whose fence check, entry
// publication and item writes fail with fence, publish and item.
type storeFaults struct {
	Store
	begin, commit, fence, publish, item error
}

func (s storeFaults) Tx(ctx context.Context, fn func(context.Context) error) error {
	if s.begin != nil {
		return s.begin
	}
	return s.Store.Tx(ctx, func(ctx context.Context) error {
		if err := fn(ctx); err != nil {
			return err
		}
		// Failing the inner function rolls back what fn wrote, as a failed
		// commit does.
		return s.commit
	})
}

func (s storeFaults) RemoteCache() port.RemoteCacheStore {
	return remoteCacheFaults{RemoteCacheStore: s.Store.RemoteCache(), s: s}
}

func (s storeFaults) Items() port.ItemStore {
	return itemFaults{ItemStore: s.Store.Items(), err: s.item}
}

type remoteCacheFaults struct {
	port.RemoteCacheStore
	s storeFaults
}

func (r remoteCacheFaults) Check(ctx context.Context, token model.RemoteCacheToken) error {
	if r.s.fence != nil {
		return r.s.fence
	}
	return r.RemoteCacheStore.Check(ctx, token)
}

func (r remoteCacheFaults) Put(
	ctx context.Context,
	token model.RemoteCacheToken,
	e model.RemoteCacheEntry,
) (model.RemoteCacheEntry, error) {
	if r.s.publish != nil {
		return model.RemoteCacheEntry{}, r.s.publish
	}
	return r.RemoteCacheStore.Put(ctx, token, e)
}

type itemFaults struct {
	port.ItemStore
	err error
}

func (i itemFaults) Put(ctx context.Context, it model.Item) (model.Item, error) {
	if i.err != nil {
		return model.Item{}, i.err
	}
	return i.ItemStore.Put(ctx, it)
}

// readingHook is a format whose Deployed reads the stored copy, as Go's
// reads a version's stored .info, and refuses with errBadCopy for any
// error, or always when refuse is set.
type readingHook struct {
	generic.Handler
	refuse bool
}

func (h readingHook) Deployed(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	it model.Item,
) error {
	r, _, err := env.Blobs.Open(ctx, it.Actual.SHA256)
	if err != nil {
		return errBadCopy
	}
	defer r.Close()
	if _, err = io.ReadAll(r); err != nil || h.refuse {
		return errBadCopy
	}
	return nil
}

// wantMarked checks that err is a StorageFault whose cause is cause when
// cause is set, and no StorageFault otherwise.
func wantMarked(t *testing.T, err, cause error) {
	t.Helper()
	var fault *StorageFault
	marked := errors.As(err, &fault)
	if cause == nil {
		if marked || errors.Is(err, format.ErrStorageFault) {
			t.Fatalf("marked: %v", err)
		}
		return
	}
	if !marked || !errors.Is(err, format.ErrStorageFault) || !errors.Is(fault.Cause, cause) {
		t.Fatalf("want a storage fault caused by %v, got %v", cause, err)
	}
}

// wantNothingCached checks that the cache kept neither an entry nor an item
// for u.
func (w *world) wantNothingCached(u CacheUpload) {
	w.t.Helper()
	if _, err := w.db.RemoteCache().
		Read(w.t.Context(), u.Token.Key); !errors.Is(
		err,
		kernel.ErrNotFound,
	) {
		w.t.Fatalf("cache entry: %v", err)
	}
	if _, err := w.db.Items().Get(w.t.Context(), u.Repo.Key, true, u.Token.Key.Path); !errors.Is(
		err,
		kernel.ErrNotFound,
	) {
		w.t.Fatalf("item: %v", err)
	}
}

// A remote file whose copy the blob store fails to begin, write or commit
// is a StorageFault, and the cache keeps nothing. A failed read of the
// upstream's body is the upstream's, and a body whose digest is not the
// one declared is the upstream's bad content: both keep their error.
func TestUnitCacheMarksAFailedWriteOfItsCopy(t *testing.T) {
	for name, tc := range map[string]struct {
		blobs writeFaultBlobs
		body  io.Reader
		want  error // the error Cache returns
		cause error // nil: not marked
	}{
		"begin":    {blobs: writeFaultBlobs{begin: errDown}, want: errDown, cause: errDown},
		"write":    {blobs: writeFaultBlobs{write: errDown}, want: errDown, cause: errDown},
		"commit":   {blobs: writeFaultBlobs{commit: errDown}, want: errDown, cause: errDown},
		"digest":   {blobs: writeFaultBlobs{commit: blob.ErrDigestMismatch}, want: ErrChecksumMismatch},
		"upstream": {body: io.MultiReader(strings.NewReader("pay"), iotest.ErrReader(errUpstream)), want: errUpstream},
	} {
		t.Run(name, func(t *testing.T) {
			w, u := remoteWorld(t)
			tc.blobs.Store = w.svc.Blobs
			w.svc.Blobs = tc.blobs
			if tc.body != nil {
				u.Body = tc.body
			}
			_, err := w.svc.Cache(t.Context(), u)
			if !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
			wantMarked(t, err, tc.cause)
			w.wantNothingCached(u)
		})
	}
}

// A remote file whose publication meets a failed call of the store, its
// transaction's begin or commit, the cache fence, the item write or the
// entry's publication, is a StorageFault, and the cache keeps nothing. A
// fence that finds the read superseded (kernel.ErrConflict) is the store's
// verdict and keeps its error.
func TestUnitCacheMarksAFailedStoreCall(t *testing.T) {
	for name, tc := range map[string]struct {
		store storeFaults
		want  error
		cause error
	}{
		"begin":      {store: storeFaults{begin: errDown}, want: errDown, cause: errDown},
		"commit":     {store: storeFaults{commit: errDown}, want: errDown, cause: errDown},
		"fence":      {store: storeFaults{fence: errDown}, want: errDown, cause: errDown},
		"item":       {store: storeFaults{item: errDown}, want: errDown, cause: errDown},
		"publish":    {store: storeFaults{publish: errDown}, want: errDown, cause: errDown},
		"superseded": {store: storeFaults{fence: kernel.ErrConflict}, want: kernel.ErrConflict},
	} {
		t.Run(name, func(t *testing.T) {
			w, u := remoteWorld(t)
			tc.store.Store = w.svc.Store
			w.svc.Store = tc.store
			_, err := w.svc.Cache(t.Context(), u)
			if !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
			wantMarked(t, err, tc.cause)
			w.wantNothingCached(u)
		})
	}
}

// A format hook of the publication whose open or read of stored content
// fails, and that refuses for it, makes the publication a StorageFault, as
// a check's refusal after a failed read is; the cache keeps nothing. A
// hook's refusal with no failed read keeps its error.
func TestUnitCacheMarksAFailedReadOfAHook(t *testing.T) {
	sha := sums("payload").SHA256
	for name, tc := range map[string]struct {
		open, read error
		refuse     bool
		cause      error
	}{
		"open":    {open: errDown, cause: errDown},
		"read":    {read: errDown, cause: errDown},
		"refusal": {refuse: true},
	} {
		t.Run(name, func(t *testing.T) {
			w, u := remoteWorld(t)
			w.svc.Blobs = readFaultBlobs{Store: w.svc.Blobs, sha: sha, open: tc.open, read: tc.read}
			w.svc.Formats = func(repo.PackageType) format.Handler { return readingHook{refuse: tc.refuse} }
			_, err := w.svc.Cache(t.Context(), u)
			if !strings.Contains(fmt.Sprint(err), "cache transaction") ||
				tc.cause == nil && !errors.Is(err, errBadCopy) {
				t.Fatal(err)
			}
			wantMarked(t, err, tc.cause)
			w.wantNothingCached(u)
		})
	}
}

// A spooled copy the cache will not keep whose temporary file cannot be
// made is a StorageFault.
func TestUnitCacheTransientMarksAFailedSpool(t *testing.T) {
	w, u := remoteWorld(t)
	u.NoStore = true
	missingTempDir(t)
	_, err := w.svc.Cache(t.Context(), u)
	var fault *StorageFault
	if !errors.As(err, &fault) || !errors.Is(fault.Cause, fs.ErrNotExist) {
		t.Fatal(err)
	}
}

// spoolUpstream marks a failed write of the server's file, never a failed
// read of the upstream's body, nor a write that fails once the request
// ended.
func TestUnitSpoolUpstreamTellsTheStorageFromTheUpstream(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for name, tc := range map[string]struct {
		ctx   context.Context
		w     io.Writer
		body  io.Reader
		want  error
		cause error
	}{
		"write":    {ctx: t.Context(), w: failingWriter{}, body: strings.NewReader("payload"), want: errDown, cause: errDown},
		"upstream": {ctx: t.Context(), w: io.Discard, body: iotest.ErrReader(errUpstream), want: errUpstream},
		"canceled": {ctx: canceled, w: failingWriter{}, body: strings.NewReader("payload"), want: errDown},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := spoolUpstream(tc.ctx, tc.w, tc.body)
			if !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
			wantMarked(t, err, tc.cause)
		})
	}
	if n, err := spoolUpstream(
		t.Context(),
		io.Discard,
		strings.NewReader("payload"),
	); n != 7 ||
		err != nil {
		t.Fatal(n, err)
	}
}

// missingTempDir points os.TempDir at a directory that does not exist, so
// os.CreateTemp fails. os.TempDir reads TMPDIR on Unix, and TMP, then TEMP,
// on Windows.
func missingTempDir(t *testing.T) {
	t.Helper()
	missing := filepath.Join(t.TempDir(), "missing")
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, missing)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errDown }

// A mark is kept: an error already marked is not marked again with another
// cause. The store's verdicts, and no error, are never marked.
func TestUnitMarkStorageFaultKeepsAMark(t *testing.T) {
	marked := fmt.Errorf("cache fence: %w", MarkStorageFault(t.Context(), errDown, errBadCopy))
	if got := MarkStorageFault(t.Context(), errDropped, marked); got != marked {
		t.Fatalf("marked again: %v", got)
	}
	for _, verdict := range []error{nil, kernel.ErrNotFound, kernel.ErrConflict, kernel.ErrInvalid} {
		if got := storeFault(t.Context(), verdict); got != verdict {
			t.Fatalf("%v marked: %v", verdict, got)
		}
	}
	if got := storeFault(t.Context(), marked); got != marked {
		t.Fatalf("marked again: %v", got)
	}
}
