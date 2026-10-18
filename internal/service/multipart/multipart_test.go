package multipart_test

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // the protocol's own checksum
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/service/multipart"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const mib = 1 << 20

type world struct {
	db    *inmem.Store
	blobs blob.Store
	dir   string
}

func newWorld(t *testing.T) world {
	t.Helper()
	b, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return world{db: inmem.New(), blobs: b, dir: filepath.Join(t.TempDir(), "multipart")}
}

func (w world) service() *multipart.Service {
	return &multipart.Service{Store: w.db, Blobs: w.blobs, Dir: w.dir}
}

// put uploads part through the URL PartURL returns, as the CLI does.
func put(t *testing.T, s *multipart.Service, token string, part int, body []byte) error {
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
	n, _ := strconv.Atoi(q.Get("partNumber"))
	return s.PutPart(
		context.Background(),
		q.Get("upload"),
		n,
		q.Get("signature"),
		bytes.NewReader(body),
	)
}

func content(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}
	return b
}

func sha1Of(b []byte) string {
	sum := sha1.Sum(b) //nolint:gosec // the protocol's own checksum
	return hex.EncodeToString(sum[:])
}

// wait polls Status until it leaves PROCESSING.
func wait(t *testing.T, s *multipart.Service, token string) multipart.Status {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, err := s.Status(context.Background(), token)
		if err != nil {
			t.Fatal(err)
		}
		if st.Status != multipart.StatusProcessing {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("still processing: %+v", st)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestOutOfOrderRoundTrip(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.service()
	token, err := s.Create(ctx, "libs", "/big/file.bin", 1, "admin")
	if err != nil {
		t.Fatal(err)
	}
	data := content(2*mib + 123)
	for _, part := range []int{3, 1, 2} {
		lo := (part - 1) * mib
		if err := put(t, s, token, part, data[lo:min(lo+mib, len(data))]); err != nil {
			t.Fatal(err)
		}
	}
	// A retry of a part already appended changes nothing.
	if err := put(t, s, token, 1, data[:mib]); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, token, sha1Of(data)); err != nil {
		t.Fatal(err)
	}
	st := wait(t, s, token)
	id, _, _ := strings.Cut(token, ".")
	if st.Status != multipart.StatusFinished || !strings.HasPrefix(st.ChecksumToken, id+".") ||
		*st.Progress != 100 {
		t.Fatalf("status %+v", st)
	}
	b, err := w.db.Blobs().Find(ctx, types.Checksums{SHA1: sha1Of(data)})
	if err != nil || b.Refs != 0 || b.Size != int64(len(data)) {
		t.Fatalf("the content is indexed without references: %+v %v", b, err)
	}
	r, _, err := w.blobs.Open(ctx, b.Checksums.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	_, _ = got.ReadFrom(r)
	_ = r.Close()
	if !bytes.Equal(got.Bytes(), data) {
		t.Fatal("the stored content differs")
	}
	if _, err := os.Stat(filepath.Join(w.dir, id)); !os.IsNotExist(err) {
		t.Fatalf("the staged parts are removed: %v", err)
	}
	// Completing again, or polling again, is harmless.
	if err := s.Complete(ctx, token, sha1Of(data)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PartURL(ctx, token, 4); !errors.Is(err, multipart.ErrState) {
		t.Fatalf("no parts after completion: %v", err)
	}
}

func TestAuthentication(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.service()
	token, err := s.Create(ctx, "libs", "f", 1, "admin")
	if err != nil {
		t.Fatal(err)
	}
	id, _, _ := strings.Cut(token, ".")
	for _, bad := range []string{"", "nodot", ".secret", id + ".", id + ".wrong", "missing.secret"} {
		if _, err := s.Status(ctx, bad); !errors.Is(err, multipart.ErrBadToken) {
			t.Errorf("token %q: %v", bad, err)
		}
	}
	ref, err := s.PartURL(ctx, token, 1)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(ref)
	sig := u.Query().Get("signature")
	for name, call := range map[string]func() error{
		"wrong signature": func() error { return s.PutPart(ctx, id, 1, strings.Repeat("0", 64), strings.NewReader("x")) },
		"another part":    func() error { return s.PutPart(ctx, id, 2, sig, strings.NewReader("x")) },
		"part zero":       func() error { return s.PutPart(ctx, id, 0, sig, strings.NewReader("x")) },
		"unknown upload":  func() error { return s.PutPart(ctx, "nope", 1, sig, strings.NewReader("x")) },
	} {
		if err := call(); !errors.Is(err, multipart.ErrBadToken) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A row of another backend is not a multipart session.
	if err := w.db.Uploads().Put(ctx, store.Upload{ID: "other", Backend: "fs"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Status(ctx, "other.secret"); !errors.Is(err, multipart.ErrBadToken) {
		t.Fatalf("other backend: %v", err)
	}
}

// TestFinished: the checksum token the status poll hands out proves a
// finished upload of one content by one caller, and nothing else.
func TestFinished(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.service()
	token, err := s.Create(ctx, "libs", "f.bin", 1, "alice")
	if err != nil {
		t.Fatal(err)
	}
	data := content(mib + 5)
	if err := put(t, s, token, 1, data[:mib]); err != nil {
		t.Fatal(err)
	}
	id, _, _ := strings.Cut(token, ".")
	finished := func(tok, by, sum string) bool {
		t.Helper()
		ok, err := s.Finished(ctx, tok, by, sum)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	// Before completion there is no checksum token to present; the upload
	// id, which the part URLs carry, is not one.
	if finished(id, "alice", "") || finished(id+".x", "alice", "") {
		t.Fatal("an unfinished upload passes")
	}
	if err := put(t, s, token, 2, data[mib:]); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, token, sha1Of(data)); err != nil {
		t.Fatal(err)
	}
	st := wait(t, s, token)
	b, err := w.db.Blobs().Find(ctx, types.Checksums{SHA1: sha1Of(data)})
	if err != nil {
		t.Fatal(err)
	}
	sum := b.Checksums.SHA256
	if !finished(st.ChecksumToken, "alice", sum) {
		t.Fatal("the uploader's checksum token does not pass")
	}
	for name, tc := range map[string]struct{ token, by, sum string }{
		"another caller":    {st.ChecksumToken, "mallory", sum},
		"other content":     {st.ChecksumToken, "alice", strings.Repeat("0", 64)},
		"the upload id":     {id, "alice", sum},
		"the upload token":  {token, "alice", sum},
		"a forged token":    {id + "." + strings.Repeat("0", 64), "alice", sum},
		"no upload id":      {"." + strings.Repeat("0", 64), "alice", sum},
		"an unknown upload": {"missing.x", "alice", sum},
	} {
		if finished(tc.token, tc.by, tc.sum) {
			t.Errorf("%s passes", name)
		}
	}
	// Once the session is gone, so is its token.
	if err := s.Abort(ctx, token); err != nil {
		t.Fatal(err)
	}
	if finished(st.ChecksumToken, "alice", sum) {
		t.Fatal("an aborted upload's checksum token passes")
	}
}

func TestInvalidRequests(t *testing.T) {
	ctx := context.Background()
	s := newWorld(t).service()
	for name, call := range map[string]func() error{
		"no repo":        func() error { _, err := s.Create(ctx, "", "f", 1, "a"); return err },
		"no path":        func() error { _, err := s.Create(ctx, "libs", "/", 1, "a"); return err },
		"zero part size": func() error { _, err := s.Create(ctx, "libs", "f", 0, "a"); return err },
		"huge part size": func() error { _, err := s.Create(ctx, "libs", "f", 6000, "a"); return err },
	} {
		if err := call(); !errors.Is(err, multipart.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	token, err := s.Create(ctx, "libs", "f", 1, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PartURL(ctx, token, 0); !errors.Is(err, multipart.ErrInvalid) {
		t.Fatalf("part zero: %v", err)
	}
	if err := put(t, s, token, 1, content(mib+1)); !errors.Is(err, multipart.ErrInvalid) {
		t.Fatalf("a part larger than the part size: %v", err)
	}
	if err := put(t, s, token, 1, nil); !errors.Is(err, multipart.ErrInvalid) {
		t.Fatalf("an empty part: %v", err)
	}
	if err := s.Complete(ctx, token, "not-a-sha1"); !errors.Is(err, multipart.ErrInvalid) {
		t.Fatalf("a malformed sha1: %v", err)
	}
}

func TestCompletionFailures(t *testing.T) {
	ctx := context.Background()
	data := content(3 * mib)
	for name, tc := range map[string]struct {
		parts map[int][]byte
		sha1  string
		want  string
	}{
		"no parts":     {sha1: sha1Of(data), want: "no parts"},
		"missing part": {parts: map[int][]byte{1: data[:mib], 3: data[2*mib:]}, sha1: sha1Of(data), want: "part 2 of 3 is missing"},
		"wrong sha1":   {parts: map[int][]byte{1: data[:mib]}, sha1: sha1Of([]byte("other")), want: "do not match the SHA-1"},
	} {
		s := newWorld(t).service()
		token, err := s.Create(ctx, "libs", "f", 1, "admin")
		if err != nil {
			t.Fatal(err)
		}
		for n, b := range tc.parts {
			if err := put(t, s, token, n, b); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Complete(ctx, token, tc.sha1); err != nil {
			t.Fatal(err)
		}
		st := wait(t, s, token)
		if st.Status != multipart.StatusFailed || !strings.Contains(st.Error, tc.want) {
			t.Errorf("%s: %+v", name, st)
		}
		if err := s.Complete(ctx, token, tc.sha1); !errors.Is(err, multipart.ErrState) {
			t.Errorf("%s: a failed upload cannot complete again: %v", name, err)
		}
	}
}

func TestShortPartMustBeLast(t *testing.T) {
	ctx := context.Background()
	s := newWorld(t).service()
	token, err := s.Create(ctx, "libs", "f", 1, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := put(t, s, token, 2, content(mib)); err != nil {
		t.Fatal(err)
	}
	if err := put(t, s, token, 1, content(10)); err != nil {
		t.Fatal(err)
	}
	st, err := s.Status(ctx, token)
	if err != nil || st.Status != multipart.StatusFailed || !strings.Contains(st.Error, "part 1") {
		t.Fatalf("status %+v %v", st, err)
	}
	if _, err := s.PartURL(ctx, token, 3); !errors.Is(err, multipart.ErrState) {
		t.Fatalf("no part URLs after a failure: %v", err)
	}
}

func TestResumesAfterRestart(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	data := content(2*mib + 5)
	first := w.service()
	token, err := first.Create(ctx, "libs", "f", 1, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := put(t, first, token, 1, data[:mib]); err != nil {
		t.Fatal(err)
	}
	if err := put(t, first, token, 3, data[2*mib:]); err != nil {
		t.Fatal(err)
	}
	// A new process over the same store, blob store and staging directory.
	second := w.service()
	if err := put(t, second, token, 2, data[mib:2*mib]); err != nil {
		t.Fatal(err)
	}
	// A completion interrupted by a restart: PROCESSING with no worker.
	id, _, _ := strings.Cut(token, ".")
	u, err := w.db.Uploads().Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var ss map[string]any
	_ = json.Unmarshal(u.State, &ss)
	ss["status"] = multipart.StatusProcessing
	u.State, _ = json.Marshal(ss)
	if err := w.db.Uploads().Put(ctx, u); err != nil {
		t.Fatal(err)
	}
	third := w.service()
	if st, err := third.Status(ctx, token); err != nil || st.Status != multipart.StatusRetryable {
		t.Fatalf("an interrupted completion asks for another: %+v %v", st, err)
	}
	if err := third.Complete(ctx, token, sha1Of(data)); err != nil {
		t.Fatal(err)
	}
	if st := wait(t, third, token); st.Status != multipart.StatusFinished {
		t.Fatalf("status %+v", st)
	}
	if _, err := w.db.Blobs().Find(ctx, types.Checksums{SHA1: sha1Of(data)}); err != nil {
		t.Fatal(err)
	}
}

func TestAbortReapAndSweep(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.service()
	token, err := s.Create(ctx, "libs", "f", 1, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := put(t, s, token, 2, content(mib)); err != nil {
		t.Fatal(err)
	}
	if err := put(t, s, token, 1, content(mib)); err != nil {
		t.Fatal(err)
	}
	id, _, _ := strings.Cut(token, ".")
	u, err := w.db.Uploads().Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var ss struct{ Blob string }
	_ = json.Unmarshal(u.State, &ss)
	if err := s.Abort(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := w.blobs.ResumeUpload(ctx, ss.Blob); !errors.Is(err, blob.ErrUploadUnknown) {
		t.Fatalf("the blob upload is aborted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.dir, id)); !os.IsNotExist(err) {
		t.Fatalf("the staged parts are removed: %v", err)
	}
	if _, err := s.Status(ctx, token); !errors.Is(err, multipart.ErrBadToken) {
		t.Fatalf("the session is gone: %v", err)
	}

	// The collector's reaper does the same for a stale session.
	token, err = s.Create(ctx, "libs", "g", 1, "admin")
	if err != nil {
		t.Fatal(err)
	}
	id, _, _ = strings.Cut(token, ".")
	if u, err = w.db.Uploads().Get(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.Reap(ctx, u); err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Uploads().Get(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("reaped: %v", err)
	}

	// Sweep removes a staging directory whose session is gone.
	keep, err := s.Create(ctx, "libs", "h", 1, "admin")
	if err != nil {
		t.Fatal(err)
	}
	keepID, _, _ := strings.Cut(keep, ".")
	if err := os.MkdirAll(filepath.Join(w.dir, "orphan"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.dir, "orphan")); !os.IsNotExist(err) {
		t.Fatalf("orphan staging is swept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.dir, keepID)); err != nil {
		t.Fatalf("a live session's staging is kept: %v", err)
	}
	empty := &multipart.Service{
		Store: w.db,
		Blobs: w.blobs,
		Dir:   filepath.Join(t.TempDir(), "none"),
	}
	if err := empty.Sweep(ctx); err != nil {
		t.Fatalf("no staging directory yet: %v", err)
	}
}

func TestStatusBeforeCompletion(t *testing.T) {
	ctx := context.Background()
	s := newWorld(t).service()
	token, err := s.Create(ctx, "libs", "f", 1, "admin")
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.Status(ctx, token)
	if err != nil || st.Status != multipart.StatusParts || st.Progress != nil {
		t.Fatalf("status %+v %v", st, err)
	}
}
