package storekit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

// Stores is what the remote cache and layout fencing need from the
// artifacts stores; every call joins the caller's unit of work.
type Stores interface {
	tx.Runner
	Blobs() port.BlobIndex
	Items() port.ItemStore
	Repos() port.RepoStore
}

// RemoteCacheRecords is the backend primitive used by the common cache contract.
// Source locks its repository lifetime row inside Tx. PutEntry must only be used
// through NewRemoteCache, which maintains CAS and raw blob references.
type RemoteCacheRecords interface {
	Source(context.Context, string) (model.RemoteSourceState, error)
	PutSource(context.Context, string, model.RemoteSourceState) error
	Entry(context.Context, model.RemoteCacheKey) (model.RemoteCacheEntry, error)
	PutEntry(context.Context, model.RemoteCacheEntry) error
	Exact(context.Context, string, string, kernel.Page) ([]model.RemoteCacheEntry, string, error)
	Entries(context.Context, string, string, kernel.Page) ([]model.RemoteCacheEntry, string, error)
}

type remoteCache struct {
	store   Stores
	records RemoteCacheRecords
}

// NewRemoteCache returns the remote cache over records: the one
// compare-and-swap and blob reference implementation both adapters share.
// Every call runs in a unit of work of s, joining the caller's transaction.
func NewRemoteCache(s Stores, records RemoteCacheRecords) port.RemoteCacheStore {
	return remoteCache{store: s, records: records}
}

func (c remoteCache) Source(ctx context.Context, key string) (model.SourceIdentity, error) {
	s, err := c.records.Source(ctx, key)
	return s.Source, err //nolint:wrapcheck // backend operation has context
}

func (c remoteCache) Begin(
	ctx context.Context,
	key model.RemoteCacheKey,
) (model.RemoteCacheToken, error) {
	var out model.RemoteCacheToken
	if err := port.ValidateRemoteKey(key); err != nil {
		return out, err //nolint:wrapcheck // the port's validation error says what is invalid
	}
	err := c.store.Tx(ctx, func(ctx context.Context) error {
		s, err := c.records.Source(ctx, key.RepoKey)
		if err != nil {
			return err //nolint:wrapcheck // the records name the operation
		}
		e, err := c.records.Entry(ctx, key)
		if errors.Is(err, kernel.ErrNotFound) {
			e.Token = model.RemoteCacheToken{Key: key, Source: s.Source, Epoch: 1}
			if err = c.records.PutEntry(ctx, e); err != nil {
				return err //nolint:wrapcheck // the records name the operation
			}
		} else if err != nil {
			return err //nolint:wrapcheck // the records name the operation
		}
		out = e.Token
		out.Source = s.Source
		out.PolicyRevision = s.PolicyRevision
		return nil
	})
	return out, err //nolint:wrapcheck // transaction owns errors
}

func (c remoteCache) Read(
	ctx context.Context,
	key model.RemoteCacheKey,
) (model.RemoteCacheEntry, error) {
	if err := port.ValidateRemoteKey(key); err != nil {
		//nolint:wrapcheck // the port's validation error says what is invalid
		return model.RemoteCacheEntry{}, err
	}
	e, err := c.records.Entry(ctx, key)
	if err == nil && e.ItemID != 0 {
		it, itemErr := c.store.Items().GetByID(ctx, e.ItemID)
		if itemErr != nil {
			return e, itemErr //nolint:wrapcheck // the item store names the operation
		}
		if it.Actual != e.Blob.Checksums {
			return e, kernel.ErrNotFound
		}
	}
	if err == nil && e.Blob.Checksums.SHA256 == "" && e.NegativeUntil.IsZero() {
		err = kernel.ErrNotFound
	}
	return e, err //nolint:wrapcheck // backend operation has context
}

func (c remoteCache) check(
	ctx context.Context,
	t model.RemoteCacheToken,
) (model.RemoteCacheEntry, error) {
	s, err := c.records.Source(ctx, t.Key.RepoKey)
	if errors.Is(err, kernel.ErrNotFound) {
		return model.RemoteCacheEntry{}, kernel.ErrConflict
	}
	if err != nil {
		return model.RemoteCacheEntry{}, err //nolint:wrapcheck // the records name the operation
	}
	if s.Source != t.Source || s.PolicyRevision != t.PolicyRevision {
		return model.RemoteCacheEntry{}, kernel.ErrConflict
	}
	e, err := c.records.Entry(ctx, t.Key)
	if errors.Is(err, kernel.ErrNotFound) {
		return e, kernel.ErrConflict
	}
	if err != nil {
		return e, err //nolint:wrapcheck // the records name the operation
	}
	if e.Token.Epoch != t.Epoch || e.Token.Revision != t.Revision {
		return e, kernel.ErrConflict
	}
	return e, nil
}

