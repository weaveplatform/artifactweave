package sql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

// Snapshots returns the stream snapshots this instance has taken for its
// receivers (decision 0021, Full sync).
func (s *Store) Snapshots() port.SnapshotStore { return snapshotStore{s} }

type snapshotStore struct{ s *Store }

const (
	snapshotCols      = "stream, receiver, sequence, taken_at, read_at"
	snapshotEntryCols = "snapshot_id, item_key, hlc_wall, hlc_logical, origin, deleted, payload, blob_sha256, blob_size"
)

func (n snapshotStore) CreateSnapshot(ctx context.Context, sn model.StreamSnapshot) (model.StreamSnapshot, error) {
	err := n.s.Tx(ctx, func(ctx context.Context) error {
		// A receiver reads one snapshot of a stream at a time: the one it
		// asks for now replaces those it abandoned.
		if _, err := n.s.Exec(ctx, "drop earlier snapshot entries",
			"DELETE FROM stream_snapshot_entries WHERE snapshot_id IN "+
				"(SELECT id FROM stream_snapshots WHERE receiver = ? AND stream = ?)", sn.Receiver, sn.Stream); err != nil {
			return err
		}
		if _, err := n.s.Exec(ctx, "drop earlier snapshots",
			"DELETE FROM stream_snapshots WHERE receiver = ? AND stream = ?", sn.Receiver, sn.Stream); err != nil {
			return err
		}
		id, err := n.s.InsertID(ctx, "create snapshot",
			dialect.Insert("stream_snapshots", strings.Split(snapshotCols, ", ")),
			sn.Stream, sn.Receiver, sn.Sequence, dialect.Micros(sn.Taken), dialect.Micros(sn.Read))
		sn.ID = id
		return err
	})
	if err != nil {
		return model.StreamSnapshot{}, err //nolint:wrapcheck // the callback names its failures
	}
	return kernel.Normalise(sn), nil
}

func (n snapshotStore) Snapshot(ctx context.Context, id int64) (model.StreamSnapshot, error) {
	var (
		sn          model.StreamSnapshot
		taken, read sql.NullInt64
	)
	err := n.s.Q(ctx).QueryRowContext(ctx, n.s.Dialect().Rebind(
		"SELECT id, "+snapshotCols+" FROM stream_snapshots WHERE id = ?"), id).
		Scan(&sn.ID, &sn.Stream, &sn.Receiver, &sn.Sequence, &taken, &read)
	if err != nil {
		return model.StreamSnapshot{}, n.s.Wrap(fmt.Sprintf("get snapshot %d", id), err)
	}
	sn.Taken, sn.Read = dialect.FromMicros(taken), dialect.FromMicros(read)
	return sn, nil
}

func (n snapshotStore) AddSnapshotEntries(ctx context.Context, id int64, entries []model.SnapshotEntry) error {
	return n.s.Tx(ctx, func(ctx context.Context) error { //nolint:wrapcheck // Exec names its failures
		for _, e := range entries {
			var payload any
			if len(e.Payload) > 0 {
				payload = e.Payload
			}
			if _, err := n.s.Exec(ctx, "add snapshot entry",
				dialect.Insert("stream_snapshot_entries", strings.Split(snapshotEntryCols, ", ")),
				id, e.Key, e.Time.Wall, int64(e.Time.Logical), e.Origin, e.Deleted, payload,
				e.BlobSHA256, e.BlobSize); err != nil {
				return err
			}
		}
		return nil
	})
}

func (n snapshotStore) SnapshotEntries(
	ctx context.Context, id int64, after string, limit int,
) ([]model.SnapshotEntry, error) {
	if limit <= 0 {
		limit = kernel.DefaultLimit
	}
	var out []model.SnapshotEntry
	err := n.s.Rows(ctx, "read snapshot entries",
		"SELECT "+snapshotEntryCols+" FROM stream_snapshot_entries WHERE snapshot_id = ? AND item_key > ? "+
			"ORDER BY item_key LIMIT ?", []any{id, after, limit},
		func(r *sql.Rows) error {
			var (
				e       model.SnapshotEntry
				sid     int64
				logical int64
			)
			err := r.Scan(&sid, &e.Key, &e.Time.Wall, &logical, &e.Origin, &e.Deleted, &e.Payload,
				&e.BlobSHA256, &e.BlobSize)
			e.Time.Logical = uint32(logical) // #nosec G115 -- written from a uint32
			if len(e.Payload) == 0 {
				e.Payload = nil
			}
			out = append(out, e)
			return err //nolint:wrapcheck // wrapped by Rows
		})
	return out, err
}

func (n snapshotStore) ReadSnapshot(ctx context.Context, id int64, at time.Time) error {
	return n.s.ExecOne(ctx, fmt.Sprintf("read snapshot %d", id), //nolint:wrapcheck // ExecOne names the snapshot
		"UPDATE stream_snapshots SET read_at = ? WHERE id = ?", dialect.Micros(at), id)
}

func (n snapshotStore) PruneSnapshots(ctx context.Context, before time.Time) (int64, error) {
	var pruned int64
	err := n.s.Tx(ctx, func(ctx context.Context) error {
		// A snapshot never read counts from when it was taken.
		const stale = "SELECT id FROM stream_snapshots WHERE COALESCE(read_at, taken_at, 0) < ?"
		cut := dialect.Micros(before)
		if _, err := n.s.Exec(ctx, "prune snapshot entries",
			"DELETE FROM stream_snapshot_entries WHERE snapshot_id IN ("+stale+")", cut); err != nil {
			return err
		}
		var err error
		pruned, err = n.s.Exec(ctx, "prune snapshots",
			"DELETE FROM stream_snapshots WHERE COALESCE(read_at, taken_at, 0) < ?", cut)
		return err
	})
	return pruned, err //nolint:wrapcheck // the callback names its failures
}
