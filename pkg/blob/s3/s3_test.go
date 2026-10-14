package s3

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/smithy-go"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

var errInjected = errors.New("injected")

const testPart = 256 << 10

func swap[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

// small lowers S3's part floor so the fake exercises multipart with small
// blobs; real S3 and MinIO keep the 5 MiB floor (see minio_test.go).
func small(t *testing.T) {
	t.Helper()
	swap(t, &minPartSize, 1)
}

func newFakeStore(t *testing.T, f *fakeS3, states session.Store) *Store {
	t.Helper()
	small(t)
	if states == nil {
		states = session.NewMemory()
	}
	s, err := New(
		f,
		Options{
			Bucket:   "b",
			Prefix:   "art",
			PartSize: testPart,
			Sessions: states,
			Presign:  fakePresign{},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestContractOnFake(t *testing.T) {
	small(t)
	blobtest.Run(t, func(t *testing.T) blob.Store { return newFakeStore(t, newFake(), nil) },
		blobtest.Options{LargeSize: 4*testPart + 1234})
}

func TestNewValidates(t *testing.T) {
	states := session.NewMemory()
	cases := map[string]Options{
		"no bucket":  {Sessions: states},
		"no states":  {Bucket: "b"},
		"small part": {Bucket: "b", Sessions: states, PartSize: 1 << 20},
	}
	for name, o := range cases {
		if _, err := New(newFake(), o); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := New(nil, Options{Bucket: "b", Sessions: states}); !errors.Is(err, ErrConfig) {
		t.Errorf("nil client: %v", err)
	}
	s, err := New(newFake(), Options{Bucket: "b", Prefix: "/a/b", Sessions: states})
	if err != nil || s.prefix != "a/b/" || s.PartSize() != DefaultPartSize {
		t.Fatalf("defaults: %+v %v", s, err)
	}
}

func TestKeysAreContentAddressedUnderPrefix(t *testing.T) {
	f := newFake()
	s := newFakeStore(t, f, nil)
	info := blobtest.Put(context.Background(), t, s, []byte("hello"))
	sha := info.Digests.SHA256
	if _, ok := f.objects["art/sha256/"+sha[:2]+"/"+sha]; !ok {
		t.Fatalf("unexpected key layout: %v", keys(f))
	}
	if len(f.objects) != 1 || len(f.uploads) != 0 {
		t.Fatalf("a small blob must be a single PutObject: %v %v", keys(f), f.uploads)
	}
}

func keys(f *fakeS3) []string {
	var out []string
	for k := range f.objects {
		out = append(out, k)
	}
	return out
}

func TestDeclaredDigestSkipsTheCopy(t *testing.T) {
	small(t)
	ctx := context.Background()
	f := newFake()
	s := newFakeStore(t, f, nil)
	body := blobtest.Content(1, 3*testPart)
	d := blobtest.Digest(body)
	u, err := s.BeginUpload(ctx, blob.WithExpectedSHA256(d.SHA256))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Append(ctx, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Commit(ctx, blob.Digests{}); err != nil {
		t.Fatal(err)
	}
	if f.calls["CopyObject"] != 0 || f.calls["DeleteObject"] != 0 {
		t.Fatalf("declared digest still copied: %v", f.calls)
	}
}

func TestMultipartCopyAboveTheCopyLimit(t *testing.T) {
	small(t)
	swap(t, &copyLimit, testPart)
	swap(t, &copyChunk, 100<<10)
	ctx := context.Background()
	f := newFake()
	s := newFakeStore(t, f, nil)
	body := blobtest.Content(2, 3*testPart+77)
	info := blobtest.Put(ctx, t, s, body)
	if f.calls["UploadPartCopy"] < 2 || f.calls["CopyObject"] != 0 {
		t.Fatalf("expected a multipart copy: %v", f.calls)
	}
	rc, _, err := s.Open(ctx, info.Digests.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(got, body) {
		t.Fatal("multipart copy changed the content")
	}

	for _, op := range []string{"CreateMultipartUpload", "UploadPartCopy", "CompleteMultipartUpload"} {
		t.Run(op+" fails", func(t *testing.T) {
			f := newFake()
			s := newFakeStore(t, f, nil)
			// The session's own multipart upload uses the first create call.
			after := 0
			if op == "CreateMultipartUpload" || op == "CompleteMultipartUpload" {
				after = 1
			}
			f.failOn(op, after, errInjected)
			u, err := s.BeginUpload(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := u.Append(ctx, bytes.NewReader(body)); err != nil {
				t.Fatal(err)
			}
			if _, err := u.Commit(ctx, blob.Digests{}); !errors.Is(err, errInjected) {
				t.Fatalf("%s: %v", op, err)
			}
		})
	}
}

func TestAppendFailures(t *testing.T) {
	small(t)
	ctx := context.Background()
	body := blobtest.Content(3, 2*testPart+10)

	t.Run("part upload fails, bytes stay pending and retry", func(t *testing.T) {
		f := newFake()
		s := newFakeStore(t, f, nil)
		f.failOn("UploadPart", 0, errInjected)
		u, err := s.BeginUpload(ctx)
		if err != nil {
			t.Fatal(err)
		}
		n, err := u.Append(ctx, bytes.NewReader(body[:testPart]))
		if !errors.Is(err, errInjected) || n != testPart || u.Size() != testPart {
			t.Fatalf("n %d size %d %v", n, u.Size(), err)
		}
		delete(f.fail, "UploadPart")
		r, err := s.ResumeUpload(ctx, u.ID())
		if err != nil || r.Size() != testPart {
			t.Fatalf("resume: %v", err)
		}
		if _, err := r.Append(ctx, bytes.NewReader(body[testPart:])); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Commit(ctx, blobtest.Digest(body)); err != nil {
			t.Fatalf("commit after retry: %v", err)
		}
	})
	t.Run("create multipart fails", func(t *testing.T) {
		f := newFake()
		s := newFakeStore(t, f, nil)
		f.failOn("CreateMultipartUpload", 0, errInjected)
		u, _ := s.BeginUpload(ctx)
		if _, err := u.Append(ctx, bytes.NewReader(body)); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
	t.Run("multipart vanished", func(t *testing.T) {
		f := newFake()
		s := newFakeStore(t, f, nil)
		u, _ := s.BeginUpload(ctx)
		if _, err := u.Append(ctx, bytes.NewReader(body[:testPart])); err != nil {
			t.Fatal(err)
		}
		f.uploads = map[string]*fakeMultipart{}
		if _, err := u.Append(
			ctx,
			bytes.NewReader(body[testPart:]),
		); !errors.Is(
			err,
			blob.ErrUploadUnknown,
		) {
			t.Fatalf("a lost multipart upload must end the session: %v", err)
		}
		if err := u.Abort(ctx); err != nil {
			t.Fatalf("abort of a vanished multipart: %v", err)
		}
	})
	t.Run("reader fails", func(t *testing.T) {
		s := newFakeStore(t, newFake(), nil)
		u, _ := s.BeginUpload(ctx)
		if _, err := u.Append(
			ctx,
			io.MultiReader(bytes.NewReader([]byte("ab")), errReader{}),
		); !errors.Is(
			err,
			errInjected,
		) {
			t.Fatal(err)
		}
		if u.Size() != 2 {
			t.Fatalf("counted bytes lost: %d", u.Size())
		}
	})
	t.Run("state save fails", func(t *testing.T) {
		st := &flakyStates{Memory: session.NewMemory()}
		s := newFakeStore(t, newFake(), st)
		u, _ := s.BeginUpload(ctx)
		st.failPut = errInjected
		if _, err := u.Append(ctx, bytes.NewReader([]byte("x"))); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
		if _, err := u.Append(
			ctx,
			io.MultiReader(bytes.NewReader([]byte("x")), errReader{}),
		); !errors.Is(
			err,
			errInjected,
		) {
			t.Fatal(err)
		}
		if _, err := s.BeginUpload(ctx); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errInjected }

type flakyStates struct {
	*session.Memory
	failPut, failDelete error
}

func (f *flakyStates) Put(ctx context.Context, id string, state []byte, size int64) error {
	if f.failPut != nil {
		return f.failPut
	}
	return f.Memory.Put(ctx, id, state, size)
}

func (f *flakyStates) Delete(ctx context.Context, id string) error {
	if f.failDelete != nil {
		return f.failDelete
	}
	return f.Memory.Delete(ctx, id)
}

func TestCommitFailures(t *testing.T) {
	small(t)
	ctx := context.Background()
	big := blobtest.Content(4, 2*testPart+5)
	cases := []struct {
		op    string
		after int
		body  []byte
	}{
		{"HeadObject", 0, []byte("small")},
		{"PutObject", 0, []byte("small")},
		{"UploadPart", 2, big},
		{"CompleteMultipartUpload", 0, big},
		{"CopyObject", 0, big},
		{"DeleteObject", 0, big},
	}
	for _, tc := range cases {
		t.Run(tc.op, func(t *testing.T) {
			f := newFake()
			s := newFakeStore(t, f, nil)
			f.failOn(tc.op, tc.after, errInjected)
			u, _ := s.BeginUpload(ctx)
			if _, err := u.Append(ctx, bytes.NewReader(tc.body)); err != nil {
				t.Fatal(err)
			}
			if _, err := u.Commit(ctx, blob.Digests{}); !errors.Is(err, errInjected) {
				t.Fatalf("%s: %v", tc.op, err)
			}
		})
	}
	t.Run("state forget fails", func(t *testing.T) {
		st := &flakyStates{Memory: session.NewMemory()}
		s := newFakeStore(t, newFake(), st)
		u, _ := s.BeginUpload(ctx)
		st.failDelete = errInjected
		if _, err := u.Commit(ctx, blob.Digests{}); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
	t.Run("state save after final part fails", func(t *testing.T) {
		st := &flakyStates{Memory: session.NewMemory()}
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
	t.Run("abort multipart fails", func(t *testing.T) {
		f := newFake()
		s := newFakeStore(t, f, nil)
		u, _ := s.BeginUpload(ctx)
		if _, err := u.Append(ctx, bytes.NewReader(big)); err != nil {
			t.Fatal(err)
		}
		f.failOn("AbortMultipartUpload", 0, errInjected)
		if err := u.Abort(ctx); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
}

func TestResumeWithDamagedState(t *testing.T) {
	ctx := context.Background()
	st := session.NewMemory()
	s := newFakeStore(t, newFake(), st)
	u, _ := s.BeginUpload(ctx)
	_ = st.Put(ctx, u.ID(), []byte(`{"hasher":"anVuaw=="}`), 0)
	if _, err := s.ResumeUpload(ctx, u.ID()); !errors.Is(err, blob.ErrUploadUnknown) {
		t.Fatal(err)
	}
}

// TestContractWithDurableSessions runs the contract with sessions kept in the
// metadata store's uploads table, as the server configures it.
func TestContractWithDurableSessions(t *testing.T) {
	small(t)
	blobtest.Run(t, func(t *testing.T) blob.Store {
		return newFakeStore(t, newFake(), session.InUploads(inmem.New().Uploads(), "s3:test"))
	}, blobtest.Options{LargeSize: 2*testPart + 7})
}

func TestReadErrors(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	s := newFakeStore(t, f, nil)
	info := blobtest.Put(ctx, t, s, blobtest.Content(5, 1000))

	t.Run("get fails", func(t *testing.T) {
		rc, _, err := s.Open(ctx, info.Digests.SHA256)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rc.Close() }()
		f.failOn("GetObject", 0, errInjected)
		defer delete(f.fail, "GetObject")
		if _, err := rc.Read(make([]byte, 10)); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
	t.Run("object deleted under the reader", func(t *testing.T) {
		rc, _, err := s.Open(ctx, info.Digests.SHA256)
		if err != nil {
			t.Fatal(err)
		}
		f.objects = map[string][]byte{}
		if _, err := rc.Read(make([]byte, 10)); !errors.Is(err, blob.ErrNotFound) {
			t.Fatal(err)
		}
	})
	t.Run("short body", func(t *testing.T) {
		r := &reader{
			ctx:  ctx,
			s:    s,
			key:  "k",
			size: 10,
			body: io.NopCloser(bytes.NewReader([]byte("abc"))),
		}
		b, err := io.ReadAll(r)
		if !errors.Is(err, io.ErrUnexpectedEOF) || len(b) != 3 {
			t.Fatalf("%q %v", b, err)
		}
	})
	t.Run("body read and close errors", func(t *testing.T) {
		r := &reader{ctx: ctx, s: s, key: "k", size: 10, body: badBody{}}
		if _, err := r.Read(make([]byte, 4)); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
		if err := r.Close(); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
	t.Run("bad whence", func(t *testing.T) {
		r := &reader{size: 10}
		if _, err := r.Seek(0, 99); !errors.Is(err, ErrSeek) {
			t.Fatal(err)
		}
	})
	t.Run("stat and head errors", func(t *testing.T) {
		f.failOn("HeadObject", 0, errInjected)
		defer delete(f.fail, "HeadObject")
		if _, err := s.Stat(ctx, info.Digests.SHA256); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
		if _, err := s.Exists(ctx, info.Digests.SHA256); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
}

type badBody struct{}

func (badBody) Read([]byte) (int, error) { return 0, errInjected }
func (badBody) Close() error             { return errInjected }

func TestDeleteWalkPresignErrors(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	s := newFakeStore(t, f, nil)
	blobtest.Put(ctx, t, s, []byte("a"))
	sha := blobtest.Digest([]byte("a")).SHA256

	f.failOn("DeleteObject", 0, errInjected)
	if err := s.Delete(ctx, sha); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
	f.failOn("ListObjectsV2", 0, errInjected)
	if err := s.Walk(ctx, func(blob.Info) error { return nil }); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
	delete(f.fail, "ListObjectsV2")
	// A foreign object under the prefix is not a blob.
	f.objects["art/sha256/zz/not-a-digest"] = []byte("x")
	count := 0
	if err := s.Walk(ctx, func(blob.Info) error { count++; return nil }); err != nil || count != 1 {
		t.Fatalf("walk: %d %v", count, err)
	}

	if !s.Signs(blob.ViaStorage) || s.Signs(blob.ViaCDN) {
		t.Fatal("a store with a presign client signs storage URLs only")
	}
	signed, err := s.SignURL(ctx, sha, blob.SignOptions{
		TTL: time.Minute, Filename: "a b.tgz", ContentType: "application/x-gzip",
	})
	if err != nil || !strings.Contains(signed, sha) {
		t.Fatalf("%q %v", signed, err)
	}
	u, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	if q := u.Query(); q.Get("response-content-disposition") != blob.Disposition("a b.tgz") ||
		q.Get("response-content-type") != "application/x-gzip" || q.Get("expires") != "1m0s" {
		t.Fatalf("overrides and expiry: %v", q)
	}
	plain, err := s.SignURL(ctx, sha, blob.SignOptions{TTL: time.Minute})
	if err != nil || strings.Contains(plain, "response-content") {
		t.Fatalf("no overrides asked for: %q %v", plain, err)
	}
	for _, ttl := range []time.Duration{0, 7*24*time.Hour + time.Second} {
		if _, err := s.SignURL(ctx, sha, blob.SignOptions{TTL: ttl}); !errors.Is(err, ErrConfig) {
			t.Fatalf("ttl %s: %v", ttl, err)
		}
	}
	_, err = s.SignURL(ctx, "bad", blob.SignOptions{TTL: time.Minute})
	if !errors.Is(err, blob.ErrInvalidDigest) {
		t.Fatal(err)
	}
	_, err = s.SignURL(ctx, sha, blob.SignOptions{Via: blob.ViaCDN, TTL: time.Minute})
	if !errors.Is(err, blob.ErrCannotSign) {
		t.Fatal(err)
	}
	s.presign = fakePresign{err: errInjected}
	_, err = s.SignURL(ctx, sha, blob.SignOptions{TTL: time.Minute})
	if !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
	s.presign = nil
	if s.Signs(blob.ViaStorage) {
		t.Fatal("no presign client, no storage URLs")
	}
	_, err = s.SignURL(ctx, sha, blob.SignOptions{TTL: time.Minute})
	if !errors.Is(err, blob.ErrCannotSign) {
		t.Fatal(err)
	}
	if s.ObjectKey(sha) != "art/sha256/"+sha[:2]+"/"+sha {
		t.Fatalf("object key %q", s.ObjectKey(sha))
	}
}

func TestTranslate(t *testing.T) {
	plain := errors.New("plain")
	if translate(plain) != plain {
		t.Fatal("non-API errors pass through")
	}
	other := &smithy.GenericAPIError{Code: "SlowDown"}
	if !errors.Is(translate(other), other) || errors.Is(translate(other), blob.ErrNotFound) {
		t.Fatal("unrelated API errors pass through")
	}
	for code, want := range map[string]error{
		"NoSuchKey": blob.ErrNotFound, "NotFound": blob.ErrNotFound, "NoSuchUpload": blob.ErrUploadUnknown,
	} {
		if !errors.Is(translate(&smithy.GenericAPIError{Code: code}), want) {
			t.Errorf("%s", code)
		}
	}
}
