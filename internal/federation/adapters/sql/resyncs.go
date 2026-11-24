package sql

import (
	"context"
	"database/sql"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

// Resyncs returns the records of receivers behind an origin's pruned floor
// (decision 0021, Ordered outbox).
func (s *Store) Resyncs() port.StreamResyncStore { return resyncStore{s} }

type resyncStore struct{ s *Store }

const resyncCols = "origin, receiver, stream, position, flagged_at, run"

func (r resyncStore) Flag(ctx context.Context, f model.StreamResync) error {
	_, err := r.s.Exec(ctx, "flag stream resync",
		dialect.Upsert("stream_resyncs", strings.Split(resyncCols, ", "), []string{"origin", "receiver", "stream"}),
		f.Origin, f.Receiver, f.Stream, f.Position, dialect.Micros(f.Flagged), f.Run)
	return err
}

func scanResync(r dialect.PeersScanner) (model.StreamResync, error) {
	var (
		f       model.StreamResync
		flagged sql.NullInt64
	)
	err := r.Scan(&f.Origin, &f.Receiver, &f.Stream, &f.Position, &flagged, &f.Run)
	f.Flagged = dialect.FromMicros(flagged)
	return f, err //nolint:wrapcheck // wrapped by the caller
}

func (r resyncStore) Get(ctx context.Context, origin, receiver, stream string) (model.StreamResync, error) {
	f, err := scanResync(r.s.Q(ctx).QueryRowContext(ctx, r.s.Dialect().Rebind(
		"SELECT "+resyncCols+" FROM stream_resyncs WHERE origin = ? AND receiver = ? AND stream = ?"),
		origin, receiver, stream))
	if err != nil {
		return model.StreamResync{}, r.s.Wrap("get stream resync", err)
	}
	return f, nil
}

func (r resyncStore) Resyncs(ctx context.Context, instanceID string) ([]model.StreamResync, error) {
	where, args := "", []any(nil)
	if instanceID != "" {
		where, args = " WHERE origin = ? OR receiver = ?", []any{instanceID, instanceID}
	}
	var out []model.StreamResync
	err := r.s.Rows(ctx, "list stream resyncs",
		"SELECT "+resyncCols+" FROM stream_resyncs"+where+" ORDER BY origin, receiver, stream", args,
		func(rows *sql.Rows) error {
			f, err := scanResync(rows)
			out = append(out, f)
			return err
		})
	return out, err
}

func (r resyncStore) Clear(ctx context.Context, origin, receiver, stream string) error {
	_, err := r.s.Exec(ctx, "clear stream resync",
		"DELETE FROM stream_resyncs WHERE origin = ? AND receiver = ? AND stream = ?", origin, receiver, stream)
	return err
}

func (r resyncStore) DeletePeer(ctx context.Context, instanceID string) error {
	_, err := r.s.Exec(ctx, "delete stream resyncs",
		"DELETE FROM stream_resyncs WHERE origin = ? OR receiver = ?", instanceID, instanceID)
	return err
}
