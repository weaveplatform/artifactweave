package multipart_test

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/service/multipart"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var errBoom = errors.New("boom")

// faults switches failures on and off while a test runs.
type faults struct {
	mu sync.Mutex
	on map[string]int
}

func (f *faults) set(op string, times int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.on == nil {
		f.on = map[string]int{}
	}
	f.on[op] = times
}

func (f *faults) hit(op string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.on[op] == 0 {
		return nil
	}
	f.on[op]--
	return errBoom
}

type faultStore struct {
	store.Store
	f *faults
}

func (s faultStore) Uploads() store.UploadStore { return faultUploads{s.Store.Uploads(), s.f} }
func (s faultStore) Blobs() store.BlobIndex     { return faultIndex{s.Store.Blobs(), s.f} }

type faultUploads struct {
	store.UploadStore
	f *faults
}

func (u faultUploads) Put(ctx context.Context, up store.Upload) error {
	if err := u.f.hit("put"); err != nil {
		return err
	}
	return u.UploadStore.Put(ctx, up) //nolint:wrapcheck // test double
}

func (u faultUploads) Get(ctx context.Context, id string) (store.Upload, error) {
	if err := u.f.hit("get"); err != nil {
		return store.Upload{}, err
	}
	return u.UploadStore.Get(ctx, id) //nolint:wrapcheck // test double
}

func (u faultUploads) Delete(ctx context.Context, id string) error {
	if err := u.f.hit("delete"); err != nil {
		return err
	}
	return u.UploadStore.Delete(ctx, id) //nolint:wrapcheck // test double
}

type faultIndex struct {
	store.BlobIndex
	f *faults
}

func (i faultIndex) Ref(ctx context.Context, b types.Blob, n int64) (types.Blob, error) {
	if err := i.f.hit("ref"); err != nil {
		return types.Blob{}, err
	}
	return i.BlobIndex.Ref(ctx, b, n) //nolint:wrapcheck // test double
}

type faultBlobs struct {
	blob.Store
	f *faults
}

func (b faultBlobs) BeginUpload(ctx context.Context, o ...blob.UploadOption) (blob.Upload, error) {
	if err := b.f.hit("begin"); err != nil {
		return nil, err
	}
	u, err := b.Store.BeginUpload(ctx, o...)
	return faultUpload{u, b.f}, err //nolint:wrapcheck // test double
}

func (b faultBlobs) ResumeUpload(ctx context.Context, id string) (blob.Upload, error) {
	if err := b.f.hit("resume"); err != nil {
		return nil, err
	}
	u, err := b.Store.ResumeUpload(ctx, id)
	if err != nil {
		return nil, err //nolint:wrapcheck // test double
	}
	return faultUpload{u, b.f}, nil
}

type faultUpload struct {
	blob.Upload
	f *faults
}

func (u faultUpload) Append(ctx context.Context, r io.Reader) (int64, error) {
	if err := u.f.hit("append"); err != nil {
		return 0, err
	}
	return u.Upload.Append(ctx, r) //nolint:wrapcheck // test double
}

func (u faultUpload) Commit(ctx context.Context, d blob.Digests) (blob.Info, error) {
	if err := u.f.hit("commit"); err != nil {
		return blob.Info{}, err
	}
	return u.Upload.Commit(ctx, d) //nolint:wrapcheck // test double
}

func faulty(t *testing.T) (*multipart.Service, *faults) {
	t.Helper()
	w := newWorld(t)
	f := &faults{}
	at := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	return &multipart.Service{
		Store: faultStore{w.db, f}, Blobs: faultBlobs{w.blobs, f}, Dir: w.dir,
		Now: func() time.Time { return at },
	}, f
}

