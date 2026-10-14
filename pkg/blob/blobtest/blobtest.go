// Package blobtest is the contract every blob.Store backend must satisfy.
// Backends call Run from their own tests; the s3 backend runs it twice, against
// an in-memory fake everywhere and against MinIO where Docker is available.
package blobtest

import (
	"bytes"
	"context"
	"crypto/md5"  //nolint:gosec // wire checksum under test
	"crypto/sha1" //nolint:gosec // wire checksum under test
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob"
)

// Options tunes the suite for a backend.
type Options struct {
	// LargeSize is the size of the blob used to exercise multi-part paths.
	// Backends that buffer into parts set it above their part size. Zero
	// means 3 MiB.
	LargeSize int
	// FetchPresigned makes the suite GET a signed URL over HTTP and compare
	// the body and the response overrides. Only meaningful against a real
	// object store.
	FetchPresigned bool
}

// Run executes the contract. newStore must return an empty store each call.
func Run(t *testing.T, newStore func(t *testing.T) blob.Store, o Options) {
	t.Helper()
	if o.LargeSize == 0 {
		o.LargeSize = 3 << 20
	}
	ctx := context.Background()
	tests := map[string]func(t *testing.T, s blob.Store){
		"round trip":            func(t *testing.T, s blob.Store) { roundTrip(ctx, t, s) },
		"empty blob":            func(t *testing.T, s blob.Store) { emptyBlob(ctx, t, s) },
		"large blob":            func(t *testing.T, s blob.Store) { largeBlob(ctx, t, s, o.LargeSize) },
		"digest mismatch":       func(t *testing.T, s blob.Store) { digestMismatch(ctx, t, s) },
		"invalid digests":       func(t *testing.T, s blob.Store) { invalidDigests(ctx, t, s) },
		"expected sha256":       func(t *testing.T, s blob.Store) { expectedSHA256(ctx, t, s) },
		"idempotent commit":     func(t *testing.T, s blob.Store) { idempotent(ctx, t, s) },
		"resume":                func(t *testing.T, s blob.Store) { resume(ctx, t, s, o.LargeSize) },
		"abort":                 func(t *testing.T, s blob.Store) { abort(ctx, t, s) },
		"ranged reads":          func(t *testing.T, s blob.Store) { ranged(ctx, t, s) },
		"concurrent same bytes": func(t *testing.T, s blob.Store) { concurrent(ctx, t, s) },
		"walk":                  func(t *testing.T, s blob.Store) { walk(ctx, t, s) },
		"delete and not found":  func(t *testing.T, s blob.Store) { deleteNotFound(ctx, t, s) },
		"cancelled context":     func(t *testing.T, s blob.Store) { cancelled(t, s) },
		"sign":                  func(t *testing.T, s blob.Store) { sign(ctx, t, s, o) },
	}
	names := make([]string, 0, len(tests))
	for name := range tests {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) { tests[name](t, newStore(t)) })
	}
}

// Digest returns all three digests of b, the oracle the suite checks against.
func Digest(b []byte) blob.Digests {
	s256 := sha256.Sum256(b)
	s1 := sha1.Sum(b) //nolint:gosec // wire checksum
	m5 := md5.Sum(b)  //nolint:gosec // wire checksum
	return blob.Digests{
		SHA256: hex.EncodeToString(s256[:]),
		SHA1:   hex.EncodeToString(s1[:]),
		MD5:    hex.EncodeToString(m5[:]),
	}
}

// Content returns n deterministic, non-repeating-looking bytes for seed.
func Content(seed byte, n int) []byte {
	b := make([]byte, n)
	x := uint32(seed) + 1
	for i := range b {
		x = x*1664525 + 1013904223
		b[i] = byte(x >> 24)
	}
	return b
}