func (c remoteCache) Check(ctx context.Context, t model.RemoteCacheToken) error {
	//nolint:wrapcheck // Tx returns fn's error
	return c.store.Tx(
		ctx,
		func(ctx context.Context) error { _, err := c.check(ctx, t); return err },
	)
}

func (c remoteCache) Put(
	ctx context.Context,
	t model.RemoteCacheToken,
	e model.RemoteCacheEntry,
) (model.RemoteCacheEntry, error) {
	var out model.RemoteCacheEntry
	if err := port.ValidateRemoteEntry(e); err != nil {
		return out, err //nolint:wrapcheck // the port's validation error says what is invalid
	}
	err := c.store.Tx(ctx, func(ctx context.Context) error {
		old, err := c.check(ctx, t)
		if err != nil {
			return err
		}
		if e.ItemID != 0 {
			it, err := c.store.Items().GetByID(ctx, e.ItemID)
			if err != nil {
				return err //nolint:wrapcheck // the item store names the operation
			}
			if !it.Cached || it.RepoKey != t.Key.RepoKey || it.FullPath() != t.Key.Path ||
				it.Actual != e.Blob.Checksums {
				return kernel.ErrInvalid
			}
		}
		if err = c.references(ctx, old, e); err != nil {
			return err
		}
		// A fetch can start before a retained representation is served. Publishing
		// its content must not overwrite the newer successful-use timestamp.
		if old.LastUsed.After(e.LastUsed) {
			e.LastUsed = old.LastUsed
		}
		e.Token = t
		e.Token.Revision++
		if err = c.records.PutEntry(ctx, e); err != nil {
			return err //nolint:wrapcheck // the records name the operation
		}
		out = kernel.Normalise(e)
		return nil
	})
	return out, err //nolint:wrapcheck // transaction owns errors
}

func (c remoteCache) references(ctx context.Context, old, next model.RemoteCacheEntry) error {
	if next.ItemID == 0 && next.Blob.Checksums.SHA256 != "" {
		if _, err := c.store.Blobs().Ref(ctx, next.Blob, 1); err != nil {
			return err //nolint:wrapcheck // the blob index names the operation
		}
	}
	if old.ItemID == 0 && old.Blob.Checksums.SHA256 != "" {
		if _, err := c.store.Blobs().Unref(ctx, old.Blob.Checksums.SHA256, 1); err != nil {
			return err //nolint:wrapcheck // the blob index names the operation
		}
	}
	return nil
}

func (c remoteCache) change(
	ctx context.Context,
	t model.RemoteCacheToken,
	fn func(*model.RemoteCacheEntry) error,
) error {
	//nolint:wrapcheck // Tx returns fn's error
	return c.store.Tx(ctx, func(ctx context.Context) error {
		e, err := c.check(ctx, t)
		if err != nil {
			return err
		}
		if e.Token.Source != t.Source {
			e = model.RemoteCacheEntry{}
		}
		if err = fn(&e); err != nil {
			return err
		}
		_, err = c.Put(ctx, t, e)
		return err
	})
}

func (c remoteCache) Negative(
	ctx context.Context,
	t model.RemoteCacheToken,
	until time.Time,
) error {
	return c.change(
		ctx,
		t,
		func(e *model.RemoteCacheEntry) error { e.NegativeUntil = until; return nil },
	)
}

func (c remoteCache) Revalidate(ctx context.Context, t model.RemoteCacheToken, at time.Time) error {
	return c.change(ctx, t, func(e *model.RemoteCacheEntry) error {
		if e.Blob.Checksums.SHA256 == "" {
			return kernel.ErrInvalid
		}
		e.ValidatedAt = at
		e.NegativeUntil = time.Time{}
		return nil
	})
}

func (c remoteCache) Touch(ctx context.Context, t model.RemoteCacheToken, at time.Time) error {
	//nolint:wrapcheck // Tx returns fn's error
	return c.store.Tx(ctx, func(ctx context.Context) error {
		// check holds the repository/source lifetime lock. Usage is serialized
		// with publication but is not a content mutation and must not fence a fetch.
		e, err := c.check(ctx, t)
		if err != nil {
			return err
		}
		if e.Token.Source != t.Source {
			return kernel.ErrConflict
		}
		if e.Blob.Checksums.SHA256 == "" {
			return kernel.ErrNotFound
		}
		if !at.After(e.LastUsed) {
			return nil
		}
		e.LastUsed = at
		return c.records.PutEntry(ctx, e)
	})
}

