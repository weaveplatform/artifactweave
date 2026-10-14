package azure

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

var errInjected = errors.New("injected")

const (
	testPart  = 32 << 10
	testRange = 48 << 10
)

func newFakeStore(t *testing.T, f *fakeAzure, sessions session.Store) *Store {
	t.Helper()
	if sessions == nil {
		sessions = session.NewMemory()
	}
	s, err := New(f, Options{
		Prefix:        "art",
		PartSize:      testPart,
		CopyRangeSize: testRange,
		Concurrency:   3,
		Sessions:      sessions,
		Presign:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func swap[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

func TestContractOnFake(t *testing.T) {
	blobtest.Run(t, func(t *testing.T) blob.Store { return newFakeStore(t, newFake(), nil) },
		blobtest.Options{LargeSize: 3*testPart + 1234})
}

// TestContractWithDurableSessions runs the contract with sessions kept in the
// metadata store's uploads table, as the server configures it.
func TestContractWithDurableSessions(t *testing.T) {
	blobtest.Run(t, func(t *testing.T) blob.Store {
		return newFakeStore(t, newFake(), session.InUploads(inmem.New().Uploads(), "azure:test"))
	}, blobtest.Options{LargeSize: 2*testPart + 7})
}

func TestNewValidates(t *testing.T) {
	sessions := session.NewMemory()
	for name, o := range map[string]Options{
		"no sessions":       {},
		"negative part":     {Sessions: sessions, PartSize: -1},
		"huge part":         {Sessions: sessions, PartSize: maxBlockSize + 1},
		"negative range":    {Sessions: sessions, CopyRangeSize: -1},
		"negative parallel": {Sessions: sessions, Concurrency: -1},
	} {
		if _, err := New(newFake(), o); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := New(nil, Options{Sessions: sessions}); !errors.Is(err, ErrConfig) {
		t.Fatal("a nil API must be refused")
	}
	s, err := New(newFake(), Options{Prefix: "/a/b", Sessions: sessions})
	if err != nil {
		t.Fatal(err)
	}
	if s.prefix != "a/b/" || s.PartSize() != DefaultPartSize ||
		s.copyRange != DefaultCopyRangeSize ||
		s.concurrency != DefaultConcurrency {
		t.Fatalf("defaults: %+v", s)
	}
}

func TestBlockIDs(t *testing.T) {
	id := strings.Repeat("a", 32)
	ids := append(blockIDs(id, '-', 3), blockID(id, '+', 999_999))
	for _, b := range ids {
		raw, err := base64.StdEncoding.DecodeString(b)
		if err != nil || len(raw) != 39 {
			t.Fatalf("block id %q decodes to %d bytes: %v", b, len(raw), err)
		}
	}
	if ids[0] == blockID(id, '+', 1) {
		t.Fatal("staged and copy blocks must differ")
	}
}

func put(t *testing.T, s *Store, body []byte, opts ...blob.UploadOption) (blob.Upload, blob.Info) {
	t.Helper()
	ctx := context.Background()
	u, err := s.BeginUpload(ctx, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Append(ctx, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	info, err := u.Commit(ctx, blob.Digests{})
	if err != nil {
		t.Fatal(err)
	}
	return u, info
}

func TestUndeclaredCommitCopiesInRanges(t *testing.T) {
	f := newFake()
	s := newFakeStore(t, f, nil)
	body := blobtest.Content(1, 5*testPart+100) // 6 blocks, 4 copy ranges
	u, info := put(t, s, body)
	if got := f.count("StageBlockFromURL"); got != 4 {
		t.Fatalf("copy ranges = %d, want 4", got)
	}
	names := f.committedNames()
	if len(names) != 1 || names[0] != s.blobKey(info.Digests.SHA256) {
		t.Fatalf("committed blobs %v: the temporary blob must be gone", names)
	}
	if f.staged(s.tempKey(u.ID())) != 0 {
		t.Fatal("temporary blocks left behind")
	}
	p, _ := f.Properties(context.Background(), names[0])
	if got := hex.EncodeToString(p.MD5); got != blobtest.Digest(body).MD5 {
		t.Fatalf("content MD5 %s, want %s", got, blobtest.Digest(body).MD5)
	}
}

func TestDeclaredDigestSkipsTheCopy(t *testing.T) {
	f := newFake()
	s := newFakeStore(t, f, nil)
	body := blobtest.Content(2, 3*testPart+5)
	d := blobtest.Digest(body)
	_, info := put(t, s, body, blob.WithExpectedSHA256(d.SHA256))
	if info.Digests != d || f.count("StageBlockFromURL") != 0 {
		t.Fatalf("declared digest: %+v, %d copies", info, f.count("StageBlockFromURL"))
	}
	if names := f.committedNames(); len(names) != 1 || names[0] != s.blobKey(d.SHA256) {
		t.Fatalf("committed %v", names)
	}
}

func TestSmallBlobIsOnePutBlob(t *testing.T) {
	f := newFake()
	s := newFakeStore(t, f, nil)
	put(t, s, []byte("small"))
	if f.count("Upload") != 1 || f.count("StageBlock") != 0 || f.count("CommitBlockList") != 0 {
		t.Fatalf("calls %v", f.calls)
	}
}

// TestConcurrentIdenticalCommits covers losing the race to an identical
// upload at each commit path: the loser's write fails its precondition or
// finds its blocks discarded, and still succeeds.
func TestConcurrentIdenticalCommits(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		size     int
		declared bool
	}{
		"put blob": {100, false}, "declared": {3 * testPart, true}, "copied": {3 * testPart, false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			s := newFakeStore(t, f, nil)
			body := blobtest.Content(3, tc.size)
			d := blobtest.Digest(body)
			var opts []blob.UploadOption
			if tc.declared {
				opts = append(opts, blob.WithExpectedSHA256(d.SHA256))
			}
			loser, err := s.BeginUpload(ctx, opts...)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := loser.Append(ctx, bytes.NewReader(body)); err != nil {
				t.Fatal(err)
			}
			put(t, s, body, opts...)
			// The loser checked the content key before the winner committed.
			f.failOnce("Properties", blob.ErrNotFound)
			info, err := loser.Commit(ctx, blob.Digests{})
			if err != nil || info.Digests != d {
				t.Fatalf("losing commit = %+v, %v", info, err)
			}
		})
	}
}

func TestCommitRetryResumesTheCopy(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	sessions := session.NewMemory()
	s := newFakeStore(t, f, sessions)
	body := blobtest.Content(4, 5*testPart)
	u, err := s.BeginUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Append(ctx, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	f.failOn("StageBlockFromURL", 1, errInjected)
	if _, err := u.Commit(ctx, blob.Digests{}); !errors.Is(err, errInjected) {
		t.Fatalf("partial copy: %v", err)
	}
	f.clear("StageBlockFromURL")
	commits := f.count("CommitBlockList")
	again, err := newFakeStore(t, f, sessions).ResumeUpload(ctx, u.ID())
	if err != nil {
		t.Fatal(err)
	}
	info, err := again.Commit(ctx, blobtest.Digest(body))
	if err != nil {
		t.Fatal(err)
	}
	if f.count("CommitBlockList") != commits+1 {
		t.Fatal("the retry must not commit the temporary blob again")
	}
	rc, _, err := s.Open(ctx, info.Digests.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	if got, _ := io.ReadAll(rc); !bytes.Equal(got, body) {
		t.Fatal("content differs after the resumed copy")
	}
}

func TestBlockLimit(t *testing.T) {
	swap(t, &maxBlocks, 2)
	ctx := context.Background()
	s := newFakeStore(t, newFake(), nil)
	u, _ := s.BeginUpload(ctx)
	if _, err := u.Append(
		ctx,
		bytes.NewReader(blobtest.Content(5, 3*testPart)),
	); !errors.Is(
		err,
		ErrTooManyBlocks,
	) {
		t.Fatalf("third block: %v", err)
	}
	if u.Size() != 3*testPart {
		t.Fatalf("counted bytes lost: %d", u.Size())
	}
	// Two blocks fit, but copying them takes more ranges than the limit.
	u, _ = s.BeginUpload(ctx)
	if _, err := u.Append(ctx, bytes.NewReader(blobtest.Content(6, 2*testPart+1))); err != nil {
		t.Fatal(err)
	}
	swap(t, &maxBlocks, 3)
	s.copyRange = testPart / 2
	if _, err := u.Commit(ctx, blob.Digests{}); !errors.Is(err, ErrTooManyBlocks) {
		t.Fatalf("copy ranges over the limit: %v", err)
	}
}

func TestAppendFailures(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	s := newFakeStore(t, f, nil)
	u, _ := s.BeginUpload(ctx)
	f.failOn("StageBlock", 0, errInjected)
	if _, err := u.Append(
		ctx,
		bytes.NewReader(blobtest.Content(7, testPart+2)),
	); !errors.Is(
		err,
		errInjected,
	) {
		t.Fatal(err)
	}
	f.clear("StageBlock")
	body := blobtest.Content(7, testPart+2)
	if _, err := u.Append(ctx, bytes.NewReader(body[u.Size():])); err != nil {
		t.Fatalf("retry of the pending part: %v", err)
	}
	if _, err := u.Commit(ctx, blobtest.Digest(body)); err != nil {
		t.Fatal(err)
	}
}

func TestCommitFailures(t *testing.T) {
	ctx := context.Background()
	big := blobtest.Content(8, 3*testPart+3)
	cases := []struct {
		op    string
		after int
		size  int
		decl  bool
	}{
		{"Properties", 0, 10, false},       // existence check
		{"Upload", 0, 10, false},           // small blob
		{"Properties", 1, 10, false},       // confirm after put
		{"StageBlock", 0, len(big), false}, // tail block
		{"CommitBlockList", 0, len(big), false},
		{"StageBlockFromURL", 0, len(big), false},
		{"CommitBlockList", 1, len(big), false},
		{"Properties", 1, len(big), false},
		{"Delete", 0, len(big), false}, // temporary blob
		{"CommitBlockList", 0, len(big), true},
	}
	for _, tc := range cases {
		t.Run(tc.op, func(t *testing.T) {
			f := newFake()
			s := newFakeStore(t, f, nil)
			body := big[:tc.size]
			var opts []blob.UploadOption
			if tc.decl {
				opts = append(opts, blob.WithExpectedSHA256(blobtest.Digest(body).SHA256))
			}
			u, _ := s.BeginUpload(ctx, opts...)
			if _, err := u.Append(ctx, bytes.NewReader(body)); err != nil {
				t.Fatal(err)
			}
			f.failOn(tc.op, tc.after, errInjected)
			if _, err := u.Commit(ctx, blob.Digests{}); !errors.Is(err, errInjected) {
				t.Fatalf("%s after %d: %v", tc.op, tc.after, err)
			}
		})
	}
	t.Run("stored size differs", func(t *testing.T) {
		f := newFake()
		s := newFakeStore(t, f, nil)
		u, _ := s.BeginUpload(ctx)
		if _, err := u.Append(ctx, bytes.NewReader([]byte("abc"))); err != nil {
			t.Fatal(err)
		}
		target := s.blobKey(blobtest.Digest([]byte("abc")).SHA256)
		f.failOnce("Properties", blob.ErrNotFound)
		f.mu.Lock()
		b := f.blob(target)
		b.exists, b.committed, b.etag = true, []byte("abcd"), "x"
		f.mu.Unlock()
		if _, err := u.Commit(ctx, blob.Digests{}); err == nil || !errors.Is(err, ErrPrecondition) {
			t.Fatalf("a different blob under the key: %v", err)
		}
	})
	t.Run("session save fails", func(t *testing.T) {
		st := &flakySessions{Memory: session.NewMemory()}
		f := newFake()
		s := newFakeStore(t, f, st)
		u, _ := s.BeginUpload(ctx)
		if _, err := u.Append(ctx, bytes.NewReader(big)); err != nil {
			t.Fatal(err)
		}
		st.failPut = errInjected
		if _, err := u.Commit(ctx, blob.Digests{}); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
		// Fail the save after the temporary commit instead.
		st.failPut = nil
		st.failPutAfter, st.puts = 1, 0
		u, _ = s.BeginUpload(ctx)
		if _, err := u.Append(ctx, bytes.NewReader(big[:2*testPart])); err != nil {
			t.Fatal(err)
		}
		st.armed = true
		if _, err := u.Commit(ctx, blob.Digests{}); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
	t.Run("forget fails", func(t *testing.T) {
		st := &flakySessions{Memory: session.NewMemory()}
		s := newFakeStore(t, newFake(), st)
		u, _ := s.BeginUpload(ctx)
		st.failDelete = errInjected
		if _, err := u.Commit(ctx, blob.Digests{}); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
	t.Run("cancelled and not live", func(t *testing.T) {
		s := newFakeStore(t, newFake(), nil)
		u, _ := s.BeginUpload(ctx)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := u.Commit(cctx, blob.Digests{}); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if err := u.Abort(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := u.Commit(ctx, blob.Digests{}); !errors.Is(err, blob.ErrUploadUnknown) {
			t.Fatal(err)
		}
	})
}

// flakySessions fails session writes on demand.
type flakySessions struct {
	*session.Memory
	failPut, failDelete error
	armed               bool
	failPutAfter, puts  int
}

func (f *flakySessions) Put(ctx context.Context, id string, state []byte, size int64) error {
	if f.failPut != nil {
		return f.failPut
	}
	if f.armed {
		f.puts++
		if f.puts > f.failPutAfter {
			return errInjected
		}
	}
	return f.Memory.Put(ctx, id, state, size)
}

func (f *flakySessions) Delete(ctx context.Context, id string) error {
	if f.failDelete != nil {
		return f.failDelete
	}
	return f.Memory.Delete(ctx, id)
}

func TestAbort(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	s := newFakeStore(t, f, nil)
	u, _ := s.BeginUpload(ctx)
	if _, err := u.Append(ctx, bytes.NewReader(blobtest.Content(9, 2*testPart))); err != nil {
		t.Fatal(err)
	}
	f.failOn("Delete", 0, errInjected)
	if err := u.Abort(ctx); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
	f.clear("Delete")
	if err := u.Abort(ctx); err != nil {
		t.Fatal(err)
	}
	if f.staged(s.tempKey(u.ID())) != 0 {
		t.Fatal("abort left the temporary blocks")
	}
	// A declared digest stages on the content key, which abort never deletes.
	body := blobtest.Content(10, 2*testPart)
	u, _ = s.BeginUpload(ctx, blob.WithExpectedSHA256(blobtest.Digest(body).SHA256))
	if _, err := u.Append(ctx, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	deletes := f.count("Delete")
	if err := u.Abort(ctx); err != nil || f.count("Delete") != deletes {
		t.Fatalf("declared abort: %v, %d deletes", err, f.count("Delete")-deletes)
	}
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

func TestReadErrors(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	s := newFakeStore(t, f, nil)
	body := blobtest.Content(11, 1000)
	_, info := put(t, s, body)
	sha := info.Digests.SHA256

	f.failOn("Properties", 0, errInjected)
	if _, _, err := s.Open(ctx, sha); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
	f.clear("Properties")
	rc, _, err := s.Open(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}
	f.failOn("Download", 0, errInjected)
	if _, err := rc.Read(make([]byte, 10)); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
	f.clear("Download")
	if _, err := rc.Seek(0, 99); !errors.Is(err, ErrSeek) {
		t.Fatal(err)
	}
	// A body that ends early is an unexpected EOF, not the end of the blob.
	r := rc.(*reader) //nolint:forcetypeassert // the concrete reader under test
	r.body = io.NopCloser(bytes.NewReader([]byte("ab")))
	if _, err := io.ReadAll(r); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
	r.body = errBody{}
	if _, err := r.Read(make([]byte, 4)); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
	r.body = errBody{}
	if err := r.Close(); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	// The blob changing under a pinned reader fails the read.
	r.etag, r.off = "stale", 0
	if _, err := r.Read(make([]byte, 4)); !errors.Is(err, ErrPrecondition) {
		t.Fatal(err)
	}
}

type errBody struct{}

func (errBody) Read([]byte) (int, error) { return 0, errInjected }
func (errBody) Close() error             { return errInjected }

func TestDeleteWalkPresignErrors(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	s := newFakeStore(t, f, nil)
	_, info := put(t, s, []byte("x"))
	sha := info.Digests.SHA256
	f.failOn("Delete", 0, errInjected)
	if err := s.Delete(ctx, sha); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
	f.failOn("List", 0, errInjected)
	if err := s.Walk(ctx, func(blob.Info) error { return nil }); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
	f.clear("List")
	// Names that are not digests are skipped.
	if err := f.Upload(ctx, s.prefix+"sha256/zz/not-a-digest", []byte("y"), false); err != nil {
		t.Fatal(err)
	}
	n := 0
	if err := s.Walk(ctx, func(blob.Info) error { n++; return nil }); err != nil || n != 1 {
		t.Fatalf("walk = %d, %v", n, err)
	}
	if !s.Signs(blob.ViaStorage) || s.Signs(blob.ViaCDN) {
		t.Fatal("Presign signs storage URLs only")
	}
	signed, err := s.SignURL(ctx, sha, blob.SignOptions{
		TTL: time.Minute, Filename: "x.bin", ContentType: "application/x-bin",
	})
	if err != nil || !strings.Contains(signed, sha) {
		t.Fatalf("sign = %q, %v", signed, err)
	}
	u, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	if q := u.Query(); q.Get("rscd") != blob.Disposition("x.bin") ||
		q.Get("rsct") != "application/x-bin" {
		t.Fatalf("overrides %v", q)
	}
	if _, err := s.SignURL(ctx, sha, blob.SignOptions{}); !errors.Is(err, ErrConfig) {
		t.Fatalf("zero ttl: %v", err)
	}
	_, err = s.SignURL(ctx, "bad", blob.SignOptions{TTL: time.Minute})
	if !errors.Is(err, blob.ErrInvalidDigest) {
		t.Fatal(err)
	}
	_, err = s.SignURL(ctx, sha, blob.SignOptions{Via: blob.ViaCDN, TTL: time.Minute})
	if !errors.Is(err, blob.ErrCannotSign) {
		t.Fatal(err)
	}
	f.noSign = true
	_, err = s.SignURL(ctx, sha, blob.SignOptions{TTL: time.Minute})
	if !errors.Is(err, ErrConfig) {
		t.Fatal(err)
	}
	s.presign = false
	_, err = s.SignURL(ctx, sha, blob.SignOptions{TTL: time.Minute})
	if !errors.Is(err, blob.ErrCannotSign) {
		t.Fatal(err)
	}
	if s.ObjectKey(sha) != s.prefix+"sha256/"+sha[:2]+"/"+sha {
		t.Fatalf("object key %q", s.ObjectKey(sha))
	}
}

// TestContainerNotFound keeps a misconfigured container from looking like an
// empty store.
func TestContainerNotFound(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	s := newFakeStore(t, f, nil)
	f.container = true
	sha := blobtest.Digest([]byte("x")).SHA256
	if _, err := s.Stat(ctx, sha); !errors.Is(err, ErrConfig) || errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("stat: %v", err)
	}
	if ok, err := s.Exists(ctx, sha); ok || !errors.Is(err, ErrConfig) {
		t.Fatalf("exists = %v, %v", ok, err)
	}
	if err := s.Delete(ctx, sha); !errors.Is(err, ErrConfig) {
		t.Fatalf("delete: %v", err)
	}
}

func TestFakeModelsAzure(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	id := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	if err := f.StageBlock(ctx, "b", "not base64!", nil); !errors.Is(err, errBadRequest) {
		t.Fatal(err)
	}
	if err := f.StageBlock(
		ctx,
		"b",
		id(strings.Repeat("x", 65)),
		nil,
	); !errors.Is(
		err,
		errBadRequest,
	) {
		t.Fatal(err)
	}
	if err := f.StageBlock(ctx, "b", id("aaaa"), []byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := f.StageBlock(ctx, "b", id("aaaaa"), []byte("2")); !errors.Is(err, errBadRequest) {
		t.Fatalf("different id length: %v", err)
	}
	if _, err := f.Properties(ctx, "b"); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("uncommitted-only blob: %v", err)
	}
	if _, err := f.CommitBlockList(
		ctx,
		"b",
		[]string{id("bbbb")},
		nil,
		false,
	); !errors.Is(
		err,
		ErrBlockList,
	) {
		t.Fatal(err)
	}
	swap(t, &maxUncommitted, 1)
	if err := f.StageBlock(ctx, "b", id("cccc"), nil); err == nil {
		t.Fatal("uncommitted block limit not enforced")
	}
	f.noSource = true
	if err := f.StageBlockFromURL(ctx, "d", id("aaaa"), "b", 0, 1, ""); err == nil {
		t.Fatal("unauthorised copy source accepted")
	}
}
