package s3

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
)

// writePath is one way a commit reaches the content key, and the operation
// that carries the must-not-exist condition on that way.
type writePath struct {
	name    string
	body    []byte
	declare bool
	// copyAbove, when set, lowers the single-copy limit so the copy into
	// place is a multipart copy.
	copyAbove bool
	op        string
}

func writePaths() []writePath {
	big := blobtest.Content(7, 2*testPart+5)
	return []writePath{
		{name: "small put", body: []byte("small"), op: "PutObject"},
		{
			name:    "multipart to the content key",
			body:    big,
			declare: true,
			op:      "CompleteMultipartUpload",
		},
		{name: "multipart then copy", body: big, op: "CopyObject"},
		{
			name:      "multipart then multipart copy",
			body:      big,
			copyAbove: true,
			op:        "CompleteMultipartUpload",
		},
	}
}

func (p writePath) commit(ctx context.Context, t *testing.T, s *Store) (blob.Info, error) {
	t.Helper()
	var opts []blob.UploadOption
	if p.declare {
		opts = append(opts, blob.WithExpectedSHA256(blobtest.Digest(p.body).SHA256))
	}
	u, err := s.BeginUpload(ctx, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Append(ctx, bytes.NewReader(p.body)); err != nil {
		t.Fatal(err)
	}
	return u.Commit(ctx, blob.Digests{}) //nolint:wrapcheck // the store's own error
}

func (p writePath) setup(t *testing.T) (*fakeS3, *Store) {
	t.Helper()
	f := newFake()
	s := newFakeStore(t, f, nil)
	if p.copyAbove {
		swap(t, &copyLimit, testPart)
		swap(t, &copyChunk, 100<<10)
	}
	return f, s
}

func stored(t *testing.T, f *fakeS3, s *Store, body []byte) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	got, ok := f.objects[s.blobKey(blobtest.Digest(body).SHA256)]
	if !ok || !bytes.Equal(got, body) {
		t.Fatalf(
			"content key holds %d bytes (present %t), want the %d written",
			len(got),
			ok,
			len(body),
		)
	}
	for k := range f.objects {
		if strings.Contains(k, "_uploads/") {
			t.Fatalf("temporary %s left behind", k)
		}
	}
	if len(f.uploads) != 0 {
		t.Fatalf("%d multipart uploads left open", len(f.uploads))
	}
}

// A fresh blob is written once, conditionally, with no HeadObject first.
func TestUnitCommitWritesConditionallyWithoutAHead(t *testing.T) {
	ctx := context.Background()
	for _, p := range writePaths() {
		t.Run(p.name, func(t *testing.T) {
			f, s := p.setup(t)
			if _, err := p.commit(ctx, t, s); err != nil {
				t.Fatal(err)
			}
			if f.calls["HeadObject"] != 0 {
				t.Fatalf("%d HeadObject calls, want none", f.calls["HeadObject"])
			}
			if f.conditional[p.op] != 1 {
				t.Fatalf("conditional %s writes: %v", p.op, f.conditional)
			}
			stored(t, f, s, p.body)
		})
	}
}

// A blob already stored is adopted: the second commit succeeds, writes
// nothing over it and leaves no temporary object or open upload.
func TestUnitCommitAdoptsAStoredBlob(t *testing.T) {
	ctx := context.Background()
	for _, p := range writePaths() {
		t.Run(p.name, func(t *testing.T) {
			f, s := p.setup(t)
			first, err := p.commit(ctx, t, s)
			if err != nil {
				t.Fatal(err)
			}
			again, err := p.commit(ctx, t, s)
			if err != nil {
				t.Fatal(err)
			}
			if again != first {
				t.Fatalf("second commit %+v, want %+v", again, first)
			}
			if f.conditional[p.op] != 2 {
				t.Fatalf("conditional %s writes: %v", p.op, f.conditional)
			}
			stored(t, f, s, p.body)
		})
	}
}

