package mirror

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob"
)

var errInjected = errors.New("injected")

// fake is an in-memory member store. failOn makes the (after+1)th and later
// calls of an operation fail; delay makes an operation wait (in synctest's
// virtual time) or until its context ends, for slow-member scenarios.
type fake struct {
	mu      sync.Mutex
	blobs   map[string][]byte
	uploads map[string]*fakeUpload
	nextID  int
	calls   map[string]int
	fail    map[string]failure
	delay   map[string]time.Duration
	// appends records the length of every Append, in order.
	appends []int
}

type failure struct {
	after int
	err   error
}

func newFake() *fake {
	return &fake{
		blobs: map[string][]byte{}, uploads: map[string]*fakeUpload{},
		calls: map[string]int{}, fail: map[string]failure{}, delay: map[string]time.Duration{},
	}
}

// failOn makes op fail with err from its (after+1)th call onwards.
func (f *fake) failOn(op string, after int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail[op] = failure{after: after, err: err}
	f.calls[op] = 0
}

// heal clears every injected failure.
func (f *fake) heal() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = map[string]failure{}
}

func (f *fake) slow(op string, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delay[op] = d
}

// enter counts a call of op, applies its delay and returns its injected
// failure.
func (f *fake) enter(ctx context.Context, op string) error {
	f.mu.Lock()
	f.calls[op]++
	fl, failing := f.fail[op]
	n := f.calls[op]
	d := f.delay[op]
	f.mu.Unlock()
	if d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return fmt.Errorf("fake %s: %w", op, ctx.Err())
		}
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("fake %s: %w", op, err)
	}
	if failing && n > fl.after {
		return fl.err
	}
	return nil
}

func (f *fake) has(sha string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.blobs[sha]
	return ok
}

