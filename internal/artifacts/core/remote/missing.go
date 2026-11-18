package remote

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// missing handles an actual origin miss, not an arbitrary lookup failure. 7.x
// keeps an expired file that still exists in its cache, does not negative-cache
// that miss, and unexpires the retained file (RemoteRepoBase.getInfo).
func (p *Policy) missing(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	revision string,
	token model.RemoteCacheToken,
) (fillResult, error) {
	out := fillResult{key: token.Key}
	if fileRead(q) {
		// Read the representation that Begin observed, rather than the older
		// candidate used to form the upstream request. A concurrent replacement or
		// eviction must neither be overwritten nor acquire a false fresh clock.
		entry, err := p.ReadEntry(ctx, token.Key, permit)
		if err != nil && !errors.Is(err, kernel.ErrNotFound) {
			return out, retainedError(ctx, err)
		}
		if err == nil && entry.Blob.Checksums.SHA256 != "" {
			if entry.Token.Source != token.Source || entry.Token.Epoch != token.Epoch || entry.Token.Revision != token.Revision {
				return out, source.ErrSuperseded
			}
			if err = p.retainedFile(ctx, entry); err != nil {
				// An eviction may have released the last blob reference while
				// verification ran. Its fence wins over the resulting missing
				// copy; a stable entry's own storage failure remains a fault.
				if currentErr := p.retainedCurrent(ctx, q, permit, revision, token); currentErr != nil {
					return out, retainedError(ctx, currentErr)
				}
				return out, retainedError(ctx, err)
			}
			// RC-3 keeps these stricter HTTP rules. The file still exists, so its
			// refusal is not a missing-cache entry even when it cannot be served.
			if !parseCacheRules(entry.CacheControl).permitsStale() {
				return out, kernel.ErrNotFound
			}
			entry.NegativeUntil = time.Time{}
			out.entry, err = p.unexpireEntry(ctx, q, permit, revision, token, entry)
			if err != nil {
				return out, retainedError(ctx, err)
			}
			return out, nil
		}
	}
	if err := p.negative(ctx, q, permit, revision, token); err != nil {
		return out, err
	}
	return out, kernel.ErrNotFound
}

// retainedFile checks the cache's current item and blob without fetching or
// re-admitting bytes. Item-store verdicts retain their existing mapping; a
// missing blob under a stable entry or an actual store failure is our fault.
func (p *Policy) retainedFile(ctx context.Context, e model.RemoteCacheEntry) error {
	if e.ItemID > 0 {
		it, err := p.Store.Items().GetByID(ctx, e.ItemID)
		if err != nil {
			return fmt.Errorf("read retained item: %w", err)
		}
		if it.Actual.SHA256 != e.Blob.Checksums.SHA256 || it.Size != e.Blob.Size {
			err = fmt.Errorf("retained item differs from cache entry: %w", kernel.ErrStore)
			return err
		}
	}
	info, err := p.Blobs.Stat(ctx, e.Blob.Checksums.SHA256)
	if err != nil {
		return fmt.Errorf("stat retained blob: %w", err)
	}
	if info.Size != e.Blob.Size || info.Digests.SHA256 != e.Blob.Checksums.SHA256 {
		err = fmt.Errorf("retained blob differs from cache entry: %w", kernel.ErrStore)
		return err
	}
	return nil
}

func (p *Policy) retainedCurrent(
	ctx context.Context, q source.Request, permit source.Permit, revision string, token model.RemoteCacheToken,
) error {
	err := p.Store.Tx(ctx, func(tx context.Context) error {
		if err := p.Check(tx, q, permit, revision); err != nil {
			return err
		}
		return p.Store.RemoteCache().Check(tx, token)
	})
	if err != nil {
		return fmt.Errorf("check retained cache entry: %w", err)
	}
	return nil
}

func retainedError(ctx context.Context, err error) error {
	marked := item.MarkStorageFault(ctx, err, err)
	// A statement failure remains the store's fault even if its driver also
	// reports a timeout. An ended request retains the existing cancellation
	// mapping; neither case can fall back or poison the upstream circuit.
	if ctx.Err() == nil && errors.Is(err, kernel.ErrStore) {
		return fmt.Errorf("retain cached file: %w", marked)
	}
	return PolicyError(marked)
}
