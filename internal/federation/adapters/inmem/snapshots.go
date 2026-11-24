package inmem

import (
	"context"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// The snapshot tables have the SQL tables' names.
const (
	tSnapshots       = "stream_snapshots"
	tSnapshotEntries = "stream_snapshot_entries"
)

// Snapshots returns the stream snapshots this instance has taken for its
// receivers (decision 0021, Full sync).
func (s *Store) Snapshots() port.SnapshotStore { return snapshotStore{s} }

type snapshotStore struct{ s *Store }

type snapshotEntryKey struct {
	id  int64
	key string
}

func (n snapshotStore) snapshots() *memtx.Table[int64, model.StreamSnapshot] {
	return memtx.TableOf[int64, model.StreamSnapshot](n.s.Store, tSnapshots)
}

func (n snapshotStore) entries() *memtx.Table[snapshotEntryKey, model.SnapshotEntry] {
	return memtx.TableOf[snapshotEntryKey, model.SnapshotEntry](n.s.Store, tSnapshotEntries)
}

// drop removes the snapshots matching match and their entries.
func (n snapshotStore) drop(match func(model.StreamSnapshot) bool) int64 {
	gone := map[int64]bool{}
	dropped := n.snapshots().DeleteWhere(func(id int64, v model.StreamSnapshot) bool {
		if match(v) {
			gone[id] = true
			return true
		}
		return false
	})
	n.entries().DeleteWhere(func(k snapshotEntryKey, _ model.SnapshotEntry) bool { return gone[k.id] })
	return dropped
}

func (n snapshotStore) CreateSnapshot(ctx context.Context, sn model.StreamSnapshot) (model.StreamSnapshot, error) {
	err := n.s.Do(ctx, func() error {
		n.drop(func(v model.StreamSnapshot) bool { return v.Receiver == sn.Receiver && v.Stream == sn.Stream })
		sn.ID = n.s.NextID(tSnapshots)
		n.snapshots().Put(sn.ID, sn)
		sn, _ = n.snapshots().Get(sn.ID)
		return nil
	})
	return sn, err
}

func (n snapshotStore) Snapshot(ctx context.Context, id int64) (model.StreamSnapshot, error) {
	var out model.StreamSnapshot
	err := n.s.Do(ctx, func() error {
		var ok bool
		if out, ok = n.snapshots().Get(id); !ok {
			return memtx.NotFound("snapshot", id)
		}
		return nil
	})
	return out, err
}

func (n snapshotStore) AddSnapshotEntries(ctx context.Context, id int64, entries []model.SnapshotEntry) error {
	return n.s.Do(ctx, func() error {
		for _, e := range entries {
			k := snapshotEntryKey{id, e.Key}
			if n.entries().Has(k) {
				return memtx.Conflict("snapshot entry", e.Key)
			}
			n.entries().Put(k, e)
		}
		return nil
	})
}

func (n snapshotStore) SnapshotEntries(ctx context.Context, id int64, after string, limit int) ([]model.SnapshotEntry, error) {
	if limit <= 0 {
		limit = kernel.DefaultLimit
	}
	var out []model.SnapshotEntry
	err := n.s.Do(ctx, func() error {
		out = n.entries().First(func(k snapshotEntryKey, _ model.SnapshotEntry) bool {
			return k.id == id && k.key > after
		}, func(a, b model.SnapshotEntry) int { return strings.Compare(a.Key, b.Key) }, limit)
		return nil
	})
	return out, err
}

func (n snapshotStore) ReadSnapshot(ctx context.Context, id int64, at time.Time) error {
	return n.s.Do(ctx, func() error {
		sn, ok := n.snapshots().Get(id)
		if !ok {
			return memtx.NotFound("snapshot", id)
		}
		sn.Read = at
		n.snapshots().Put(id, sn)
		return nil
	})
}

func (n snapshotStore) PruneSnapshots(ctx context.Context, before time.Time) (int64, error) {
	var pruned int64
	err := n.s.Do(ctx, func() error {
		pruned = n.drop(func(v model.StreamSnapshot) bool {
			last := v.Read
			if last.IsZero() {
				last = v.Taken
			}
			return last.Before(before)
		})
		return nil
	})
	return pruned, err
}
