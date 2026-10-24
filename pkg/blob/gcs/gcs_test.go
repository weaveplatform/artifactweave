package gcs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

// testPart is the smallest part GCS's 256 KiB alignment allows, so a few
// MiB exercise many parts and folds.
const testPart = partAlign

func swap[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

func newFakeStore(t *testing.T, f *fakeGCS, sessions session.Store) *Store {
	t.Helper()
	swap(t, &minPartSize, testPart)
	if sessions == nil {
		sessions = session.NewMemory()
	}
	s, err := New(f, Options{Prefix: "art", PartSize: testPart, Sessions: sessions, Presign: true})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAccContractOnFake(t *testing.T) {
	blobtest.Run(t, func(t *testing.T) blob.Store { return newFakeStore(t, newFake(), nil) },
		blobtest.Options{LargeSize: 5*testPart + 1234})
}

// TestAccContractWithDurableSessions runs the contract with sessions kept in the
// metadata store's uploads table, as the server configures it.
func TestAccContractWithDurableSessions(t *testing.T) {
	blobtest.Run(t, func(t *testing.T) blob.Store {
		return newFakeStore(t, newFake(), session.InUploads(inmem.New().Uploads(), "gcs:test"))
	}, blobtest.Options{LargeSize: 3*testPart + 7})
}

func TestUnitNewValidates(t *testing.T) {
	swap(t, &minPartSize, testPart)
	sessions := session.NewMemory()
	for name, o := range map[string]Options{
		"no sessions": {},
		"unaligned":   {Sessions: sessions, PartSize: testPart + 1},
		"too small":   {Sessions: sessions, PartSize: testPart / 2},
	} {
		if _, err := New(newFake(), o); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := New(nil, Options{Sessions: sessions}); !errors.Is(err, ErrConfig) {
		t.Fatal("a nil API must be refused")
	}
	s, err := New(newFake(), Options{Prefix: "/a/b", Sessions: sessions})
	if err != nil || s.prefix != "a/b/" || s.PartSize() != DefaultPartSize {
		t.Fatalf("New = %+v, %v", s, err)
	}
}

func put(
	ctx context.Context,
	t *testing.T,
	s *Store,
	b []byte,
	opts ...blob.UploadOption,
) (blob.Upload, blob.Info) {
	t.Helper()
	u, err := s.BeginUpload(ctx, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Append(ctx, bytes.NewReader(b)); err != nil {
		t.Fatal(err)
	}
	info, err := u.Commit(ctx, blob.Digests{})
	if err != nil {
		t.Fatal(err)
	}
	return u, info
}

func TestUnitLayoutAndCleanup(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	s := newFakeStore(t, f, nil)
	body := blobtest.Content(1, 3*testPart+10)
	_, info := put(ctx, t, s, body)
	want := "art/sha256/" + info.Digests.SHA256[:2] + "/" + info.Digests.SHA256
	if got := f.names("art/"); len(got) != 1 || got[0] != want {
		t.Fatalf("objects after commit = %v, want only %s", got, want)
	}
	if len(f.composes) != 1 || len(f.composes[0]) != 4 {
		t.Fatalf("composes = %v, want one of 4 parts", f.composes)
	}
}

// TestUnitFoldBeyondThirtyTwoParts uploads more parts than one compose accepts:
// the fake refuses composes over 32 sources, so the content reads back only
// if the session folded on the way.
func TestUnitFoldBeyondThirtyTwoParts(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	s := newFakeStore(t, f, nil)
	body := blobtest.Content(2, 70*testPart+3)
	u, err := s.BeginUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Append in chunks across requests, resuming each time.
	for off := 0; off < len(body); off += 9 * testPart {
		ru, err := s.ResumeUpload(ctx, u.ID())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ru.Append(
			ctx,
			bytes.NewReader(body[off:min(off+9*testPart, len(body))]),
		); err != nil {
			t.Fatal(err)
		}
	}
	ru, _ := s.ResumeUpload(ctx, u.ID())
	info, err := ru.Commit(ctx, blob.Digests{})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.composes) != 3 {
		t.Fatalf("composes = %d, want two folds and the commit", len(f.composes))
	}
	r, _, err := s.Open(ctx, info.Digests.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("read back %d bytes, %v", len(got), err)
	}
	if left := f.names("art/_uploads/"); len(left) != 0 {
		t.Fatalf("leftovers: %v", left)
	}
}

func TestUnitDeclaredDigest(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	s := newFakeStore(t, f, nil)
	body := blobtest.Content(3, 2*testPart+1)
	d := blobtest.Digest(body)
	if _, info := put(ctx, t, s, body, blob.WithExpectedSHA256(d.SHA256)); info.Digests != d {
		t.Fatalf("info = %+v", info.Digests)
	}
	u, err := s.BeginUpload(ctx, blob.WithExpectedSHA256(blobtest.Digest([]byte("other")).SHA256))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Append(ctx, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Commit(ctx, blob.Digests{}); !errors.Is(err, blob.ErrDigestMismatch) {
		t.Fatalf("wrong declared digest: %v", err)
	}
	if left := f.names("art/_uploads/"); len(left) != 0 {
		t.Fatalf("a mismatched upload left %v", left)
	}
}

// TestUnitPartWrittenTwiceIsAdopted is a part whose write landed but whose
// response was lost: the retry finds its own copy and adopts it, but never
// adopts other bytes under that name.
func TestUnitPartWrittenTwiceIsAdopted(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	s := newFakeStore(t, f, nil)
	body := blobtest.Content(4, testPart+5)
	u, _ := s.BeginUpload(ctx)
	first := fmt.Sprintf("art/_uploads/%s/p-%08d", u.ID(), 0)
	f.put(first, body[:testPart])
	if _, err := u.Append(ctx, bytes.NewReader(body)); err != nil {
		t.Fatalf("same bytes under the part name: %v", err)
	}
	if _, err := u.Commit(ctx, blob.Digests{}); err != nil {
		t.Fatal(err)
	}

	u, _ = s.BeginUpload(ctx)
	f.put(fmt.Sprintf("art/_uploads/%s/p-%08d", u.ID(), 0), []byte("someone else"))
	if _, err := u.Append(ctx, bytes.NewReader(body)); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("other bytes under the part name: %v", err)
	}
	// The failed part's bytes stay counted and pending for a retry.
	if u.Size() != testPart {
		t.Fatalf("counted bytes = %d, want the one buffered part", u.Size())
	}
}

// TestUnitFoldAdoptsEarlierFold is a fold that landed (deleting its sources) but
// was not saved: the retry finds the fold and continues from it.
func TestUnitFoldAdoptsEarlierFold(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	s := newFakeStore(t, f, nil)
	body := blobtest.Content(5, 33*testPart+1)
	u, _ := s.BeginUpload(ctx)
	if _, err := u.Append(ctx, bytes.NewReader(body[:32*testPart])); err != nil {
		t.Fatal(err)
	}
	f.put(fmt.Sprintf("art/_uploads/%s/c-%08d", u.ID(), 32), body[:32*testPart])
	if _, err := u.Append(ctx, bytes.NewReader(body[32*testPart:])); err != nil {
		t.Fatal(err)
	}
	info, err := u.Commit(ctx, blob.Digests{})
	if err != nil || info.Digests != blobtest.Digest(body) {
		t.Fatalf("Commit = %+v, %v", info, err)
	}

	u, _ = s.BeginUpload(ctx)
	if _, err := u.Append(ctx, bytes.NewReader(body[:32*testPart])); err != nil {
		t.Fatal(err)
	}
	f.put(fmt.Sprintf("art/_uploads/%s/c-%08d", u.ID(), 32), []byte("not the fold"))
	if _, err := u.Append(
		ctx,
		bytes.NewReader(body[32*testPart:]),
	); !errors.Is(
		err,
		ErrPrecondition,
	) {
		t.Fatalf("a different fold must not be adopted: %v", err)
	}
}

func TestUnitAppendFailures(t *testing.T) {
	ctx := context.Background()
	big := blobtest.Content(6, 33*testPart)
	for _, tc := range []struct{ op, want string }{
		{"Write", "write"},
		{"Attrs", "check existing"},
		{"Compose", "fold into"},
	} {
		t.Run(tc.op, func(t *testing.T) {
			f := newFake()
			s := newFakeStore(t, f, nil)
			u, _ := s.BeginUpload(ctx)
			if tc.op == "Attrs" {
				f.put(fmt.Sprintf("art/_uploads/%s/p-%08d", u.ID(), 0), big[:testPart])
			}
			f.failOn(tc.op, 0, errInjected)
			n, err := u.Append(ctx, bytes.NewReader(big))
			if !errors.Is(err, errInjected) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Append = %d, %v", n, err)
			}
		})
	}
	t.Run("fold save fails", func(t *testing.T) {
		st := &flaky{Memory: session.NewMemory()}
		s := newFakeStore(t, newFake(), st)
		u, _ := s.BeginUpload(ctx)
		if _, err := u.Append(ctx, bytes.NewReader(big[:32*testPart])); err != nil {
			t.Fatal(err)
		}
		st.failPut = errInjected
		if _, err := u.Append(
			ctx,
			bytes.NewReader(big[32*testPart:]),
		); !errors.Is(
			err,
			errInjected,
		) {
			t.Fatal(err)
		}
	})
}

type flaky struct {
	*session.Memory
	failPut, failDelete error
}

func (f *flaky) Put(ctx context.Context, id string, state []byte, size int64) error {
	if f.failPut != nil {
		return f.failPut
	}
	return f.Memory.Put(ctx, id, state, size)
}

func (f *flaky) Delete(ctx context.Context, id string) error {
	if f.failDelete != nil {
		return f.failDelete
	}
	return f.Memory.Delete(ctx, id)
}

func TestUnitCommitFailures(t *testing.T) {
	ctx := context.Background()
	small := []byte("small")
	big := blobtest.Content(7, 2*testPart+5)
	cases := []struct {
		name  string
		body  []byte
		op    string
		after int
	}{
		{"target check", small, "Attrs", 0},
		{"single write", small, "Write", 0},
		{"tail part", big, "Write", 0},
		{"compose", big, "Compose", 0},
		{"cleanup list", big, "List", 0},
		{"cleanup delete", big, "Delete", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			s := newFakeStore(t, f, nil)
			u, _ := s.BeginUpload(ctx)
			if _, err := u.Append(ctx, bytes.NewReader(tc.body)); err != nil {
				t.Fatal(err)
			}
			f.failOn(tc.op, tc.after, errInjected)
			if tc.name == "cleanup delete" {
				// The fake hard-deletes compose sources as real GCS does; an
				// orphan part (written, never recorded) is what cleanup finds.
				f.put("art/_uploads/"+u.ID()+"/p-99999999", []byte("orphan"))
			}
			if _, err := u.Commit(ctx, blob.Digests{}); !errors.Is(err, errInjected) {
				t.Fatalf("%s: %v", tc.name, err)
			}
		})
	}
	t.Run("save after tail fails", func(t *testing.T) {
		st := &flaky{Memory: session.NewMemory()}
		s := newFakeStore(t, newFake(), st)
		u, _ := s.BeginUpload(ctx)
		if _, err := u.Append(ctx, bytes.NewReader(big)); err != nil {
			t.Fatal(err)
		}
		st.failPut = errInjected
		if _, err := u.Commit(ctx, blob.Digests{}); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
	t.Run("forget fails", func(t *testing.T) {
		st := &flaky{Memory: session.NewMemory()}
		s := newFakeStore(t, newFake(), st)
		u, _ := s.BeginUpload(ctx)
		st.failDelete = errInjected
		if _, err := u.Commit(ctx, blob.Digests{}); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
	t.Run("composed blob does not match", func(t *testing.T) {
		f := newFake()
		s := newFakeStore(t, f, nil)
		u, _ := s.BeginUpload(ctx)
		if _, err := u.Append(ctx, bytes.NewReader(big)); err != nil {
			t.Fatal(err)
		}
		f.badCompose = true
		if _, err := u.Commit(
			ctx,
			blob.Digests{},
		); err == nil ||
			!strings.Contains(err.Error(), "crc32c") {
			t.Fatalf("mismatched compose: %v", err)
		}
		if got := f.names("art/sha256/"); len(got) != 0 {
			t.Fatalf("a mismatched blob stayed at its content address: %v", got)
		}
	})
	t.Run("compose finds the blob stored meanwhile", func(t *testing.T) {
		f := newFake()
		s := newFakeStore(t, f, nil)
		u, _ := s.BeginUpload(ctx)
		if _, err := u.Append(ctx, bytes.NewReader(big)); err != nil {
			t.Fatal(err)
		}
		d := blobtest.Digest(big)
		// The target appears after the commit's existence check, the shape
		// of two sessions committing the same bytes at once.
		f.failOn("Attrs", 0, fmt.Errorf("%w: race", blob.ErrNotFound))
		f.put("art/sha256/"+d.SHA256[:2]+"/"+d.SHA256, big)
		if info, err := u.Commit(ctx, blob.Digests{}); err != nil || info.Digests != d {
			t.Fatalf("Commit = %+v, %v", info, err)
		}
	})
	t.Run("abort removes parts", func(t *testing.T) {
		f := newFake()
		s := newFakeStore(t, f, nil)
		u, _ := s.BeginUpload(ctx)
		if _, err := u.Append(ctx, bytes.NewReader(big)); err != nil {
			t.Fatal(err)
		}
		f.failOn("List", 0, errInjected)
		if err := u.Abort(ctx); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
		f.failOn("List", 100, nil)
		if err := u.Abort(ctx); err != nil {
			t.Fatal(err)
		}
		if left := f.names("art/_uploads/"); len(left) != 0 {
			t.Fatalf("leftovers: %v", left)
		}
	})
}

func TestUnitResumeWithDamagedState(t *testing.T) {
	ctx := context.Background()
	st := session.NewMemory()
	s := newFakeStore(t, newFake(), st)
	u, _ := s.BeginUpload(ctx)
	_ = st.Put(ctx, u.ID(), []byte(`{"hasher":"anVuaw=="}`), 0)
	if _, err := s.ResumeUpload(ctx, u.ID()); !errors.Is(err, blob.ErrUploadUnknown) {
		t.Fatal(err)
	}
	if _, err := s.BeginUpload(
		ctx,
		blob.WithExpectedSHA256("nope"),
	); !errors.Is(
		err,
		blob.ErrInvalidDigest,
	) {
		t.Fatal(err)
	}
}

func TestUnitReadErrors(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	s := newFakeStore(t, f, nil)
	_, info := put(ctx, t, s, []byte("read me back"))
	sha := info.Digests.SHA256

	f.failOn("Read", 0, errInjected)
	r, _, err := s.Open(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(r); !errors.Is(err, errInjected) {
		t.Fatalf("read failure: %v", err)
	}
	f.failOn("Read", 100, nil)

	f.shortRead = true
	r, _, _ = s.Open(ctx, sha)
	if _, err := io.ReadAll(r); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short body: %v", err)
	}
	f.shortRead = false

	r, _, _ = s.Open(ctx, sha)
	buf := make([]byte, 4)
	if _, err := r.Read(buf); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Seek(0, 42); !errors.Is(err, ErrSeek) {
		t.Fatalf("bad whence: %v", err)
	}
	if _, err := r.Seek(-1, io.SeekStart); !errors.Is(err, ErrSeek) {
		t.Fatalf("negative position: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal("closing twice must succeed")
	}
	r, _, _ = s.Open(ctx, sha)
	_, _ = r.Read(buf)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	f.failOn("Attrs", 0, errInjected)
	if _, err := s.Exists(ctx, sha); !errors.Is(err, errInjected) {
		t.Fatalf("Exists: %v", err)
	}
	if _, _, err := s.Open(ctx, sha); !errors.Is(err, errInjected) {
		t.Fatalf("Open: %v", err)
	}
}

type failingBody struct{ io.Reader }

func (failingBody) Close() error { return errInjected }

func TestUnitReaderCloseError(t *testing.T) {
	r := &reader{name: "x", size: 10, body: failingBody{strings.NewReader("abc")}}
	if err := r.Close(); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
	r = &reader{name: "x", size: 10, body: io.NopCloser(errReader{})}
	if _, err := r.Read(make([]byte, 4)); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errInjected }

func TestUnitDeleteWalkPresignErrors(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	s := newFakeStore(t, f, nil)
	_, info := put(ctx, t, s, []byte("walk and sign"))
	sha := info.Digests.SHA256

	f.put("art/sha256/zz/not-a-digest", []byte("x"))
	var seen []string
	if err := s.Walk(ctx, func(i blob.Info) error {
		seen = append(seen, i.Digests.SHA256)
		return nil
	}); err != nil || len(seen) != 1 || seen[0] != sha {
		t.Fatalf("Walk = %v, %v", seen, err)
	}
	if err := s.Walk(
		ctx,
		func(blob.Info) error { return errInjected },
	); !errors.Is(
		err,
		errInjected,
	) {
		t.Fatalf("Walk stop: %v", err)
	}

	f.failOn("Delete", 0, errInjected)
	if err := s.Delete(ctx, sha); !errors.Is(err, errInjected) {
		t.Fatalf("Delete: %v", err)
	}
	f.failOn("Delete", 100, nil)

	if !s.Signs(blob.ViaStorage) || s.Signs(blob.ViaCDN) {
		t.Fatal("Presign signs storage URLs only")
	}
	for _, ttl := range []time.Duration{8 * 24 * time.Hour, 0} {
		if _, err := s.SignURL(ctx, sha, blob.SignOptions{TTL: ttl}); !errors.Is(err, ErrConfig) {
			t.Fatalf("ttl %s: %v", ttl, err)
		}
	}
	_, err := s.SignURL(ctx, "bad", blob.SignOptions{TTL: time.Minute})
	if !errors.Is(err, blob.ErrInvalidDigest) {
		t.Fatalf("invalid digest: %v", err)
	}
	_, err = s.SignURL(ctx, sha, blob.SignOptions{Via: blob.ViaCDN, TTL: time.Minute})
	if !errors.Is(err, blob.ErrCannotSign) {
		t.Fatalf("no CDN: %v", err)
	}
	plain, err := s.SignURL(ctx, sha, blob.SignOptions{TTL: time.Minute})
	if err != nil || strings.Contains(plain, "response-content") {
		t.Fatalf("no overrides asked for: %q %v", plain, err)
	}
	f.failOn("SignedURL", 0, errInjected)
	_, err = s.SignURL(ctx, sha, blob.SignOptions{TTL: time.Minute})
	if !errors.Is(err, errInjected) {
		t.Fatalf("signing failure: %v", err)
	}
	off, err := New(f, Options{Sessions: session.NewMemory(), PartSize: testPart})
	if err != nil {
		t.Fatal(err)
	}
	if off.Signs(blob.ViaStorage) {
		t.Fatal("presigning off signs nothing")
	}
	_, err = off.SignURL(ctx, sha, blob.SignOptions{TTL: time.Minute})
	if !errors.Is(err, blob.ErrCannotSign) {
		t.Fatalf("presigning off: %v", err)
	}
	if s.ObjectKey(sha) != "art/sha256/"+sha[:2]+"/"+sha {
		t.Fatalf("object key %q", s.ObjectKey(sha))
	}
}