// A taken content key holding other bytes is refused, not adopted.
func TestUnitCommitRefusesAStoredObjectOfAnotherSize(t *testing.T) {
	ctx := context.Background()
	for _, p := range writePaths() {
		t.Run(p.name, func(t *testing.T) {
			f, s := p.setup(t)
			f.objects[s.blobKey(blobtest.Digest(p.body).SHA256)] = []byte("damaged")
			_, err := p.commit(ctx, t, s)
			if !errors.Is(err, errPrecondition) || !strings.Contains(err.Error(), "other content") {
				t.Fatalf("got %v, want a refusal naming other content", err)
			}
		})
	}
}

// The check of a taken key failing fails the commit.
func TestUnitCommitAdoptCheckFails(t *testing.T) {
	ctx := context.Background()
	p := writePaths()[0]
	f, s := p.setup(t)
	if _, err := p.commit(ctx, t, s); err != nil {
		t.Fatal(err)
	}
	f.failOn("HeadObject", 0, errInjected)
	if _, err := p.commit(ctx, t, s); !errors.Is(err, errInjected) {
		t.Fatalf("got %v, want the injected HeadObject error", err)
	}
}

// A retried write whose first attempt landed unseen gets 412 for its own
// bytes, and adopts them instead of reporting a conflict.
func TestUnitCommitAdoptsItsOwnLandedRetry(t *testing.T) {
	ctx := context.Background()
	for _, p := range writePaths() {
		t.Run(p.name, func(t *testing.T) {
			f, s := p.setup(t)
			f.landFirst = true
			if _, err := p.commit(ctx, t, s); err != nil {
				t.Fatal(err)
			}
			stored(t, f, s, p.body)
		})
	}
}

// A store that refuses If-None-Match as NotImplemented is written without
// it, and is not asked again.
func TestUnitCommitWithoutConditionalSupport(t *testing.T) {
	ctx := context.Background()
	for _, p := range writePaths() {
		t.Run(p.name, func(t *testing.T) {
			f, s := p.setup(t)
			f.conds = "unsupported"
			if _, err := p.commit(ctx, t, s); err != nil {
				t.Fatal(err)
			}
			stored(t, f, s, p.body)
			asked := f.conditional[p.op]
			other := p
			other.body = append(append([]byte(nil), p.body...), 'x')
			if _, err := other.commit(ctx, t, s); err != nil {
				t.Fatal(err)
			}
			if f.conditional[p.op] != asked {
				t.Fatalf("asked again after NotImplemented: %v", f.conditional)
			}
			stored(t, f, s, other.body)
		})
	}
}

// A store that ignores the condition (MinIO on CopyObject) overwrites a
// stored blob with the same bytes, which is harmless for content keys.
func TestUnitCommitWhereTheConditionIsIgnored(t *testing.T) {
	ctx := context.Background()
	for _, p := range writePaths() {
		t.Run(p.name, func(t *testing.T) {
			f, s := p.setup(t)
			f.conds = "ignore"
			for range 2 {
				if _, err := p.commit(ctx, t, s); err != nil {
					t.Fatal(err)
				}
			}
			stored(t, f, s, p.body)
		})
	}
}

// Concurrent commits of one blob all succeed: one writes, the others adopt.
func TestUnitConcurrentCommitsOfOneBlob(t *testing.T) {
	ctx := context.Background()
	for _, p := range writePaths() {
		t.Run(p.name, func(t *testing.T) {
			f, s := p.setup(t)
			var wg sync.WaitGroup
			errs := make(chan error, 8)
			for range 8 {
				wg.Go(func() {
					var opts []blob.UploadOption
					if p.declare {
						opts = append(opts, blob.WithExpectedSHA256(blobtest.Digest(p.body).SHA256))
					}
					u, err := s.BeginUpload(ctx, opts...)
					if err == nil {
						_, err = u.Append(ctx, bytes.NewReader(p.body))
					}
					if err == nil {
						_, err = u.Commit(ctx, blob.Digests{})
					}
					errs <- err
				})
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			stored(t, f, s, p.body)
		})
	}
}