func TestStoreAndBlobFailures(t *testing.T) {
	ctx := context.Background()
	data := content(2 * 1 << 20)

	s, f := faulty(t)
	f.set("put", 1)
	if _, err := s.Create(ctx, "libs", "f", 1, "admin"); !errors.Is(err, errBoom) {
		t.Fatalf("create with a failing store: %v", err)
	}
	token, err := s.Create(ctx, "libs", "f", 1, "admin")
	if err != nil {
		t.Fatal(err)
	}
	f.set("get", 1)
	if _, err := s.Status(ctx, token); !errors.Is(err, errBoom) {
		t.Fatalf("status with a failing store: %v", err)
	}
	f.set("begin", 1)
	if err := put(t, s, token, 1, data[:1<<20]); !errors.Is(err, errBoom) {
		t.Fatalf("a part whose blob upload cannot begin: %v", err)
	}
	// Part 1 stays staged; part 2 begins the blob upload but its append
	// fails, so both wait for the completion to append them.
	f.set("append", 1)
	if err := put(t, s, token, 2, data[1<<20:]); !errors.Is(err, errBoom) {
		t.Fatalf("a failing append: %v", err)
	}
	f.set("commit", 1)
	if err := s.Complete(ctx, token, sha1Of(data)); err != nil {
		t.Fatal(err)
	}
	if st := wait(t, s, token); st.Status != multipart.StatusRetryable {
		t.Fatalf("a failing commit is retryable: %+v", st)
	}
	f.set("ref", 1)
	if err := s.Complete(ctx, token, sha1Of(data)); err != nil {
		t.Fatal(err)
	}
	if st := wait(t, s, token); st.Status != multipart.StatusFailed ||
		!strings.Contains(st.Error, "index") {
		t.Fatalf("a failing index: %+v", st)
	}

	// A part appended whose session save failed is not appended again when
	// the client retries it.
	s, f = faulty(t)
	token, err = s.Create(ctx, "libs", "g", 1, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := put(t, s, token, 1, data[:1<<20]); err != nil {
		t.Fatal(err)
	}
	f.set("put", 1)
	if err := put(t, s, token, 2, data[1<<20:]); !errors.Is(err, errBoom) {
		t.Fatalf("a part whose session cannot be saved: %v", err)
	}
	if err := put(t, s, token, 2, data[1<<20:]); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, token, sha1Of(data)); err != nil {
		t.Fatal(err)
	}
	if st := wait(t, s, token); st.Status != multipart.StatusFinished {
		t.Fatalf("the retried part is appended once: %+v", st)
	}

	// Parts that cannot be appended leave the completion retryable.
	s, f = faulty(t)
	token, err = s.Create(ctx, "libs", "h", 1, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := put(t, s, token, 1, data[:1<<20]); err != nil {
		t.Fatal(err)
	}
	f.set("resume", 1)
	if err := put(t, s, token, 2, data[1<<20:]); !errors.Is(err, errBoom) {
		t.Fatalf("a blob upload that cannot resume: %v", err)
	}
	f.set("resume", 1)
	if err := s.Complete(ctx, token, sha1Of(data)); err != nil {
		t.Fatal(err)
	}
	if st := wait(t, s, token); st.Status != multipart.StatusRetryable {
		t.Fatalf("parts that cannot be appended are retryable: %+v", st)
	}
	f.set("delete", 1)
	if err := s.Abort(ctx, token); !errors.Is(err, errBoom) {
		t.Fatalf("an abort whose session cannot be dropped: %v", err)
	}
	// The sweep looks up the session of every staging directory.
	if _, err := s.Create(ctx, "libs", "i", 1, "admin"); err != nil {
		t.Fatal(err)
	}
	f.set("get", 1)
	if err := s.Sweep(ctx); !errors.Is(err, errBoom) {
		t.Fatalf("a sweep with a failing store: %v", err)
	}
}

func TestStagingFailures(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := &multipart.Service{Store: w.db, Blobs: w.blobs, Dir: file}
	if _, err := s.Create(ctx, "libs", "f", 1, "admin"); err == nil {
		t.Fatal("staging under a file")
	}
	if err := s.Sweep(ctx); err == nil {
		t.Fatal("sweeping a file")
	}
	ok := w.service()
	token, err := ok.Create(ctx, "libs", "f", 1, "admin")
	if err != nil {
		t.Fatal(err)
	}
	// The upload's staging directory replaced by a file.
	id, _, _ := strings.Cut(token, ".")
	if err := os.RemoveAll(filepath.Join(w.dir, id)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.dir, id), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := put(t, ok, token, 1, content(10)); err == nil {
		t.Fatal("a part staged under a file")
	}
	if err := os.Remove(filepath.Join(w.dir, id)); err != nil {
		t.Fatal(err)
	}
	if err := putFrom(t, ok, token, 1, errReader{}); err == nil {
		t.Fatal("a body that fails")
	}
}

// putFrom is put with a reader for the body.
func putFrom(t *testing.T, s *multipart.Service, token string, part int, body io.Reader) error {
	t.Helper()
	ref, err := s.PartURL(context.Background(), token, part)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(ref)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	return s.PutPart(context.Background(), q.Get("upload"), part, q.Get("signature"), body)
}

// errReader is a request body that breaks off.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errBoom }
