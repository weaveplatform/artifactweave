package storekit

import (
	"context"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

// CopyRecords supplies backend rows to the shared copy/reference transaction.
// Lock serializes writes with repository deletion, including the first copy.
type CopyRecords interface {
	Lock(context.Context, string) error
	Read(context.Context, string, string) (virtual.Copy, error)
	Write(context.Context, virtual.Copy) error
	List(context.Context, string) ([]virtual.Copy, error)
	Delete(context.Context, string) error
}

type virtualCopies struct {
	store   Stores
	records CopyRecords
}

// NewVirtualCopies gives both adapters the same atomic blob-reference rules.
func NewVirtualCopies(s Stores, r CopyRecords) virtual.CopyStore { return virtualCopies{s, r} }

func (s virtualCopies) Get(ctx context.Context, key, path string) (virtual.Copy, error) {
	c, err := s.records.Read(ctx, key, path)
	if err != nil {
		return c, fmt.Errorf("read virtual copy: %w", err)
	}
	return c, nil
}

func (s virtualCopies) Put(ctx context.Context, c virtual.Copy) error {
	if c.Virtual == "" || c.Path == "" || c.Member == "" || c.SourceSHA256 == "" || c.Item.Actual.SHA256 == "" || c.Item.Size < 0 {
		return fmt.Errorf("put virtual copy: %w", kernel.ErrInvalid)
	}
	return s.store.Tx(ctx, func(ctx context.Context) error { //nolint:wrapcheck // the transaction retains the contextual operation errors below
		if err := s.records.Lock(ctx, c.Virtual); err != nil {
			return fmt.Errorf("lock virtual copy: %w", err)
		}
		v, err := s.store.Repos().Get(ctx, c.Virtual)
		if err != nil {
			return fmt.Errorf("virtual copy repository: %w", err)
		}
		if v.Class != repo.Virtual {
			return fmt.Errorf("virtual copy repository: %w", kernel.ErrInvalid)
		}
		old, err := s.records.Read(ctx, c.Virtual, c.Path)
		if err != nil && !errors.Is(err, kernel.ErrNotFound) {
			return fmt.Errorf("old virtual copy: %w", err)
		}
		if old.Item.Actual.SHA256 != c.Item.Actual.SHA256 {
			if _, err = s.store.Blobs().Ref(ctx, blob.Blob{Checksums: c.Item.Actual, Size: c.Item.Size, Created: c.Item.Created}, 1); err != nil {
				return fmt.Errorf("reference virtual copy: %w", err)
			}
			if old.Item.Actual.SHA256 != "" {
				if _, err = s.store.Blobs().Unref(ctx, old.Item.Actual.SHA256, 1); err != nil {
					return fmt.Errorf("release virtual copy: %w", err)
				}
			}
		}
		if err = s.records.Write(ctx, c); err != nil {
			return fmt.Errorf("write virtual copy: %w", err)
		}
		return nil
	})
}

func (s virtualCopies) DeleteRepo(ctx context.Context, key string) error {
	return s.store.Tx(ctx, func(ctx context.Context) error { //nolint:wrapcheck // the transaction retains the contextual operation errors below
		if err := s.records.Lock(ctx, key); err != nil {
			return fmt.Errorf("lock virtual copies: %w", err)
		}
		copies, err := s.records.List(ctx, key)
		if err != nil {
			return fmt.Errorf("list virtual copies: %w", err)
		}
		for _, c := range copies {
			if _, err = s.store.Blobs().Unref(ctx, c.Item.Actual.SHA256, 1); err != nil {
				return fmt.Errorf("release virtual copies: %w", err)
			}
		}
		if err = s.records.Delete(ctx, key); err != nil {
			return fmt.Errorf("delete virtual copies: %w", err)
		}
		return nil
	})
}
