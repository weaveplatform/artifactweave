package item

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

var (
	errDropped = errors.New("the store dropped the connection")
	// errBadCopy stands for a hook's refusal of the content, such as
	// source.ErrInvalidResult, which answers 502.
	errBadCopy = errors.New("bad content")
)

// cutCopy is a copy that can only seek, as S3's, GCS's and Azure's is,
// whose first sound reads are served and every later one fails.
type cutCopy struct {
	r     *bytes.Reader
	sound int
}

func (c *cutCopy) Read(p []byte) (int, error) {
	if c.sound == 0 {
		return 0, errDropped
	}
	c.sound--
	return c.r.Read(p)
}

func (c *cutCopy) Seek(off int64, whence int) (int64, error) { return c.r.Seek(off, whence) }

// cutCopyAt is a cutCopy with ReadAt, as a file store's copy is.
type cutCopyAt struct{ *cutCopy }

func (c cutCopyAt) ReadAt(p []byte, off int64) (int, error) {
	if c.sound == 0 {
		return 0, errDropped
	}
	c.sound--
	return c.r.ReadAt(p, off)
}

func cut(content string, sound int, at bool) io.ReadSeeker {
	c := &cutCopy{r: bytes.NewReader([]byte(content)), sound: sound}
	if at {
		return cutCopyAt{c}
	}
	return c
}

// readAll is a hook that reads the whole copy, through ReadAt when it
// has one, and refuses it with errBadCopy for any error, as most remote
// hooks map any error to source.ErrInvalidResult. It fails t when the copy
// it gets lacks or gains ReadAt.
func readAll(t *testing.T, at bool) func(context.Context, io.ReadSeeker, int64) error {
	return func(_ context.Context, r io.ReadSeeker, size int64) error {
		ra, ok := r.(io.ReaderAt)
		if ok != at {
			t.Fatalf("the hook's copy has ReadAt %v, want %v", ok, at)
		}
		var err error
		if ok {
			_, err = io.ReadAll(io.NewSectionReader(ra, 0, size))
		} else {
			_, err = io.ReadAll(r)
		}
		if err != nil {
			return errBadCopy
		}
		return nil
	}
}

// A hook that refuses the copy after a read of it failed refused the
// storage's fault, not the content: wherever the read fails, on a copy
// with ReadAt or one that can only seek, checkCopy returns a StorageFault
// that is format.ErrStorageFault and the store's error, never the hook's
// refusal, which it keeps as Refusal. Once every read is sound the copy
// passes.
func TestUnitCheckCopyMarksAFailedRead(t *testing.T) {
	content := strings.Repeat("payload ", 400)
	for _, at := range []bool{false, true} {
		failed := 0
		for sound := 0; ; sound++ {
			err := checkCopy(
				t.Context(),
				readAll(t, at),
				cut(content, sound, at),
				int64(len(content)),
			)
			if err == nil {
				break
			}
			var fault *StorageFault
			if !errors.As(err, &fault) || !errors.Is(err, format.ErrStorageFault) ||
				!errors.Is(err, errDropped) || errors.Is(err, errBadCopy) ||
				!errors.Is(fault.Err, errBadCopy) {
				t.Fatalf("ReadAt %v, %d sound reads: %v", at, sound, err)
			}
			if got := err.Error(); got != "remote cache: format: storage fault: "+errDropped.Error() {
				t.Fatalf("ReadAt %v, %d sound reads: text %q", at, sound, got)
			}
			failed++
		}
		if failed < 3 {
			t.Fatalf("ReadAt %v: only %d reads failed", at, failed)
		}
	}
}

// failingSeek is a copy whose Seek fails, as a store's does only for a
// bad whence or a negative position.
type failingSeek struct{ io.ReadSeeker }

func (failingSeek) Seek(int64, int) (int64, error) { return 0, errors.New("negative position") }

