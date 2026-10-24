package session_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

var errInjected = errors.New("injected")

// parts is a backend document: the parts a fake backend received.
type parts struct {
	Sizes []int `json:"sizes,omitempty"`
}

// collect is a FlushFunc that records part sizes in the session's Data.
func collect(ss *session.Session[parts]) session.FlushFunc {
	return func(_ context.Context, part []byte) error {
		ss.Data.Sizes = append(ss.Data.Sizes, len(part))
		return nil
	}
}

func begin(t *testing.T, st session.Store, opts ...blob.UploadOption) *session.Session[parts] {
	t.Helper()
	ss, err := session.Begin[parts](context.Background(), st, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return ss
}

func TestUnitAppendFlushesFullPartsAndResumes(t *testing.T) {
	ctx := context.Background()
	st := session.NewMemory()
	content := bytes.Repeat([]byte("abcdefgh"), 5) // 40 bytes
	ss := begin(t, st)
	if n, err := ss.Append(
		ctx,
		bytes.NewReader(content[:25]),
		10,
		collect(ss),
	); err != nil ||
		n != 25 {
		t.Fatalf("Append = %d, %v", n, err)
	}
	if len(ss.Data.Sizes) != 2 || len(ss.Pending) != 5 || ss.Size != 25 {
		t.Fatalf(
			"after 25 bytes: parts %v pending %d size %d",
			ss.Data.Sizes,
			len(ss.Pending),
			ss.Size,
		)
	}
	again, err := session.Resume[parts](ctx, st, ss.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Size != 25 || len(again.Pending) != 5 || len(again.Data.Sizes) != 2 {
		t.Fatalf("resumed: %+v", again)
	}
	if _, err := again.Append(ctx, bytes.NewReader(content[25:]), 10, collect(again)); err != nil {
		t.Fatal(err)
	}
	if err := again.Flush(ctx, collect(again)); err != nil {
		t.Fatal(err)
	}
	if err := again.Flush(
		ctx,
		func(context.Context, []byte) error { return errInjected },
	); err != nil {
		t.Fatal("Flush with nothing pending must not call flush")
	}
	got, err := again.Verify(blob.Digests{})
	if err != nil || got != blobDigests(content) {
		t.Fatalf("Verify = %+v, %v", got, err)
	}
	if err := again.Forget(ctx); err != nil {
		t.Fatal(err)
	}
	if err := again.Live(ctx); !errors.Is(err, blob.ErrUploadUnknown) {
		t.Fatalf("Live after Forget: %v", err)
	}
	if _, err := again.Append(
		ctx,
		bytes.NewReader(nil),
		10,
		collect(again),
	); !errors.Is(
		err,
		blob.ErrUploadUnknown,
	) {
		t.Fatalf("Append after Forget: %v", err)
	}
}

func blobDigests(b []byte) blob.Digests {
	h := blob.NewHasher()
	_, _ = h.Write(b)
	return h.Sum()
}

func TestUnitVerify(t *testing.T) {
	content := []byte("verify me")
	want := blobDigests(content)
	ss := begin(t, session.NewMemory(), blob.WithExpectedSHA256(want.SHA256))
	if _, err := ss.Append(
		context.Background(),
		bytes.NewReader(content),
		4,
		collect(ss),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := ss.Verify(blob.Digests{MD5: want.MD5}); err != nil {
		t.Fatalf("declared sha256 and matching md5: %v", err)
	}
	other := begin(t, session.NewMemory(), blob.WithExpectedSHA256(blobDigests([]byte("x")).SHA256))
	if _, err := other.Verify(blob.Digests{}); !errors.Is(err, blob.ErrDigestMismatch) {
		t.Fatalf("declared digest of other content: %v", err)
	}
}

func TestUnitAppendFailures(t *testing.T) {
	ctx := context.Background()
	t.Run("flush fails keeps the bytes pending", func(t *testing.T) {
		ss := begin(t, session.NewMemory())
		n, err := ss.Append(ctx, bytes.NewReader([]byte("0123456789ab")), 4,
			func(context.Context, []byte) error { return errInjected })
		if !errors.Is(err, errInjected) || n != 4 || ss.Size != 4 || len(ss.Pending) != 4 {
			t.Fatalf("Append = %d, %v; size %d pending %d", n, err, ss.Size, len(ss.Pending))
		}
	})
	t.Run("read fails", func(t *testing.T) {
		ss := begin(t, session.NewMemory())
		r := io.MultiReader(bytes.NewReader([]byte("ab")), errReader{})
		if n, err := ss.Append(ctx, r, 4, collect(ss)); !errors.Is(err, errInjected) || n != 2 {
			t.Fatalf("Append = %d, %v", n, err)
		}
	})
	t.Run("save fails", func(t *testing.T) {
		st := &flaky{Memory: session.NewMemory()}
		ss := begin(t, st)
		st.failPut = errInjected
		if _, err := ss.Append(
			ctx,
			bytes.NewReader([]byte("ab")),
			4,
			collect(ss),
		); !errors.Is(
			err,
			errInjected,
		) {
			t.Fatal(err)
		}
		r := io.MultiReader(bytes.NewReader([]byte("ab")), errReader{})
		if _, err := ss.Append(ctx, r, 4, collect(ss)); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
		if _, err := session.Begin[parts](ctx, st); !errors.Is(err, errInjected) {
			t.Fatalf("Begin with a failing store: %v", err)
		}
	})
	t.Run("forget fails", func(t *testing.T) {
		st := &flaky{Memory: session.NewMemory()}
		ss := begin(t, st)
		st.failDelete = errInjected
		if err := ss.Forget(ctx); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
	t.Run("data cannot be encoded", func(t *testing.T) {
		ss, err := session.Begin[chan int](ctx, session.NewMemory())
		if err == nil || ss != nil {
			t.Fatalf("Begin with unencodable data = %v, %v", ss, err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		ss := begin(t, session.NewMemory())
		if _, err := ss.Append(
			cctx,
			bytes.NewReader(nil),
			4,
			collect(ss),
		); !errors.Is(
			err,
			context.Canceled,
		) {
			t.Fatal(err)
		}
		if _, err := session.Begin[parts](
			cctx,
			session.NewMemory(),
		); !errors.Is(
			err,
			context.Canceled,
		) {
			t.Fatal(err)
		}
		if _, err := session.Resume[parts](
			cctx,
			session.NewMemory(),
			ss.ID,
		); !errors.Is(
			err,
			context.Canceled,
		) {
			t.Fatal(err)
		}
	})
	t.Run("invalid declared digest", func(t *testing.T) {
		if _, err := session.Begin[parts](
			ctx,
			session.NewMemory(),
			blob.WithExpectedSHA256("nope"),
		); !errors.Is(
			err,
			blob.ErrInvalidDigest,
		) {
			t.Fatal(err)
		}
	})
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errInjected }

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

func TestUnitResumeRejects(t *testing.T) {
	ctx := context.Background()
	st := session.NewMemory()
	id := begin(t, st).ID
	for name, state := range map[string]string{
		"not json":   `{`,
		"bad hasher": `{"hasher":"anVuaw=="}`,
		"bad data":   `{"hasher":"","data":{"sizes":"x"}}`,
	} {
		_ = st.Put(ctx, id, []byte(state), 0)
		if _, err := session.Resume[parts](ctx, st, id); !errors.Is(err, blob.ErrUploadUnknown) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, bad := range []string{"", "../x", "0123456789abcdef0123456789abcdeZ"} {
		if _, err := session.Resume[parts](ctx, st, bad); !errors.Is(err, blob.ErrUploadUnknown) {
			t.Errorf("id %q: %v", bad, err)
		}
	}
	if _, err := session.Resume[parts](ctx, st, "0123456789abcdef0123456789abcdef"); !errors.Is(
		err, blob.ErrUploadUnknown) {
		t.Errorf("never issued: %v", err)
	}
}

func TestUnitMemoryCopies(t *testing.T) {
	ctx := context.Background()
	m := session.NewMemory()
	state := []byte("xy")
	_ = m.Put(ctx, "a", state, 2)
	state[0] = 'z'
	got, err := m.Get(ctx, "a")
	if err != nil || string(got) != "xy" {
		t.Fatalf("Put must copy: %q %v", got, err)
	}
	got[1] = 'q'
	if again, _ := m.Get(ctx, "a"); string(again) != "xy" {
		t.Fatal("Get must copy")
	}
	_ = m.Delete(ctx, "a")
	if _, err := m.Get(ctx, "a"); !errors.Is(err, blob.ErrUploadUnknown) {
		t.Fatal(err)
	}
}

// TestUnitInUploadsSeparatesStores is decision 0027's acceptance check: two blob
// stores of one kind share the uploads table without seeing each other's
// sessions, and each row names the store that owns it.
func TestUnitInUploadsSeparatesStores(t *testing.T) {
	ctx := context.Background()
	uploads := inmem.New().Uploads()
	hot := session.InUploads(uploads, "s3:hot")
	cold := session.InUploads(uploads, "s3:cold")
	a := begin(t, hot)
	b := begin(t, cold)
	if _, err := session.Resume[parts](ctx, cold, a.ID); !errors.Is(err, blob.ErrUploadUnknown) {
		t.Fatalf("cold resumed hot's session: %v", err)
	}
	if _, err := session.Resume[parts](ctx, hot, b.ID); !errors.Is(err, blob.ErrUploadUnknown) {
		t.Fatalf("hot resumed cold's session: %v", err)
	}
	stale, err := uploads.Stale(ctx, time.Now().Add(time.Hour), 10)
	if err != nil || len(stale) != 2 {
		t.Fatalf("Stale = %v, %v", stale, err)
	}
	owners := map[string]string{}
	for _, u := range stale {
		owners[u.ID] = u.Backend
	}
	if owners[a.ID] != "s3:hot" || owners[b.ID] != "s3:cold" {
		t.Fatalf("owners = %v", owners)
	}
}

func TestUnitInUploadsRoundTripKeepsStart(t *testing.T) {
	ctx := context.Background()
	uploads := inmem.New().Uploads()
	st := session.InUploads(uploads, "gcs:main")
	ss := begin(t, st)
	first, err := uploads.Get(ctx, ss.ID)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := ss.Append(ctx, bytes.NewReader([]byte("abc")), 8, collect(ss)); err != nil {
		t.Fatal(err)
	}
	second, err := uploads.Get(ctx, ss.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Started.Equal(first.Started) || !second.Updated.After(first.Updated) ||
		second.Bytes != 3 {
		t.Fatalf("first %+v second %+v", first, second)
	}
	again, err := session.Resume[parts](ctx, st, ss.ID)
	if err != nil || string(again.Pending) != "abc" {
		t.Fatalf("Resume = %+v, %v", again, err)
	}
	if err := again.Forget(ctx); err != nil {
		t.Fatal(err)
	}
	if err := again.Forget(ctx); err != nil {
		t.Fatalf("forgetting twice must succeed: %v", err)
	}
}

func TestUnitInUploadsErrors(t *testing.T) {
	ctx := context.Background()
	st := session.InUploads(failingUploads{get: errInjected}, "azure:x")
	if err := st.Put(ctx, "u", nil, 0); !errors.Is(err, errInjected) {
		t.Fatalf("Put when the load fails: %v", err)
	}
	if _, err := st.Get(
		ctx,
		"u",
	); !errors.Is(err, errInjected) ||
		errors.Is(err, blob.ErrUploadUnknown) {
		t.Fatalf("Get: %v", err)
	}
	st = session.InUploads(
		failingUploads{get: store.ErrNotFound, put: errInjected, del: errInjected},
		"azure:x",
	)
	if err := st.Put(ctx, "u", nil, 0); !errors.Is(err, errInjected) {
		t.Fatalf("Put: %v", err)
	}
	if err := st.Delete(ctx, "u"); !errors.Is(err, errInjected) {
		t.Fatalf("Delete: %v", err)
	}
}

type failingUploads struct{ get, put, del error }

func (f failingUploads) Put(context.Context, store.Upload) error { return f.put }
func (f failingUploads) Get(context.Context, string) (store.Upload, error) {
	return store.Upload{}, f.get
}
func (f failingUploads) Delete(context.Context, string) error { return f.del }
func (failingUploads) Stale(context.Context, time.Time, int) ([]store.Upload, error) {
	return nil, nil
}
