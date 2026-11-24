package sync

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
)

// Receive applies a batch caller pushed and returns the highest sequence
// now applied from it on the stream (the peer API's POST
// streams/{stream}/events). ErrNoStream or ErrRefused when caller may not
// send the stream here or b.Origin is not caller; kernel.ErrInvalid for a
// batch whose entries are not consecutive from FromSeq; a *ConflictError
// when FromSeq is not the sequence expected next, a batch already applied
// included, so a retried batch changes nothing.
func (e *Engine) Receive(ctx context.Context, caller model.Peer, b Batch) (int64, error) {
	k, err := e.authorize(ctx, caller, b.Stream, Receive)
	if err != nil {
		return 0, err
	}
	if b.Origin != caller.InstanceID {
		return 0, ErrRefused
	}
	if err := consecutive(b.FromSeq, b.Entries); err != nil {
		return 0, err
	}
	m, err := e.Store.Outbox().Applied(ctx, b.Origin, b.Stream)
	if err != nil {
		return 0, fmt.Errorf("applied position on %s: %w", b.Stream, err)
	}
	if b.FromSeq != m.Expect() {
		return 0, &ConflictError{Expected: m.Expect()}
	}
	if err := e.apply(ctx, caller, k, b.Stream, b.Entries); err != nil {
		return 0, err
	}
	m, err = e.Store.Outbox().Applied(ctx, b.Origin, b.Stream)
	if err != nil {
		return 0, fmt.Errorf("applied position on %s: %w", b.Stream, err)
	}
	return m.LastSeq, nil
}

// consecutive checks a batch or page: at least one entry, at most
// MaxBatch, in sequence from first without a gap, each one the wire can
// carry.
func consecutive(first int64, entries []outbox.OutboxEntry) error {
	if len(entries) == 0 || len(entries) > MaxBatch {
		return fmt.Errorf("a batch holds 1 to %d events: %w", MaxBatch, kernel.ErrInvalid)
	}
	for i, en := range entries {
		if en.Seq != first+int64(i) {
			return fmt.Errorf("event %d has sequence %d, not %d: %w", i, en.Seq, first+int64(i), kernel.ErrInvalid)
		}
		if err := validEntry(en); err != nil {
			return fmt.Errorf("event %d: %w", i, err)
		}
	}
	return nil
}

// apply applies from's entries of stream in sequence, each in its own
// transaction with its applied position. A content entry's blob is fetched
// first, outside any transaction, since no metadata transaction spans
// network I/O; a failed fetch still applies the entry, recording a failed
// binary and queueing its retry, so the stream moves on.
func (e *Engine) apply(ctx context.Context, from model.Peer, k Kind, stream string, entries []outbox.OutboxEntry) error {
	for _, en := range entries {
		en.Origin, en.Stream = from.InstanceID, stream
		held, reason, err := e.ensureBlob(ctx, []model.Peer{from}, en.BlobSHA256, en.BlobSize)
		if err != nil {
			return err
		}
		err = e.Store.Tx(ctx, func(ctx context.Context) error {
			m, err := e.Store.Outbox().LockApplied(ctx, en.Origin, stream)
			switch {
			case err != nil:
				return fmt.Errorf("applied position on %s: %w", stream, err)
			case m.LastSeq >= en.Seq:
				return errApplied
			case m.LastSeq != en.Seq-1:
				return &ConflictError{Expected: m.Expect()}
			}
			if _, err := e.regrant(ctx, from.InstanceID, stream, Receive); err != nil {
				return err
			}
			e.observe(en.Time)
			if err := k.Apply(ctx, en, held); err != nil {
				return fmt.Errorf("apply %s %d: %w", stream, en.Seq, err)
			}
			if !held {
				if err := e.recordFailure(ctx, stream, en.BlobSHA256, reason); err != nil {
					return err
				}
				if err := e.enqueue(ctx, FetchKind, item{Stream: stream, SHA256: en.BlobSHA256}); err != nil {
					return err
				}
			}
			m.LastSeq = en.Seq
			if err := e.Store.Outbox().SetApplied(ctx, m); err != nil {
				return fmt.Errorf("record applied position on %s: %w", stream, err)
			}
			return nil
		})
		if err != nil && !errors.Is(err, errApplied) {
			return err //nolint:wrapcheck // the callback names its failures
		}
	}
	return nil
}

// recordFailure records a failed binary for FED-06 to list and replay.
func (e *Engine) recordFailure(ctx context.Context, stream, sha256, reason string) error {
	f := outbox.FailedBlobFetch{Stream: stream, SHA256: sha256, FirstFailed: e.now(), LastError: reason}
	if kind, rest, err := model.ParseStream(stream); err == nil && kind == model.StreamRepository {
		f.RepoKey = rest
	}
	if _, err := e.Store.Outbox().RecordFailedFetch(ctx, f); err != nil {
		return fmt.Errorf("record failed fetch of %s: %w", sha256, err)
	}
	return nil
}

// ensureBlob makes sure the blob sha256 of size bytes is in the blob store,
// fetching it from the first of from that serves it. held is false, with
// the last failure as reason, when none did; err is a failure of this
// instance's own store, which stops the apply rather than marking content
// it may well hold as failed. No sha256 needs nothing.
func (e *Engine) ensureBlob(ctx context.Context, from []model.Peer, sha256 string, size int64) (held bool, reason string, err error) {
	if sha256 == "" {
		return true, "", nil
	}
	ok, err := e.Blobs.Exists(ctx, sha256)
	if err != nil {
		return false, "", fmt.Errorf("look up blob %s: %w", sha256, err)
	}
	if ok {
		return true, "", nil
	}
	reason = "no peer to fetch the blob from"
	for _, p := range from {
		err := e.fetch(ctx, p, sha256, size)
		if err == nil {
			return true, "", nil
		}
		reason = err.Error()
	}
	return false, reason, nil
}

// fetch streams one peer's blob into the blob store. The store's commit
// checks the SHA-256 and fetch the size before committing, so content
// that is corrupt, truncated or another size than the entry says is never
// stored.
func (e *Engine) fetch(ctx context.Context, p model.Peer, sha256 string, size int64) error {
	return e.Remote.FetchBlob(ctx, p, sha256, func(r io.Reader) error { //nolint:wrapcheck // the remote names the peer and the blob
		up, err := e.Blobs.BeginUpload(ctx)
		if err != nil {
			return fmt.Errorf("begin blob %s: %w", sha256, err)
		}
		// One byte past the size is enough to tell that it is longer.
		n, err := up.Append(ctx, io.LimitReader(r, size+1))
		if err == nil && n != size {
			err = fmt.Errorf("%w: blob %s has %d bytes, not the %d its entry gives", errSize, sha256, n, size)
		}
		if err == nil {
			_, err = up.Commit(ctx, blob.Digests{SHA256: sha256})
		}
		if err != nil {
			return errors.Join(err, up.Abort(context.WithoutCancel(ctx)))
		}
		return nil
	})
}
