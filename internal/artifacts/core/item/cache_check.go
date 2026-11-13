package item

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

// checkCopy runs validate over body, the server's own copy of a remote
// file. A refusal whose check met a failed read of body is a StorageFault.
//
// The cache tells the two apart rather than each hook, since a hook sees
// only an io.ReadSeeker, and many map every error, a failed read included,
// to source.ErrInvalidResult or a 502 they build (issue 1269). A hook that
// fails before any read of body fails keeps its own error. A hook that
// passes despite a failed read is left as it was: only a refusal changes.
func checkCopy(
	ctx context.Context,
	validate func(context.Context, io.ReadSeeker, int64) error,
	body io.ReadSeeker,
	size int64,
) error {
	var log faultLog
	err := validate(ctx, watch(body, &log), size)
	if err == nil {
		return nil
	}
	err = fmt.Errorf("cache validation: %w", err)
	if read := log.failure(); read != nil {
		return MarkStorageFault(ctx, read, err)
	}
	return err
}

// inspectedCopy is inspected for the copy Cache just committed, with the
// format's opens and reads of stored content watched as checkCopy watches
// a Validate hook's reads: an inspection that fails after one of them
// failed is a StorageFault. The environment it returns, the publication's,
// has the inspection, and notes its hooks' opens and reads in hooks, which
// Cache consults when the publication fails.
func (s *Service) inspectedCopy(
	ctx context.Context,
	d Deploy,
	actual kernel.Checksums,
	size int64,
	hooks *faultLog,
) (format.Env, error) {
	var log faultLog
	// The copy differs only in its blob store. Service holds configuration
	// and no state, and inspected builds the Write the format inspects.
	watched := *s
	watched.Blobs = watchedStore{Store: s.Blobs, log: &log}
	inspected, err := watched.inspected(ctx, d, actual, size)
	if err != nil {
		if cause := log.failure(); cause != nil {
			return format.Env{}, MarkStorageFault(ctx, cause, err)
		}
		return format.Env{}, err
	}
	env := s.env()
	env.Inspection = inspected.Inspection
	if s.Blobs != nil {
		env.Blobs = watchedStore{Store: s.Blobs, log: hooks}
	}
	return env, nil
}

// faultLog keeps the first failed open or read of stored content.
// Seek is not watched: every blob store's Seek fails only for a bad whence
// or a negative position, which is the caller's mistake (the blob/s3, gcs,
// azure and mirror readers), and os.File's likewise.
type faultLog struct {
	mu  sync.Mutex
	err error
}

func (l *faultLog) note(err error) {
	if err == nil || errors.Is(err, io.EOF) {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err == nil {
		l.err = err
	}
}

func (l *faultLog) failure() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

// watch returns body with its reads noted in log. It has ReadAt only when
// body has, so a check that looks for io.ReaderAt (archive.ReaderAt) takes
// the path it took over body.
func watch(body io.ReadSeeker, log *faultLog) io.ReadSeeker {
	w := &watchedCopy{r: body, log: log}
	if at, ok := body.(io.ReaderAt); ok {
		return &watchedCopyAt{watchedCopy: w, at: at}
	}
	return w
}

type watchedCopy struct {
	r   io.ReadSeeker
	log *faultLog
}

func (w *watchedCopy) Read(p []byte) (int, error) {
	n, err := w.r.Read(p)
	w.log.note(err)
	return n, err //nolint:wrapcheck // the store's own error, which callers compare with io.EOF
}

func (w *watchedCopy) Seek(offset int64, whence int) (int64, error) {
	return w.r.Seek(offset, whence) //nolint:wrapcheck // the store's own error
}

type watchedCopyAt struct {
	*watchedCopy
	at io.ReaderAt
}

// ReadAt at a negative offset fails as the caller's mistake, as a Seek to
// a negative position does, so that failure is not noted.
func (w *watchedCopyAt) ReadAt(p []byte, off int64) (int, error) {
	n, err := w.at.ReadAt(p, off)
	if off >= 0 {
		w.log.note(err)
	}
	return n, err //nolint:wrapcheck // the store's own error, which callers compare with io.EOF
}

// watchedStore is a blob store whose opens, and reads of what it opens,
// are noted in log.
type watchedStore struct {
	blob.Store
	log *faultLog
}

func (s watchedStore) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	r, info, err := s.Store.Open(ctx, sha)
	if err != nil {
		s.log.note(err)
		return nil, info, err //nolint:wrapcheck // the store's own error
	}
	switch w := watch(r, s.log).(type) {
	case *watchedCopyAt:
		return watchedFileAt{w, r}, info, nil
	default:
		return watchedFile{w.(*watchedCopy), r}, info, nil
	}
}

type watchedFile struct {
	*watchedCopy
	io.Closer
}

type watchedFileAt struct {
	*watchedCopyAt
	io.Closer
}
