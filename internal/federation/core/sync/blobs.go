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

// Blob serves the peer API's GET blobs/{sha256}: the content, when a
// stream this instance sends caller refers to it. ErrNoStream for content
// no such stream refers to, or that this instance does not hold, so the
// answer never says which content exists here; kernel.ErrInvalid for a
// sha256 that is not 64 lowercase hex digits.
func (e *Engine) Blob(ctx context.Context, caller model.Peer, sha256 string) (io.ReadSeekCloser, blob.Info, error) {
	if !sha256Hex.MatchString(sha256) {
		return nil, blob.Info{}, fmt.Errorf("sha256 must be 64 lowercase hex digits: %w", kernel.ErrInvalid)
	}
	ok, err := e.referenced(ctx, caller, sha256)
	if err != nil || !ok {
		return nil, blob.Info{}, errors.Join(err, ErrNoStream)
	}
	r, info, err := e.Blobs.Open(ctx, sha256)
	if errors.Is(err, blob.ErrNotFound) {
		return nil, blob.Info{}, ErrNoStream
	}
	if err != nil {
		return nil, blob.Info{}, fmt.Errorf("open blob %s: %w", sha256, err)
	}
	return r, info, nil
}

// referenced reports whether a stream this instance sends caller refers to
// the blob.
func (e *Engine) referenced(ctx context.Context, caller model.Peer, sha256 string) (bool, error) {
	for _, k := range e.Kinds.Kinds() {
		if !holds(caller, k, Send) {
			continue
		}
		streams, err := k.Streams(ctx)
		if err != nil {
			return false, fmt.Errorf("%s streams: %w", k.Name(), err)
		}
		for _, stream := range streams {
			if _, err := e.authorize(ctx, caller, stream, Send); errors.Is(err, ErrNoStream) {
				continue
			} else if err != nil {
				return false, err
			}
			ok, err := k.References(ctx, stream, sha256)
			if err != nil {
				return false, fmt.Errorf("%s references: %w", stream, err)
			}
			if ok {
				return true, nil
			}
		}
	}
	return false, nil
}

// RetryFetch fetches a blob whose fetch failed while stream was applied, from
// any subscriber of the stream that serves it, and publishes the items
// waiting for it, which a later entry may have deleted or replaced
// meanwhile (FetchKind; FED-06's replay calls it too). A failure is
// recorded on the failed binary and returned, so the queue retries with
// back-off. A blob with no failed binary on stream, or a stream of no
// registered kind, has nothing to do.
func (e *Engine) RetryFetch(ctx context.Context, stream, sha256 string) error {
	k, ok := e.Kinds.For(stream)
	if !ok {
		return nil
	}
	failed, err := e.Store.Outbox().FailedFetches(ctx, stream)
	if err != nil {
		return fmt.Errorf("failed fetches of %s: %w", stream, err)
	}
	if !hasFailure(failed, sha256) {
		return nil
	}
	size, waiting, err := k.Pending(ctx, stream, sha256)
	if err != nil {
		return fmt.Errorf("items of %s pending %s: %w", stream, sha256, err)
	}
	if !waiting {
		// Claim before checking again: a newer event can have started
		// waiting since Pending returned. Its failure record's insert or
		// update serialises behind this delete on PostgreSQL. If it is
		// pending now, roll the claim back and let the queue retry.
		return e.Store.Tx(ctx, func(ctx context.Context) error { //nolint:wrapcheck // the callback names its failures
			if err := e.Store.Outbox().ClearFailedFetch(ctx, stream, sha256); errors.Is(err, kernel.ErrNotFound) {
				return nil
			} else if err != nil {
				return fmt.Errorf("claim obsolete failed fetch: %w", err)
			}
			_, pending, err := k.Pending(ctx, stream, sha256)
			if err != nil {
				return fmt.Errorf("recheck pending content: %w", err)
			}
			if pending {
				return fmt.Errorf("pending content changed while clearing failed fetch: %w", kernel.ErrConflict)
			}
			return nil
		})
	}
	subs, err := k.Subscribers(ctx, stream)
	if err != nil {
		return fmt.Errorf("stream %s subscribers: %w", stream, err)
	}
	var from []model.Peer
	for _, id := range subs {
		p, ok, err := e.peer(ctx, id)
		if err != nil {
			return err
		}
		if ok && holds(p, k, Receive) {
			from = append(from, p)
		}
	}
	// The item's entry gave the size: a retry holds the content to it as
	// the first fetch did, so content of another size never publishes.
	held, reason, err := e.ensureBlob(ctx, from, sha256, size)
	if err != nil {
		return err
	}
	if !held {
		if err := e.Store.Outbox().RetryFailedFetch(ctx, stream, sha256, reason); errors.Is(err, kernel.ErrNotFound) {
			return nil
		} else if err != nil {
			return fmt.Errorf("record failed retry of %s: %w", sha256, err)
		}
		return fmt.Errorf("fetch blob %s for %s: %s: %w", sha256, stream, reason, ErrFetchUnavailable)
	}
	return e.Store.Tx(ctx, func(ctx context.Context) error { //nolint:wrapcheck // the callback names its failures
		// Content is verified by digest, so any still-authorized member
		// can supply it. A peer snapshot taken before the network call
		// cannot grant publication after every source has been revoked.
		allowed := false
		for _, p := range from {
			if _, err := e.regrant(ctx, p.InstanceID, stream, Receive); errors.Is(err, ErrRefused) || errors.Is(err, ErrNoStream) {
				continue
			} else if err != nil {
				return err
			}
			allowed = true
			break
		}
		if !allowed {
			return errors.Join(ErrFetchUnavailable, ErrRefused)
		}
		// Claim the record before publishing. DELETE serialises concurrent
		// retries and operator deletion on PostgreSQL too; if deletion won
		// while the fetch was in flight, the item must stay absent. A failed
		// publication rolls the claim back with its item changes.
		if err := e.Store.Outbox().ClearFailedFetch(ctx, stream, sha256); errors.Is(err, kernel.ErrNotFound) {
			return nil
		} else if err != nil {
			return fmt.Errorf("claim failed fetch of %s: %w", sha256, err)
		}
		if err := k.Publish(ctx, stream, sha256); err != nil {
			return fmt.Errorf("publish %s on %s: %w", sha256, stream, err)
		}
		return nil
	})
}

func hasFailure(failed []outbox.FailedBlobFetch, sha256 string) bool {
	for _, f := range failed {
		if f.SHA256 == sha256 {
			return true
		}
	}
	return false
}
