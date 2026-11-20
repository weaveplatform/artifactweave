package project

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"sync"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

// StorageAccounting binds actual artifacts deltas to the shared root metadata
// transaction. It grants no content authority. The composition supplies the
// canonical secret-free repository lookup and a Service with initialization
// and durable warning handoff. Construct it before exposing the shared backend.
type StorageAccounting struct {
	Service    Service
	Repository func(context.Context, string) (artifactsapi.ProjectRepository, error)
}

type storageAccountingKey struct{ owner *StorageAccounting }

type storageLifetime struct {
	project     string
	incarnation model.ProjectIncarnation
}

type storageChanges struct {
	mu             sync.Mutex
	accounting     *StorageAccounting
	entries        map[storageLifetime]*big.Int
	err            error
	sealed, closed bool
}

// Middleware takes the project guard before business/source/item/queue locks,
// then settles the whole operation before before-commit event finalizers.
// Explicit publication checks settle earlier, before stream/lease fences;
// subsequent content changes are refused and the root tail is a clean no-op.
func (a *StorageAccounting) Middleware(ctx context.Context, next func(context.Context) error) error {
	if a.Service.Store == nil || a.Service.StorageBytes == nil || a.Repository == nil {
		return fmt.Errorf("project storage composition unavailable: %w", kernel.ErrStore)
	}
	if err := a.Service.Store.Projects().Guard(ctx); err != nil {
		return fmt.Errorf("guard storage transaction: %w", err)
	}
	changes := &storageChanges{accounting: a, entries: make(map[storageLifetime]*big.Int)}
	defer func() {
		changes.mu.Lock()
		changes.closed = true
		changes.mu.Unlock()
	}()
	ctx = context.WithValue(ctx, storageAccountingKey{a}, changes)
	if err := tx.SetPublicationCheck(ctx, changes.settle); err != nil {
		return fmt.Errorf("bind storage publication settlement: %w", err)
	}
	if err := next(ctx); err != nil {
		return err
	}
	return changes.settle(ctx)
}

func (a *StorageAccounting) current(ctx context.Context) (*storageChanges, error) {
	changes, _ := ctx.Value(storageAccountingKey{a}).(*storageChanges)
	if changes == nil {
		return nil, fmt.Errorf("storage watch has no owning root transaction: %w", kernel.ErrStore)
	}
	return changes, nil
}

func (c *storageChanges) open() error {
	if c.closed || c.sealed {
		return fmt.Errorf("storage publication is already fenced: %w", kernel.ErrConflict)
	}
	return c.err
}

// entry initializes BEFORE any affected artifact row changes. Missing projects
// in trusted legacy metadata have no live quota lifetime; public repository
// administration separately requires a real project. Deleted/recreated keys
// get different entries, and ended lifetimes are discarded at settlement.
func (c *storageChanges) entry(ctx context.Context, key string) (*big.Int, error) {
	if key == "" {
		return nil, nil //nolint:nilnil // unassigned content has no project counter
	}
	s := c.accounting.Service
	ps := s.Store.Projects()
	authority, err := ps.Authority(ctx, key)
	if errors.Is(err, kernel.ErrNotFound) {
		return nil, nil //nolint:nilnil // no live project lifetime to charge
	}
	if err != nil {
		return nil, fmt.Errorf("read storage project lifetime: %w", err)
	}
	lifetime := storageLifetime{key, authority.Incarnation}
	if delta, exists := c.entries[lifetime]; exists {
		return delta, nil
	}
	state, initialized, err := ps.Storage(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("read initial project storage: %w", err)
	}
	if !initialized {
		state.Bytes, err = s.StorageBytes(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("initialize owned logical storage: %w", err)
		}
		if err := ps.SetStorage(ctx, key, state); err != nil {
			return nil, fmt.Errorf("save initial project storage: %w", err)
		}
	}
	delta := new(big.Int)
	c.entries[lifetime] = delta
	return delta, nil
}

// Content captures the current canonical owner before the actual row mutation.
func (a *StorageAccounting) Content(ctx context.Context, repository string) (artifactsapi.StorageChange, error) {
	c, err := a.current(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.open(); err != nil {
		return nil, err
	}
	owner, err := a.Repository(ctx, repository)
	if errors.Is(err, kernel.ErrNotFound) {
		return nil, nil //nolint:nilnil // orphan rows are charged when a repository claims them
	}
	if err != nil {
		c.err = fmt.Errorf("read storage repository owner: %w", err)
		return nil, c.err
	}
	delta, err := c.entry(ctx, owner.ProjectKey)
	if err != nil {
		c.err = err
		return nil, err
	}
	return func(_ context.Context, bytes int64) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		if err := c.open(); err != nil {
			return err
		}
		if delta != nil {
			delta.Add(delta, big.NewInt(bytes))
		}
		return nil
	}, nil
}

// Ownership captures both project lifetimes before configuration changes. The
// returned callback receives remaining repository bytes, after raw-cache
// invalidation has independently recorded its deltas on deletion.
func (a *StorageAccounting) Ownership(ctx context.Context, change artifactsapi.StorageOwnership) (artifactsapi.StorageChange, error) {
	c, err := a.current(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.open(); err != nil {
		return nil, err
	}
	previous, err := c.entry(ctx, change.PreviousProject)
	if err != nil {
		c.err = err
		return nil, err
	}
	next, err := c.entry(ctx, change.Project)
	if err != nil {
		c.err = err
		return nil, err
	}
	return func(_ context.Context, bytes int64) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		if err := c.open(); err != nil {
			return err
		}
		if bytes < 0 {
			c.err = fmt.Errorf("negative ownership transfer bytes: %w", kernel.ErrStore)
			return c.err
		}
		if previous != nil {
			previous.Sub(previous, big.NewInt(bytes))
		}
		if next != nil {
			next.Add(next, big.NewInt(bytes))
		}
		return nil
	}, nil
}

func (c *storageChanges) settle(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	if c.sealed {
		return nil
	}
	c.sealed = true
	keys := make([]storageLifetime, 0, len(c.entries))
	for key := range c.entries {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b storageLifetime) int {
		return strings.Compare(a.project+"/"+string(a.incarnation), b.project+"/"+string(b.incarnation))
	})
	for _, key := range keys {
		authority, err := c.accounting.Service.Store.Projects().Authority(ctx, key.project)
		if errors.Is(err, kernel.ErrNotFound) || err == nil && authority.Incarnation != key.incarnation {
			continue
		}
		if err != nil {
			c.err = fmt.Errorf("refresh storage project lifetime: %w", err)
			return c.err
		}
		delta := c.entries[key]
		if !delta.IsInt64() {
			c.err = fmt.Errorf("logical storage delta exceeds accounting range: %w", kernel.ErrStore)
			return c.err
		}
		if err := c.accounting.Service.ChangeStorage(ctx, key.project, delta.Int64(), nil); err != nil {
			c.err = err
			return err
		}
	}
	return nil
}
