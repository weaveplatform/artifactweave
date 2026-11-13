package item

import (
	"context"
	"errors"
	"io"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

// StorageFault is a failure of the server's own storage while the cache
// keeps a remote file: a failed open or read of the copy it checks or
// inspects or that a hook reads, a failed write of the copy or of its
// spool file, or a failed call of the store that publishes it. It is
// format.ErrStorageFault and Cause, the store's error, and so answers 500
// "internal error" rather than the 502 an upstream's failure gets.
//
// Err is the error the cache returned without the mark, and is not
// unwrapped: a hook's refusal must not answer for a fault of the storage.
// The remote policy maps it as it did before wherever that answer served a
// stale copy or counted toward the assumed-offline circuit
// (remote.PolicyError), so only what answered 502 changes.
type StorageFault struct {
	Cause error
	Err   error
}

// Error names the store's error, for the log.
func (e *StorageFault) Error() string {
	return "remote cache: " + format.ErrStorageFault.Error() + ": " + e.Cause.Error()
}

// Unwrap is format.ErrStorageFault and the store's error.
func (e *StorageFault) Unwrap() []error { return []error{format.ErrStorageFault, e.Cause} }

// MarkStorageFault is err, an error that cause, a failure of the server's
// own storage, brought about, marked as a StorageFault, unless ctx is done:
// a call that fails because the request ended answers as it did before. An
// error already marked keeps its mark.
func MarkStorageFault(ctx context.Context, cause, err error) error {
	if ctx.Err() != nil || errors.As(err, new(*StorageFault)) {
		return err
	}
	return &StorageFault{Cause: cause, Err: err}
}

// storeFault marks err, a failed call of the store or blob store that
// keeps a remote file, as a StorageFault. The store's verdicts are left as
// they are: kernel.ErrNotFound, kernel.ErrConflict and kernel.ErrInvalid
// say what the store holds, not that it failed, and the remote policy maps
// them as it did (a conflict is a superseded read).
func storeFault(ctx context.Context, err error) error {
	if err == nil || errors.Is(err, kernel.ErrNotFound) || errors.Is(err, kernel.ErrConflict) ||
		errors.Is(err, kernel.ErrInvalid) {
		return err
	}
	return MarkStorageFault(ctx, err, err)
}

// uploadFault is err, Upload's failure to keep the upstream's body, marked
// as a StorageFault where the blob store failed. A failed read of the body
// (noted in body) is the upstream's failure, a connectivity 502 or a
// timeout 504 with its stale fallback, and a body whose digest is not the
// one declared is the upstream's bad content; both keep their error.
func uploadFault(ctx context.Context, err error, body *faultLog) error {
	if body.failure() != nil || errors.Is(err, ErrChecksumMismatch) ||
		errors.Is(err, ErrBadChecksum) ||
		errors.Is(err, blob.ErrDigestMismatch) {
		return err
	}
	return storeFault(ctx, err)
}

// spoolUpstream copies body, the upstream's, to w, the server's own file.
// A failed write is a StorageFault; a failed read of body is the
// upstream's and keeps its error.
func spoolUpstream(ctx context.Context, w io.Writer, body io.Reader) (int64, error) {
	var reads faultLog
	n, err := io.Copy(w, upstreamBody{r: body, log: &reads})
	if err != nil && reads.failure() == nil {
		return n, storeFault(ctx, err)
	}
	return n, err //nolint:wrapcheck // the upstream's error, which the caller wraps
}

// upstreamBody is the upstream's body with its failed reads noted in log,
// so that a failure of the copy it feeds can tell the upstream's from the
// storage's.
type upstreamBody struct {
	r   io.Reader
	log *faultLog
}

func (b upstreamBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.log.note(err)
	return n, err //nolint:wrapcheck // the upstream's own error, which callers compare with io.EOF
}