// What is no failed read of the copy keeps the hook's own answer: a
// refusal before any read or after sound ones, a pass despite a failed
// read, a failure while the request ends, the copy's end, a failed seek
// and a read at a negative offset.
func TestUnitCheckCopyKeepsWhatIsNoFailedRead(t *testing.T) {
	const content = "payload"
	ended, cancel := context.WithCancel(t.Context())
	cancel()
	for name, tc := range map[string]struct {
		ctx  context.Context
		body io.ReadSeeker
		hook func(context.Context, io.ReadSeeker, int64) error
		pass bool
	}{
		"refused before a read": {
			body: cut(content, 0, false),
			hook: func(context.Context, io.ReadSeeker, int64) error { return errBadCopy },
		},
		"refused after sound reads": {
			body: strings.NewReader(content),
			hook: func(_ context.Context, r io.ReadSeeker, _ int64) error {
				if _, err := io.ReadAll(r); err != nil {
					t.Fatal(err)
				}
				return errBadCopy
			},
		},
		"passes despite a failed read": {
			body: cut(content, 0, true),
			hook: func(_ context.Context, r io.ReadSeeker, _ int64) error {
				_, _ = io.ReadAll(r)
				return nil
			},
			pass: true,
		},
		"request ended": {
			ctx:  ended,
			body: cut(content, 0, false),
			hook: readAll(t, false),
		},
		"end of the copy": {
			body: strings.NewReader(content),
			hook: func(_ context.Context, r io.ReadSeeker, _ int64) error {
				if _, err := r.Read(make([]byte, 64)); err != nil {
					t.Fatal(err)
				}
				if _, err := r.Read(make([]byte, 64)); !errors.Is(err, io.EOF) {
					t.Fatal(err)
				}
				return errBadCopy
			},
		},
		"failed seek": {
			body: failingSeek{strings.NewReader(content)},
			hook: func(_ context.Context, r io.ReadSeeker, _ int64) error {
				if _, err := r.Seek(-1, io.SeekStart); err != nil {
					return errBadCopy
				}
				return nil
			},
		},
		"read at a negative offset": {
			body: strings.NewReader(content),
			hook: func(_ context.Context, r io.ReadSeeker, _ int64) error {
				if _, err := r.(io.ReaderAt).ReadAt(make([]byte, 1), -1); err != nil {
					return errBadCopy
				}
				return nil
			},
		},
	} {
		ctx := tc.ctx
		if ctx == nil {
			ctx = t.Context()
		}
		err := checkCopy(ctx, tc.hook, tc.body, int64(len(content)))
		if tc.pass {
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			continue
		}
		if !errors.Is(err, errBadCopy) || errors.Is(err, format.ErrStorageFault) ||
			errors.As(err, new(*StorageFault)) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// readFaultBlobs fails every open of sha with open, or every read of what
// it opens of sha with read, as a store whose connection drops does. What
// it opens of sha can only seek.
type readFaultBlobs struct {
	blob.Store
	sha        string
	open, read error
}

func (b readFaultBlobs) Open(
	ctx context.Context,
	sha string,
) (io.ReadSeekCloser, blob.Info, error) {
	if sha == b.sha && b.open != nil {
		return nil, blob.Info{}, b.open
	}
	r, info, err := b.Store.Open(ctx, sha)
	if err != nil || sha != b.sha || b.read == nil {
		return r, info, err
	}
	return failingRead{r, b.read}, info, nil
}

type failingRead struct {
	io.ReadSeekCloser
	err error
}

func (f failingRead) Read([]byte) (int, error) { return 0, f.err }

// inspecting is a format whose Inspect reads the stored copy, refusing it
// with errBadCopy for any error, and whose Deployed keeps the environment
// it is given.
type inspecting struct {
	generic.Handler
	refuse   bool
	deployed []format.Env
}

func (h *inspecting) Inspect(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	w format.Write,
) (any, error) {
	r, _, err := env.Blobs.Open(ctx, w.Checksums.SHA256)
	if err != nil {
		return nil, errBadCopy
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil || h.refuse {
		return nil, errBadCopy
	}
	return "inspected " + string(b), nil
}

func (h *inspecting) Deployed(
	_ context.Context,
	env format.Env,
	_ repo.Config,
	_ model.Item,
) error {
	h.deployed = append(h.deployed, env)
	return nil
}

// A remote file whose committed copy fails to open or to read while the
// cache checks it, by the hook or by the format's inspection, is a
// StorageFault, and the cache keeps neither an entry nor an item. A
// refusal of a sound copy keeps the check's own error. The publication's
// hooks get the inspection, and read the service's own blob store.
func TestUnitCacheMarksAFailedReadOfItsCopy(t *testing.T) {
	sha := sums("payload").SHA256
	refuse := func(context.Context, io.ReadSeeker, int64) error { return errBadCopy }
	for name, tc := range map[string]struct {
		open, read error
		validate   func(context.Context, io.ReadSeeker, int64) error
		inspect    *inspecting
		marked     bool
	}{
		"validate open":    {open: errDropped, validate: readAll(t, false), marked: true},
		"validate read":    {read: errDropped, validate: readAll(t, false), marked: true},
		"validate refusal": {validate: refuse},
		"inspect open":     {open: errDropped, inspect: &inspecting{}, marked: true},
		"inspect read":     {read: errDropped, inspect: &inspecting{}, marked: true},
		"inspect refusal":  {inspect: &inspecting{refuse: true}},
	} {
		t.Run(name, func(t *testing.T) {
			w, u := remoteWorld(t)
			w.svc.Blobs = readFaultBlobs{Store: w.svc.Blobs, sha: sha, open: tc.open, read: tc.read}
			u.Validate = tc.validate
			if tc.inspect != nil {
				w.svc.Formats = func(repo.PackageType) format.Handler { return tc.inspect }
			}
			_, err := w.svc.Cache(t.Context(), u)
			var fault *StorageFault
			if marked := errors.As(
				err,
				&fault,
			); marked != tc.marked ||
				!errors.Is(err, errBadCopy) == !tc.marked {
				t.Fatalf("marked %v: %v", marked, err)
			}
			if tc.marked &&
				(!errors.Is(err, errDropped) || !errors.Is(err, format.ErrStorageFault) ||
					!errors.Is(fault.Err, errBadCopy) && fault.Err != fault.Cause) {
				t.Fatalf("fault %v", err)
			}
			if _, err = w.db.RemoteCache().
				Read(t.Context(), u.Token.Key); !errors.Is(
				err,
				kernel.ErrNotFound,
			) {
				t.Fatalf("cache entry: %v", err)
			}
			if _, err = w.db.Items().
				Get(t.Context(), u.Repo.Key, true, u.Token.Key.Path); !errors.Is(
				err,
				kernel.ErrNotFound,
			) {
				t.Fatalf("item: %v", err)
			}
		})
	}
	w, u := remoteWorld(t)
	h := &inspecting{}
	w.svc.Formats = func(repo.PackageType) format.Handler { return h }
	if _, err := w.svc.Cache(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	if len(h.deployed) != 1 || h.deployed[0].Inspection != "inspected payload" {
		t.Fatalf("publication environment: %+v", h.deployed)
	}
	r, _, err := h.deployed[0].Blobs.Open(t.Context(), sha)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if b, err := io.ReadAll(r); err != nil || string(b) != "payload" {
		t.Fatalf("publication blob store: %q %v", b, err)
	}
}

// A spooled copy the cache will not keep is checked as a committed one
// is: the hook's refusal of sound content keeps its error.
func TestUnitCacheTransientCheckKeepsARefusal(t *testing.T) {
	w, u := remoteWorld(t)
	u.NoStore = true
	u.Validate = func(_ context.Context, r io.ReadSeeker, _ int64) error {
		if _, ok := r.(io.ReaderAt); !ok {
			t.Fatal("the spooled copy lost ReadAt")
		}
		return errBadCopy
	}
	if _, err := w.svc.Cache(t.Context(), u); !errors.Is(err, errBadCopy) ||
		errors.Is(err, format.ErrStorageFault) {
		t.Fatal(err)
	}
}
