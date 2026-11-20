package storekit

import (
	"context"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

// PrepareStorageContent captures accounting before a content row can change.
// Standalone module stores omit the cross-module watch and retain their own
// transaction contract; application compositions must install it.
func PrepareStorageContent(ctx context.Context, watch api.StorageWatch, repository string) (api.StorageChange, error) {
	if watch == nil {
		return nil, nil //nolint:nilnil // nil is the documented optional accounting change
	}
	change, err := watch.Content(ctx, repository)
	if err != nil {
		return nil, storageRefusal(ctx, fmt.Errorf("prepare logical content accounting: %w", err))
	}
	return change, nil
}

// PrepareStorageOwnership captures both current project lifetimes before a
// canonical assignment changes; a same-owner edit transfers no storage.
func PrepareStorageOwnership(ctx context.Context, watch api.StorageWatch, ownership api.StorageOwnership) (api.StorageChange, error) {
	if watch == nil || ownership.PreviousProject == ownership.Project {
		return nil, nil //nolint:nilnil // nil is the documented optional accounting change
	}
	change, err := watch.Ownership(ctx, ownership)
	if err != nil {
		return nil, storageRefusal(ctx, fmt.Errorf("prepare logical ownership accounting: %w", err))
	}
	return change, nil
}

// RecordStorageChange must complete before the mutation transaction can commit.
func RecordStorageChange(ctx context.Context, change api.StorageChange, bytes int64) error {
	if change == nil {
		return nil
	}
	if err := change(ctx, bytes); err != nil {
		return storageRefusal(ctx, fmt.Errorf("record logical storage change: %w", err))
	}
	return nil
}

// RecordRepositoryStorage records the remaining actual total at an ownership
// boundary. Deletion calls it after cache invalidation, so raw cache negatives
// already recorded by their own hooks are not subtracted twice.
func RecordRepositoryStorage(ctx context.Context, change api.StorageChange, usage api.StorageUsage, repository string) error {
	if change == nil {
		return nil
	}
	bytes, err := usage.RepositoryStorageBytes(ctx, repository)
	if err != nil {
		return storageRefusal(ctx, fmt.Errorf("read transferred repository bytes: %w", err))
	}
	return RecordStorageChange(ctx, change, bytes)
}

// A joined memory Tx deliberately permits the outer caller to handle its
// errors. Accounting refusal cannot use that escape: rows may already have
// changed. Bind the refusal to the owning finalizer queue, without acquiring
// any domain lock during finalization, so dropping the error still rolls back.
func storageRefusal(ctx context.Context, cause error) error {
	if err := tx.BeforeCommit(ctx, func(context.Context) error { return cause }); err != nil {
		return errors.Join(cause, fmt.Errorf("bind storage refusal to transaction: %w", err))
	}
	return cause
}

// LogicalItemBytes excludes root/folder metadata from logical content usage.
func LogicalItemBytes(item model.Item) (int64, error) {
	if item.Folder || item.IsRoot() {
		return 0, nil
	}
	if item.Size < 0 {
		return 0, fmt.Errorf("negative logical item bytes: %w", kernel.ErrStore)
	}
	return item.Size, nil
}

// RawStorageBytes excludes item-backed representations and empty tombstones.
// NegativeUntil alone does not discard a retained positive body.
func RawStorageBytes(entry model.RemoteCacheEntry) (int64, error) {
	if entry.ItemID != 0 || entry.Blob.Checksums.SHA256 == "" {
		return 0, nil
	}
	if entry.Blob.Size < 0 {
		return 0, fmt.Errorf("negative logical cache bytes: %w", kernel.ErrStore)
	}
	return entry.Blob.Size, nil
}

// WriteRawStorage observes the actual retained cache representation, including
// a positive body hidden by a later negative response. The adapters call this
// within their metadata transaction; standalone stores skip the extra read.
func WriteRawStorage(
	ctx context.Context,
	watch api.StorageWatch,
	entry model.RemoteCacheEntry,
	read func(context.Context, model.RemoteCacheKey) (model.RemoteCacheEntry, error),
	write func() error,
) error {
	if watch == nil {
		return write()
	}
	change, err := PrepareStorageContent(ctx, watch, entry.Token.Key.RepoKey)
	if err != nil {
		return err
	}
	old, err := read(ctx, entry.Token.Key)
	if err != nil && !errors.Is(err, kernel.ErrNotFound) {
		return fmt.Errorf("read previous logical cache body: %w", err)
	}
	previous, err := RawStorageBytes(old)
	if err != nil {
		return err
	}
	next, err := RawStorageBytes(entry)
	if err != nil {
		return err
	}
	if err := write(); err != nil {
		return err
	}
	return RecordStorageChange(ctx, change, next-previous)
}