// Put stores b in one append and returns the committed Info.
func Put(ctx context.Context, t *testing.T, s blob.Store, b []byte) blob.Info {
	t.Helper()
	u, err := s.BeginUpload(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := u.Append(ctx, bytes.NewReader(b)); err != nil {
		t.Fatalf("append: %v", err)
	}
	info, err := u.Commit(ctx, blob.Digests{})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	return info
}

func readAll(ctx context.Context, t *testing.T, s blob.Store, sha string) []byte {
	t.Helper()
	rc, info, err := s.Open(ctx, sha)
	if err != nil {
		t.Fatalf("open %s: %v", sha, err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %s: %v", sha, err)
	}
	if info.Size != int64(len(b)) || info.Digests.SHA256 != sha {
		t.Fatalf("open info %+v for %d bytes of %s", info, len(b), sha)
	}
	return b
}

func roundTrip(ctx context.Context, t *testing.T, s blob.Store) {
	body := Content(1, 70_000)
	u, err := s.BeginUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !blob.ValidUploadID(u.ID()) {
		t.Fatalf("upload id %q", u.ID())
	}
	n1, err := u.Append(ctx, bytes.NewReader(body[:30_000]))
	if err != nil || n1 != 30_000 {
		t.Fatalf("append 1: %d %v", n1, err)
	}
	n2, err := u.Append(ctx, bytes.NewReader(body[30_000:]))
	if err != nil || n2 != 40_000 || u.Size() != 70_000 {
		t.Fatalf("append 2: %d %v size %d", n2, err, u.Size())
	}
	want := Digest(body)
	info, err := u.Commit(ctx, want)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if info.Size != 70_000 || info.Digests != want {
		t.Fatalf("commit info %+v, want %+v", info, want)
	}
	if got := readAll(ctx, t, s, want.SHA256); !bytes.Equal(got, body) {
		t.Fatal("content differs after round trip")
	}
	st, err := s.Stat(ctx, want.SHA256)
	if err != nil || st.Size != 70_000 || st.Digests.SHA256 != want.SHA256 {
		t.Fatalf("stat %+v %v", st, err)
	}
	if ok, err := s.Exists(ctx, want.SHA256); err != nil || !ok {
		t.Fatalf("exists %v %v", ok, err)
	}
	if _, err := s.ResumeUpload(ctx, u.ID()); !errors.Is(err, blob.ErrUploadUnknown) {
		t.Fatalf("resume after commit: %v", err)
	}
	if _, err := u.Append(
		ctx,
		bytes.NewReader([]byte("x")),
	); !errors.Is(
		err,
		blob.ErrUploadUnknown,
	) {
		t.Fatalf("append after commit: %v", err)
	}
	if _, err := u.Commit(ctx, blob.Digests{}); !errors.Is(err, blob.ErrUploadUnknown) {
		t.Fatalf("commit after commit: %v", err)
	}
}

func emptyBlob(ctx context.Context, t *testing.T, s blob.Store) {
	u, err := s.BeginUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	info, err := u.Commit(ctx, Digest(nil))
	if err != nil || info.Size != 0 || info.Digests != Digest(nil) {
		t.Fatalf("empty commit %+v %v", info, err)
	}
	if got := readAll(ctx, t, s, info.Digests.SHA256); len(got) != 0 {
		t.Fatalf("empty blob read %d bytes", len(got))
	}
}

func largeBlob(ctx context.Context, t *testing.T, s blob.Store, size int) {
	body := Content(2, size)
	u, err := s.BeginUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Uneven chunks so part boundaries never line up with appends.
	for off := 0; off < len(body); {
		end := min(off+700_001, len(body))
		if _, err := u.Append(ctx, bytes.NewReader(body[off:end])); err != nil {
			t.Fatalf("append at %d: %v", off, err)
		}
		off = end
	}
	info, err := u.Commit(ctx, Digest(body))
	if err != nil || info.Size != int64(size) {
		t.Fatalf("commit %+v %v", info, err)
	}
	if got := readAll(ctx, t, s, info.Digests.SHA256); !bytes.Equal(got, body) {
		t.Fatal("large blob differs")
	}
}

func digestMismatch(ctx context.Context, t *testing.T, s blob.Store) {
	body := Content(3, 1000)
	right := Digest(body)
	wrong := Digest([]byte("other"))
	for _, want := range []blob.Digests{
		{SHA256: wrong.SHA256},
		{SHA1: wrong.SHA1},
		{MD5: wrong.MD5},
		{SHA256: right.SHA256, SHA1: wrong.SHA1},
	} {
		u, err := s.BeginUpload(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := u.Append(ctx, bytes.NewReader(body)); err != nil {
			t.Fatal(err)
		}
		if _, err := u.Commit(ctx, want); !errors.Is(err, blob.ErrDigestMismatch) {
			t.Fatalf("want %+v: %v", want, err)
		}
		if _, err := s.ResumeUpload(ctx, u.ID()); !errors.Is(err, blob.ErrUploadUnknown) {
			t.Fatalf("mismatch must abort the session: %v", err)
		}
	}
	if ok, _ := s.Exists(ctx, right.SHA256); ok {
		t.Fatal("mismatched content was committed")
	}
}

func invalidDigests(ctx context.Context, t *testing.T, s blob.Store) {
	for _, bad := range []string{"", "../../etc/passwd", "ABCDEF", Digest(nil).SHA256 + "0"} {
		if _, err := s.Stat(ctx, bad); !errors.Is(err, blob.ErrInvalidDigest) {
			t.Errorf("stat %q: %v", bad, err)
		}
		if _, _, err := s.Open(ctx, bad); !errors.Is(err, blob.ErrInvalidDigest) {
			t.Errorf("open %q: %v", bad, err)
		}
		if err := s.Delete(ctx, bad); !errors.Is(err, blob.ErrInvalidDigest) {
			t.Errorf("delete %q: %v", bad, err)
		}
		if _, err := s.Exists(ctx, bad); !errors.Is(err, blob.ErrInvalidDigest) {
			t.Errorf("exists %q: %v", bad, err)
		}
	}
	u, err := s.BeginUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []blob.Digests{{SHA256: "zz"}, {SHA1: "zz"}, {MD5: "zz"}} {
		if _, err := u.Commit(ctx, want); !errors.Is(err, blob.ErrInvalidDigest) {
			t.Errorf("commit %+v: %v", want, err)
		}
	}
	// An invalid declaration is a client error, not a failed upload: the
	// session survives and can still commit.
	if _, err := u.Commit(ctx, blob.Digests{}); err != nil {
		t.Fatalf("commit after invalid declaration: %v", err)
	}
	if _, err := s.BeginUpload(
		ctx,
		blob.WithExpectedSHA256("nope"),
	); !errors.Is(
		err,
		blob.ErrInvalidDigest,
	) {
		t.Fatalf("begin with invalid expected digest: %v", err)
	}
	for _, id := range []string{"", "../x", "0123", "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ"} {
		if _, err := s.ResumeUpload(ctx, id); !errors.Is(err, blob.ErrUploadUnknown) {
			t.Errorf("resume %q: %v", id, err)
		}
	}
	if _, err := s.ResumeUpload(
		ctx,
		"0123456789abcdef0123456789abcdef",
	); !errors.Is(
		err,
		blob.ErrUploadUnknown,
	) {
		t.Errorf("resume never-issued id: %v", err)
	}
}

func expectedSHA256(ctx context.Context, t *testing.T, s blob.Store) {
	body := Content(4, 5000)
	d := Digest(body)
	u, err := s.BeginUpload(ctx, blob.WithExpectedSHA256(d.SHA256))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Append(ctx, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	// The declaration must survive a resume, as a Docker monolithic upload
	// may be split across requests by a proxy.
	r, err := s.ResumeUpload(ctx, u.ID())
	if err != nil {
		t.Fatal(err)
	}
	info, err := r.Commit(ctx, blob.Digests{})
	if err != nil || info.Digests.SHA256 != d.SHA256 {
		t.Fatalf("commit %+v %v", info, err)
	}
	u, err = s.BeginUpload(ctx, blob.WithExpectedSHA256(Digest([]byte("x")).SHA256))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Append(ctx, bytes.NewReader(Content(5, 100))); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Commit(ctx, blob.Digests{}); !errors.Is(err, blob.ErrDigestMismatch) {
		t.Fatalf("wrong expected digest: %v", err)
	}
}

func idempotent(ctx context.Context, t *testing.T, s blob.Store) {
	body := Content(6, 2048)
	a := Put(ctx, t, s, body)
	b := Put(ctx, t, s, body)
	if a.Digests.SHA256 != b.Digests.SHA256 || a.Size != b.Size {
		t.Fatalf("two commits differ: %+v %+v", a, b)
	}
	count := 0
	if err := s.Walk(ctx, func(blob.Info) error { count++; return nil }); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("walk saw %d blobs, want 1", count)
	}
}

func resume(ctx context.Context, t *testing.T, s blob.Store, size int) {
	body := Content(7, size)
	half := len(body) / 2
	u, err := s.BeginUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Append(ctx, bytes.NewReader(body[:half])); err != nil {
		t.Fatal(err)
	}
	r, err := s.ResumeUpload(ctx, u.ID())
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if r.ID() != u.ID() || r.Size() != int64(half) {
		t.Fatalf("resumed %s at %d, want %s at %d", r.ID(), r.Size(), u.ID(), half)
	}
	if _, err := r.Append(ctx, bytes.NewReader(body[half:])); err != nil {
		t.Fatal(err)
	}
	info, err := r.Commit(ctx, Digest(body))
	if err != nil {
		t.Fatalf("commit after resume: %v", err)
	}
	if got := readAll(ctx, t, s, info.Digests.SHA256); !bytes.Equal(got, body) {
		t.Fatal("resumed content differs")
	}
}

func abort(ctx context.Context, t *testing.T, s blob.Store) {
	u, err := s.BeginUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Append(ctx, bytes.NewReader(Content(8, 100))); err != nil {
		t.Fatal(err)
	}
	if err := u.Abort(ctx); err != nil {
		t.Fatal(err)
	}
	if err := u.Abort(ctx); err != nil {
		t.Fatalf("second abort: %v", err)
	}
	if _, err := s.ResumeUpload(ctx, u.ID()); !errors.Is(err, blob.ErrUploadUnknown) {
		t.Fatalf("resume after abort: %v", err)
	}
	if _, err := u.Append(
		ctx,
		bytes.NewReader([]byte("x")),
	); !errors.Is(
		err,
		blob.ErrUploadUnknown,
	) {
		t.Fatalf("append after abort: %v", err)
	}
	count := 0
	_ = s.Walk(ctx, func(blob.Info) error { count++; return nil })
	if count != 0 {
		t.Fatalf("aborted upload left %d blobs", count)
	}
}

func ranged(ctx context.Context, t *testing.T, s blob.Store) {
	body := Content(9, 10_000)
	info := Put(ctx, t, s, body)
	rc, _, err := s.Open(ctx, info.Digests.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	read := func(n int) []byte {
		t.Helper()
		b := make([]byte, n)
		if _, err := io.ReadFull(rc, b); err != nil {
			t.Fatalf("read %d: %v", n, err)
		}
		return b
	}
	steps := []struct {
		off    int64
		whence int
		pos    int64
	}{
		{1234, io.SeekStart, 1234},
		{-100, io.SeekEnd, 9900},
		{-50, io.SeekCurrent, 9870},
	}
	for _, st := range steps {
		pos, err := rc.Seek(st.off, st.whence)
		if err != nil || pos != st.pos {
			t.Fatalf("seek %d/%d: %d %v", st.off, st.whence, pos, err)
		}
		if got := read(20); !bytes.Equal(got, body[pos:pos+20]) {
			t.Fatalf("bytes at %d differ", pos)
		}
	}
	if _, err := rc.Seek(-1, io.SeekStart); err == nil {
		t.Fatal("seek before start must fail")
	}
	if _, err := rc.Seek(0, io.SeekEnd); err != nil {
		t.Fatal(err)
	}
	if n, err := rc.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("read at end: %d %v", n, err)
	}
}

func concurrent(ctx context.Context, t *testing.T, s blob.Store) {
	body := Content(10, 50_000)
	const writers = 6
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u, err := s.BeginUpload(ctx)
			if err != nil {
				errs <- err
				return
			}
			if _, err := u.Append(ctx, bytes.NewReader(body)); err != nil {
				errs <- err
				return
			}
			_, err = u.Commit(ctx, Digest(body))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent commit: %v", err)
		}
	}
	if got := readAll(ctx, t, s, Digest(body).SHA256); !bytes.Equal(got, body) {
		t.Fatal("concurrent commits corrupted the blob")
	}
}

func walk(ctx context.Context, t *testing.T, s blob.Store) {
	want := map[string]int64{}
	for i := range 3 {
		b := Content(byte(20+i), 100*(i+1))
		want[Put(ctx, t, s, b).Digests.SHA256] = int64(len(b))
	}
	// An open session must not show up as a blob.
	u, err := s.BeginUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Append(ctx, bytes.NewReader(Content(30, 10))); err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	if err := s.Walk(
		ctx,
		func(i blob.Info) error { got[i.Digests.SHA256] = i.Size; return nil },
	); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("walk saw %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("walk size for %s: %d, want %d", k, got[k], v)
		}
	}
	stop := errors.New("stop")
	calls := 0
	err = s.Walk(ctx, func(blob.Info) error { calls++; return stop })
	if !errors.Is(err, stop) || calls != 1 {
		t.Fatalf("walk must stop at the first error: %v after %d calls", err, calls)
	}
}