func (c remoteCache) Evict(ctx context.Context, t model.RemoteCacheToken) error {
	//nolint:wrapcheck // Tx returns fn's error
	return c.store.Tx(ctx, func(ctx context.Context) error {
		e, err := c.check(ctx, t)
		if err != nil {
			return err
		}
		_, err = c.invalidateEntry(ctx, e, false)
		return err
	})
}

func (c remoteCache) List(
	ctx context.Context,
	key, prefix string,
	p kernel.Page,
) ([]model.RemoteCacheEntry, string, error) {
	//nolint:wrapcheck // the records name the operation
	return c.records.Entries(ctx, key, strings.Trim(prefix, "/"), p)
}

func (c remoteCache) Exact(
	ctx context.Context,
	key, path string,
	p kernel.Page,
) ([]model.RemoteCacheEntry, string, error) {
	if err := port.ValidateRemoteKey(model.RemoteCacheKey{RepoKey: key, Path: path}); err != nil {
		return nil, "", err //nolint:wrapcheck // the port's validation error says what is invalid
	}
	return c.records.Exact(ctx, key, path, p) //nolint:wrapcheck // the records name the operation
}

func (c remoteCache) Invalidate(
	ctx context.Context,
	key, prefix string,
	metadataOnly bool,
) (int64, error) {
	prefix = strings.Trim(prefix, "/")
	if prefix != "" {
		if err := port.ValidateRemoteKey(
			model.RemoteCacheKey{RepoKey: key, Path: prefix},
		); err != nil {
			return 0, err //nolint:wrapcheck // the port's validation error says what is invalid
		}
	}
	var n int64
	err := c.store.Tx(ctx, func(ctx context.Context) error {
		if _, err := c.records.Source(ctx, key); err != nil {
			return err //nolint:wrapcheck // the records name the operation
		}
		page := kernel.Page{Limit: 128}
		for {
			entries, next, err := c.records.Entries(ctx, key, prefix, page)
			if err != nil {
				return err //nolint:wrapcheck // the records name the operation
			}
			for _, e := range entries {
				changed, err := c.invalidateEntry(ctx, e, metadataOnly)
				if err != nil {
					return err
				}
				if changed {
					n++
				}
			}
			if next == "" {
				return nil
			}
			page.After = next
		}
	})
	return n, err //nolint:wrapcheck // transaction owns errors
}

func (c remoteCache) invalidateEntry(
	ctx context.Context,
	e model.RemoteCacheEntry,
	metadataOnly bool,
) (bool, error) {
	if metadataOnly && e.Kind != model.RemoteMetadata && e.Kind != "" && e.NegativeUntil.IsZero() &&
		e.Smart == nil {
		return false, nil
	}
	old := e
	e.Token.Epoch++
	e.Token.Revision++
	e.NegativeUntil = time.Time{}
	if metadataOnly {
		if e.Smart != nil {
			e.Smart.PropertiesAt, e.Smart.OriginAt = time.Time{}, time.Time{}
		}
		if e.Kind == model.RemoteMetadata {
			e.ValidatedAt = time.Time{}
		}
	} else {
		e = model.RemoteCacheEntry{Token: e.Token}
	}
	if err := c.references(ctx, old, e); err != nil {
		return false, err
	}
	if err := c.records.PutEntry(ctx, e); err != nil {
		return false, err //nolint:wrapcheck // the records name the operation
	}
	return true, nil
}

func (c remoteCache) State(ctx context.Context, key string) (model.RemoteSourceState, error) {
	return c.records.Source(ctx, key) //nolint:wrapcheck // the records name the operation
}

func (c remoteCache) PutState(ctx context.Context, key string, s model.RemoteSourceState) error {
	if s.ConnectionFailures < 0 {
		return fmt.Errorf("remote connection failures: %w", kernel.ErrInvalid)
	}
	//nolint:wrapcheck // Tx returns fn's error
	return c.store.Tx(ctx, func(ctx context.Context) error {
		old, err := c.records.Source(ctx, key)
		if err != nil {
			return err //nolint:wrapcheck // the records name the operation
		}
		if old.Source != s.Source || old.Revision != s.Revision ||
			old.PolicyRevision != s.PolicyRevision {
			return kernel.ErrConflict
		}
		s.Revision++
		return c.records.PutSource(ctx, key, s)
	})
}
