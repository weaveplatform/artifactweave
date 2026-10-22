package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// RemoteCacheRecords is the backend primitive used by the common cache contract.
// Source locks its repository lifetime row inside Tx. PutEntry must only be used
// through NewRemoteCache, which maintains CAS and raw blob references.
type RemoteCacheRecords interface {
	Source(context.Context, string) (types.RemoteSourceState, error)
	PutSource(context.Context, string, types.RemoteSourceState) error
	Entry(context.Context, types.RemoteCacheKey) (types.RemoteCacheEntry, error)
	PutEntry(context.Context, types.RemoteCacheEntry) error
	Exact(context.Context, string, string, Page) ([]types.RemoteCacheEntry, string, error)
	Entries(context.Context, string, string, Page) ([]types.RemoteCacheEntry, string, error)
}

type remoteCache struct {
	store   Store
	records RemoteCacheRecords
}

// NewRemoteCache shares one CAS/reference implementation across all backends.
func NewRemoteCache(s Store, records RemoteCacheRecords) RemoteCacheStore {
	return remoteCache{store: s, records: records}
}

func (c remoteCache) Source(ctx context.Context, key string) (types.SourceIdentity, error) {
	s, err := c.records.Source(ctx, key)
	return s.Source, err //nolint:wrapcheck // backend operation has context
}