func deleteNotFound(ctx context.Context, t *testing.T, s blob.Store) {
	info := Put(ctx, t, s, Content(40, 64))
	sha := info.Digests.SHA256
	if err := s.Delete(ctx, sha); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, sha); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	if ok, err := s.Exists(ctx, sha); err != nil || ok {
		t.Fatalf("exists after delete: %v %v", ok, err)
	}
	if _, err := s.Stat(ctx, sha); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("stat: %v", err)
	}
	if _, _, err := s.Open(ctx, sha); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("open: %v", err)
	}
}

func cancelled(t *testing.T, s blob.Store) {
	ctx, cancel := context.WithCancel(context.Background())
	live, err := s.BeginUpload(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	sha := Digest(nil).SHA256
	checks := map[string]error{}
	_, checks["begin"] = s.BeginUpload(ctx)
	_, checks["resume"] = s.ResumeUpload(ctx, live.ID())
	_, checks["stat"] = s.Stat(ctx, sha)
	_, _, checks["open"] = s.Open(ctx, sha)
	checks["delete"] = s.Delete(ctx, sha)
	checks["walk"] = s.Walk(ctx, func(blob.Info) error { return nil })
	_, checks["append"] = live.Append(ctx, bytes.NewReader([]byte("x")))
	_, checks["commit"] = live.Commit(ctx, blob.Digests{})
	for op, err := range checks {
		if op == "walk" && err == nil {
			continue // an empty store has nothing to visit; returning early is fine
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("%s with cancelled context: %v", op, err)
		}
	}
}

func sign(ctx context.Context, t *testing.T, s blob.Store, o Options) {
	sg, ok := s.(blob.Signer)
	if !ok || !sg.Signs(blob.ViaStorage) {
		t.Skip("backend does not sign storage URLs")
	}
	body := Content(50, 3000)
	info := Put(ctx, t, s, body)
	opts := blob.SignOptions{
		TTL: time.Minute, Filename: "lib 1.0.tgz", ContentType: "application/x-test",
	}
	if _, err := sg.SignURL(ctx, "bad", opts); !errors.Is(err, blob.ErrInvalidDigest) {
		t.Fatalf("sign invalid digest: %v", err)
	}
	if !sg.Signs(blob.ViaCDN) {
		cdn := opts
		cdn.Via = blob.ViaCDN
		_, err := sg.SignURL(ctx, info.Digests.SHA256, cdn)
		if !errors.Is(err, blob.ErrCannotSign) {
			t.Fatalf("sign for a CDN the store lacks: %v", err)
		}
	}
	url, err := sg.SignURL(ctx, info.Digests.SHA256, opts)
	if err != nil || url == "" {
		t.Fatalf("sign: %q %v", url, err)
	}
	if !o.FetchPresigned {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	got, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK || !bytes.Equal(got, body) {
		t.Fatalf("signed GET: %d %v (%d bytes)", resp.StatusCode, err, len(got))
	}
	if cd, ct := resp.Header.Get(
		"Content-Disposition",
	), resp.Header.Get(
		"Content-Type",
	); cd != blob.Disposition(opts.Filename) ||
		ct != opts.ContentType {
		t.Fatalf("signed GET overrides: Content-Disposition %q, Content-Type %q", cd, ct)
	}
}