func (f *fake) BeginUpload(ctx context.Context, opts ...blob.UploadOption) (blob.Upload, error) {
	if err := f.enter(ctx, "begin"); err != nil {
		return nil, err
	}
	o, err := blob.ResolveOptions(opts...)
	if err != nil {
		return nil, err //nolint:wrapcheck // test fake
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	u := &fakeUpload{f: f, id: fmt.Sprintf("%032x", f.nextID), expected: o.ExpectedSHA256}
	f.uploads[u.id] = u
	return u, nil
}

func (f *fake) ResumeUpload(ctx context.Context, id string) (blob.Upload, error) {
	if err := f.enter(ctx, "resume"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.uploads[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", blob.ErrUploadUnknown, id)
	}
	return u, nil
}

func (f *fake) Stat(ctx context.Context, sha string) (blob.Info, error) {
	if err := f.enter(ctx, "stat"); err != nil {
		return blob.Info{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.blobs[sha]
	if !ok {
		return blob.Info{}, fmt.Errorf("%w: %s", blob.ErrNotFound, sha)
	}
	return blob.Info{Size: int64(len(b)), Digests: blob.Digests{SHA256: sha}}, nil
}

func (f *fake) Exists(ctx context.Context, sha string) (bool, error) {
	_, err := f.Stat(ctx, sha)
	if errors.Is(err, blob.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (f *fake) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	if err := f.enter(ctx, "open"); err != nil {
		return nil, blob.Info{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.blobs[sha]
	if !ok {
		return nil, blob.Info{}, fmt.Errorf("%w: %s", blob.ErrNotFound, sha)
	}
	r := &fakeReader{Reader: bytes.NewReader(b), f: f, ctx: ctx}
	return r, blob.Info{Size: int64(len(b)), Digests: blob.Digests{SHA256: sha}}, nil
}

func (f *fake) Delete(ctx context.Context, sha string) error {
	if err := f.enter(ctx, "delete"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.blobs, sha)
	return nil
}

func (f *fake) Walk(ctx context.Context, fn func(blob.Info) error) error {
	if err := f.enter(ctx, "walk"); err != nil {
		return err
	}
	f.mu.Lock()
	keys := slices.Sorted(maps.Keys(f.blobs))
	sizes := map[string]int64{}
	for _, k := range keys {
		sizes[k] = int64(len(f.blobs[k]))
	}
	f.mu.Unlock()
	for _, k := range keys {
		if err := fn(blob.Info{Size: sizes[k], Digests: blob.Digests{SHA256: k}}); err != nil {
			return err
		}
	}
	return nil
}

// presigner is a fake that signs storage URLs.
type presigner struct{ *fake }

// cdnSigner is a fake that signs storage and CDN URLs.
type cdnSigner struct{ *fake }

func (c cdnSigner) Signs(blob.Via) bool { return true }

func (c cdnSigner) SignURL(ctx context.Context, sha string, o blob.SignOptions) (string, error) {
	if err := c.enter(ctx, "sign"); err != nil {
		return "", err
	}
	return o.Via.String() + "://" + sha + "/" + o.Filename, nil
}

// parted is a fake that writes in parts of a fixed size.
type parted struct {
	*fake
	part int64
}

func (p parted) PartSize() int64 { return p.part }

func (p presigner) Signs(via blob.Via) bool { return via == blob.ViaStorage }

func (p presigner) SignURL(ctx context.Context, sha string, o blob.SignOptions) (string, error) {
	if err := p.enter(ctx, "sign"); err != nil {
		return "", err
	}
	if o.Via != blob.ViaStorage {
		return "", blob.ErrCannotSign
	}
	return "fake://" + sha, nil
}

type fakeReader struct {
	*bytes.Reader
	f   *fake
	ctx context.Context //nolint:containedctx // test fake mirrors the real readers
}

func (r *fakeReader) Read(p []byte) (int, error) {
	if err := r.f.enter(r.ctx, "read"); err != nil {
		return 0, err
	}
	return r.Reader.Read(p) //nolint:wrapcheck // test fake
}

func (r *fakeReader) Close() error { return nil }

type fakeUpload struct {
	f        *fake
	id       string
	expected string
	buf      []byte
}

func (u *fakeUpload) ID() string { return u.id }

func (u *fakeUpload) Size() int64 {
	u.f.mu.Lock()
	defer u.f.mu.Unlock()
	return int64(len(u.buf))
}

func (u *fakeUpload) live() error {
	u.f.mu.Lock()
	defer u.f.mu.Unlock()
	if _, ok := u.f.uploads[u.id]; !ok {
		return fmt.Errorf("%w: %s", blob.ErrUploadUnknown, u.id)
	}
	return nil
}

func (u *fakeUpload) Append(ctx context.Context, r io.Reader) (int64, error) {
	if err := u.live(); err != nil {
		return 0, err
	}
	if err := u.f.enter(ctx, "append"); err != nil {
		return 0, err
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return 0, err //nolint:wrapcheck // test fake
	}
	u.f.mu.Lock()
	defer u.f.mu.Unlock()
	u.buf = append(u.buf, b...)
	u.f.appends = append(u.f.appends, len(b))
	return int64(len(b)), nil
}

func (u *fakeUpload) Commit(ctx context.Context, want blob.Digests) (blob.Info, error) {
	if err := u.live(); err != nil {
		return blob.Info{}, err
	}
	if err := u.f.enter(ctx, "commit"); err != nil {
		return blob.Info{}, err
	}
	u.f.mu.Lock()
	defer u.f.mu.Unlock()
	h := blob.NewHasher()
	_, _ = h.Write(u.buf)
	got := h.Sum()
	if want.SHA256 == "" {
		want.SHA256 = u.expected
	}
	if err := blob.Verify(want, got); err != nil {
		delete(u.f.uploads, u.id)
		return blob.Info{}, err //nolint:wrapcheck // test fake
	}
	u.f.blobs[got.SHA256] = bytes.Clone(u.buf)
	delete(u.f.uploads, u.id)
	return blob.Info{Size: int64(len(u.buf)), Digests: got}, nil
}

func (u *fakeUpload) Abort(ctx context.Context) error {
	if err := u.f.enter(ctx, "abort"); err != nil {
		return err
	}
	u.f.mu.Lock()
	defer u.f.mu.Unlock()
	delete(u.f.uploads, u.id)
	return nil
}
