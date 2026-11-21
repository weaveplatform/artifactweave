package inmem

import (
	"cmp"
	"context"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

type cleanupEntryKey struct {
	run  int64
	kind model.CleanupEntryKind
	id   int64
}

func (c cleanupStore) progress() *memtx.Table[int64, model.CleanupProgress] {
	return memtx.TableOf[int64, model.CleanupProgress](c.s.Store, "cleanup_progress")
}

func (c cleanupStore) entries() *memtx.Table[cleanupEntryKey, model.CleanupEntry] {
	return memtx.TableOf[cleanupEntryKey, model.CleanupEntry](c.s.Store, "cleanup_entries")
}

func (c cleanupStore) transaction(ctx context.Context) error {
	if !c.s.InTx(ctx) {
		return fmt.Errorf("cleanup progress requires a transaction: %w", kernel.ErrInvalid)
	}
	return nil
}

func (c cleanupStore) CreateProgress(ctx context.Context, p model.CleanupProgress) error {
	if err := c.transaction(ctx); err != nil {
		return err
	}
	if err := p.Validate(); err != nil {
		return fmt.Errorf("validate cleanup continuation: %w", err)
	}
	return c.s.Do(ctx, func() error {
		if !c.runs().Has(p.RunID) {
			return memtx.NotFound("cleanup run", p.RunID)
		}
		if c.progress().Has(p.RunID) {
			return memtx.Conflict("cleanup progress", p.RunID)
		}
		c.progress().Put(p.RunID, p)
		return nil
	})
}

func (c cleanupStore) Progress(ctx context.Context, id int64) (model.CleanupProgress, error) {
	var out model.CleanupProgress
	err := c.s.Do(ctx, func() error {
		var ok bool
		out, ok = c.progress().Get(id)
		if !ok {
			return memtx.NotFound("cleanup progress", id)
		}
		return nil
	})
	return out, err
}

func (c cleanupStore) AdvanceProgress(ctx context.Context, expected int64, p model.CleanupProgress) error {
	if err := c.transaction(ctx); err != nil {
		return err
	}
	return c.s.Do(ctx, func() error {
		old, ok := c.progress().Get(p.RunID)
		if !ok || old.Revision != expected {
			return memtx.Conflict("cleanup progress", p.RunID)
		}
		if err := model.ValidateCleanupProgressAdvance(old, p); err != nil {
			return fmt.Errorf("advance cleanup continuation: %w", err)
		}
		c.progress().Put(p.RunID, p)
		return nil
	})
}

func entryKey(e model.CleanupEntry) cleanupEntryKey {
	return cleanupEntryKey{e.RunID, e.Kind, e.EntityID}
}

func (c cleanupStore) CreateEntry(ctx context.Context, e model.CleanupEntry) (bool, error) {
	if err := c.transaction(ctx); err != nil {
		return false, err
	}
	e = kernel.Normalise(e)
	if err := e.Validate(); err != nil {
		return false, fmt.Errorf("validate cleanup intent: %w", err)
	}
	created := false
	err := c.s.Do(ctx, func() error {
		progress, ok := c.progress().Get(e.RunID)
		if !ok {
			return memtx.NotFound("cleanup progress", e.RunID)
		}
		if progress.Phase == model.CleanupFinished {
			return memtx.Conflict("finished cleanup progress", e.RunID)
		}
		if old, ok := c.entries().Get(entryKey(e)); ok {
			if !model.SameCleanupIntent(old, e) {
				return memtx.Conflict("cleanup intent", e.EntityID)
			}
			return nil
		}
		c.entries().Put(entryKey(e), e)
		created = true
		return nil
	})
	return created, err
}

func (c cleanupStore) Entry(ctx context.Context, run int64, kind model.CleanupEntryKind, id int64) (model.CleanupEntry, error) {
	var out model.CleanupEntry
	err := c.s.Do(ctx, func() error {
		var ok bool
		out, ok = c.entries().Get(cleanupEntryKey{run, kind, id})
		if !ok {
			return memtx.NotFound("cleanup entry", id)
		}
		return nil
	})
	return out, err
}

func (c cleanupStore) AdvanceEntry(ctx context.Context, expected int64, e model.CleanupEntry) error {
	if err := c.transaction(ctx); err != nil {
		return err
	}
	e = kernel.Normalise(e)
	return c.s.Do(ctx, func() error {
		progress, ok := c.progress().Get(e.RunID)
		if !ok {
			return memtx.NotFound("cleanup progress", e.RunID)
		}
		if progress.Phase == model.CleanupFinished {
			return memtx.Conflict("finished cleanup progress", e.RunID)
		}
		old, ok := c.entries().Get(entryKey(e))
		if !ok || old.Revision != expected {
			return memtx.Conflict("cleanup entry", e.EntityID)
		}
		if err := model.ValidateCleanupEntryAdvance(old, e); err != nil {
			return fmt.Errorf("advance cleanup outcome: %w", err)
		}
		c.entries().Put(entryKey(e), e)
		return nil
	})
}

func (c cleanupStore) Entries(ctx context.Context, q port.CleanupEntryQuery, page kernel.Page) ([]model.CleanupEntry, string, error) {
	if q.RunID <= 0 {
		return nil, "", fmt.Errorf("cleanup entry run is required: %w", kernel.ErrInvalid)
	}
	var out []model.CleanupEntry
	var next string
	err := c.s.Do(ctx, func() error {
		rows := c.entries().All(func(_ cleanupEntryKey, e model.CleanupEntry) bool {
			return e.RunID == q.RunID && (q.VersionID == 0 || e.VersionID == q.VersionID) && (q.Kind == "" || e.Kind == q.Kind) && (q.State == "" || e.State == q.State)
		}, func(a, b model.CleanupEntry) int {
			if n := strings.Compare(string(a.Kind), string(b.Kind)); n != 0 {
				return n
			}
			return cmp.Compare(a.EntityID, b.EntityID)
		})
		var err error
		out, next, err = memtx.Paginate(rows, page, false, func(e model.CleanupEntry) []any { return []any{string(e.Kind), e.EntityID} })
		return err //nolint:wrapcheck // the shared paginator names its invalid cursor
	})
	return out, next, err
}

func (c cleanupStore) PendingEntry(ctx context.Context, run, version int64) (model.CleanupEntry, error) {
	var out model.CleanupEntry
	err := c.s.Do(ctx, func() error {
		found := false
		for _, entry := range c.entries().Rows {
			if entry.RunID != run || entry.VersionID != version || entry.Kind != model.CleanupItemEntry || entry.State != model.CleanupPending {
				continue
			}
			if !found || pendingCleanupOrder(entry, out) < 0 {
				out, found = entry, true
			}
		}
		if !found {
			return memtx.NotFound("pending cleanup entry", version)
		}
		out = kernel.Clone(out)
		return nil
	})
	return out, err
}

func pendingCleanupOrder(a, b model.CleanupEntry) int {
	al, bl := a.Snapshot.Lead, b.Snapshot.Lead
	af, bf := al != nil && al.Found, bl != nil && bl.Found
	if af != bf {
		if af {
			return 1
		}
		return -1
	}
	if af {
		if n := cmp.Compare(al.Rank, bl.Rank); n != 0 {
			return n
		}
		if n := strings.Compare(al.TieBreak, bl.TieBreak); n != 0 {
			return n
		}
	}
	return cmp.Compare(a.EntityID, b.EntityID)
}