func (c remoteCache) Begin(
	ctx context.Context,
	key types.RemoteCacheKey,
) (types.RemoteCacheToken, error) {
	var out types.RemoteCacheToken
	if err := ValidateRemoteKey(key); err != nil {
		return out, err
	}
	err := c.store.Tx(ctx, func(ctx context.Context) error {
		s, err := c.records.Source(ctx, key.RepoKey)
		if err != nil {
			return err
		}
		e, err := c.records.Entry(ctx, key)
		if errors.Is(err, ErrNotFound) {
			e.Token = types.RemoteCacheToken{Key: key, Source: s.Source, Epoch: 1}
			if err = c.records.PutEntry(ctx, e); err != nil {
				return err
			}
		} else if err != nil {
			return err
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
	key types.RemoteCacheKey,
) (types.RemoteCacheEntry, error) {
	if err := ValidateRemoteKey(key); err != nil {
		return types.RemoteCacheEntry{}, err
	}
	e, err := c.records.Entry(ctx, key)
	if err == nil && e.ItemID != 0 {
		it, itemErr := c.store.Items().GetByID(ctx, e.ItemID)
		if itemErr != nil {
			return e, itemErr
		}
		if it.Actual != e.Blob.Checksums {
			return e, ErrNotFound
		}
	}
	if err == nil && e.Blob.Checksums.SHA256 == "" && e.NegativeUntil.IsZero() {
		err = ErrNotFound
	}
	return e, err //nolint:wrapcheck // backend operation has context
}

func (c remoteCache) check(
	ctx context.Context,
	t types.RemoteCacheToken,
) (types.RemoteCacheEntry, error) {
	s, err := c.records.Source(ctx, t.Key.RepoKey)
	if errors.Is(err, ErrNotFound) {
		return types.RemoteCacheEntry{}, ErrConflict
	}
	if err != nil {
		return types.RemoteCacheEntry{}, err
	}
	if s.Source != t.Source || s.PolicyRevision != t.PolicyRevision {
		return types.RemoteCacheEntry{}, ErrConflict
	}
	e, err := c.records.Entry(ctx, t.Key)
	if errors.Is(err, ErrNotFound) {
		return e, ErrConflict
	}
	if err != nil {
		return e, err
	}
	if e.Token.Epoch != t.Epoch || e.Token.Revision != t.Revision {
		return e, ErrConflict
	}
	return e, nil
}

func (c remoteCache) Check(ctx context.Context, t types.RemoteCacheToken) error {
	return c.store.Tx(
		ctx,
		func(ctx context.Context) error { _, err := c.check(ctx, t); return err },
	)
}

func (c remoteCache) Put(
	ctx context.Context,
	t types.RemoteCacheToken,
	e types.RemoteCacheEntry,
) (types.RemoteCacheEntry, error) {
	var out types.RemoteCacheEntry
	if err := ValidateRemoteEntry(e); err != nil {
		return out, err
	}
	err := c.store.Tx(ctx, func(ctx context.Context) error {
		old, err := c.check(ctx, t)
		if err != nil {
			return err
		}
		if e.ItemID != 0 {
			it, err := c.store.Items().GetByID(ctx, e.ItemID)
			if err != nil {
				return err
			}
			if !it.Cached || it.RepoKey != t.Key.RepoKey || it.FullPath() != t.Key.Path ||
				it.Actual != e.Blob.Checksums {
				return ErrInvalid
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
			return err
		}
		out = Normalise(e)
		return nil
	})
	return out, err //nolint:wrapcheck // transaction owns errors
}

func (c remoteCache) references(ctx context.Context, old, next types.RemoteCacheEntry) error {
	if next.ItemID == 0 && next.Blob.Checksums.SHA256 != "" {
		if _, err := c.store.Blobs().Ref(ctx, next.Blob, 1); err != nil {
			return err
		}
	}
	if old.ItemID == 0 && old.Blob.Checksums.SHA256 != "" {
		if _, err := c.store.Blobs().Unref(ctx, old.Blob.Checksums.SHA256, 1); err != nil {
			return err
		}
	}
	return nil
}

func (c remoteCache) change(
	ctx context.Context,
	t types.RemoteCacheToken,
	fn func(*types.RemoteCacheEntry) error,
) error {
	return c.store.Tx(ctx, func(ctx context.Context) error {
		e, err := c.check(ctx, t)
		if err != nil {
			return err
		}
		if e.Token.Source != t.Source {
			e = types.RemoteCacheEntry{}
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
	t types.RemoteCacheToken,
	until time.Time,
) error {
	return c.change(
		ctx,
		t,
		func(e *types.RemoteCacheEntry) error { e.NegativeUntil = until; return nil },
	)
}

func (c remoteCache) Revalidate(ctx context.Context, t types.RemoteCacheToken, at time.Time) error {
	return c.change(ctx, t, func(e *types.RemoteCacheEntry) error {
		if e.Blob.Checksums.SHA256 == "" {
			return ErrInvalid
		}
		e.ValidatedAt = at
		e.NegativeUntil = time.Time{}
		return nil
	})
}

func (c remoteCache) Touch(ctx context.Context, t types.RemoteCacheToken, at time.Time) error {
	return c.store.Tx(ctx, func(ctx context.Context) error {
		// check holds the repository/source lifetime lock. Usage is serialized
		// with publication but is not a content mutation and must not fence a fetch.
		e, err := c.check(ctx, t)
		if err != nil {
			return err
		}
		if e.Token.Source != t.Source {
			return ErrConflict
		}
		if e.Blob.Checksums.SHA256 == "" {
			return ErrNotFound
		}
		if !at.After(e.LastUsed) {
			return nil
		}
		e.LastUsed = at
		return c.records.PutEntry(ctx, e)
	})
}

func (c remoteCache) Evict(ctx context.Context, t types.RemoteCacheToken) error {
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
	p Page,
) ([]types.RemoteCacheEntry, string, error) {
	return c.records.Entries(ctx, key, strings.Trim(prefix, "/"), p)
}

func (c remoteCache) Exact(
	ctx context.Context,
	key, path string,
	p Page,
) ([]types.RemoteCacheEntry, string, error) {
	if err := ValidateRemoteKey(types.RemoteCacheKey{RepoKey: key, Path: path}); err != nil {
		return nil, "", err
	}
	return c.records.Exact(ctx, key, path, p)
}

func (c remoteCache) Invalidate(
	ctx context.Context,
	key, prefix string,
	metadataOnly bool,
) (int64, error) {
	prefix = strings.Trim(prefix, "/")
	if prefix != "" {
		if err := ValidateRemoteKey(types.RemoteCacheKey{RepoKey: key, Path: prefix}); err != nil {
			return 0, err
		}
	}
	var n int64
	err := c.store.Tx(ctx, func(ctx context.Context) error {
		if _, err := c.records.Source(ctx, key); err != nil {
			return err
		}
		page := Page{Limit: 128}
		for {
			entries, next, err := c.records.Entries(ctx, key, prefix, page)
			if err != nil {
				return err
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
	e types.RemoteCacheEntry,
	metadataOnly bool,
) (bool, error) {
	if metadataOnly && e.Kind != types.RemoteMetadata && e.Kind != "" && e.NegativeUntil.IsZero() &&
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
		if e.Kind == types.RemoteMetadata {
			e.ValidatedAt = time.Time{}
		}
	} else {
		e = types.RemoteCacheEntry{Token: e.Token}
	}
	if err := c.references(ctx, old, e); err != nil {
		return false, err
	}
	if err := c.records.PutEntry(ctx, e); err != nil {
		return false, err
	}
	return true, nil
}

func (c remoteCache) State(ctx context.Context, key string) (types.RemoteSourceState, error) {
	return c.records.Source(ctx, key)
}

func (c remoteCache) PutState(ctx context.Context, key string, s types.RemoteSourceState) error {
	if s.ConnectionFailures < 0 {
		return fmt.Errorf("remote connection failures: %w", ErrInvalid)
	}
	return c.store.Tx(ctx, func(ctx context.Context) error {
		old, err := c.records.Source(ctx, key)
		if err != nil {
			return err
		}
		if old.Source != s.Source || old.Revision != s.Revision ||
			old.PolicyRevision != s.PolicyRevision {
			return ErrConflict
		}
		s.Revision++
		return c.records.PutSource(ctx, key, s)
	})
}
