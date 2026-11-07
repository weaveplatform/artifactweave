package sqlcommon

import (
	"context"
	"database/sql"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Outbox implements store.Store.
func (s *Store) Outbox() store.OutboxStore { return outboxStore{s} }

type outboxStore struct{ s *Store }

const outboxCols = "origin, stream, seq, hlc_wall, hlc_logical, event_type, payload, blob_sha256, blob_size, created_at"

func scanOutbox(r dialect.PeersScanner) (types.OutboxEntry, error) {
	var (
		e       types.OutboxEntry
		logical int64
		created sql.NullInt64
	)
	err := r.Scan(&e.Origin, &e.Stream, &e.Seq, &e.Time.Wall, &logical, &e.EventType, &e.Payload,
		&e.BlobSHA256, &e.BlobSize, &created)
	e.Time.Logical = uint32(logical) // #nosec G115 -- written from a uint32
	e.Created = dialect.FromMicros(created)
	if len(e.Payload) == 0 {
		e.Payload = nil
	}
	return e, err //nolint:wrapcheck // wrapped by the caller
}

func (o outboxStore) Append(ctx context.Context, e types.OutboxEntry) (types.OutboxEntry, error) {
	err := o.s.Tx(ctx, func(ctx context.Context) error {
		// The sequence row serialises appenders: the UPDATE holds its lock
		// until commit, so two transactions never draw the same number and a
		// rolled-back append returns its number with it.
		if _, err := o.s.Exec(ctx, "append outbox", InsertIgnore("outbox_sequences",
			[]string{"origin", "stream", "last_seq"}, []string{"origin", "stream"}),
			e.Origin, e.Stream, 0); err != nil {
			return err
		}
		if err := o.s.Q(ctx).
			QueryRowContext(ctx, o.s.Dialect().Rebind("UPDATE outbox_sequences SET last_seq = last_seq + 1 "+
				"WHERE origin = ? AND stream = ? RETURNING last_seq"), e.Origin, e.Stream).
			Scan(&e.Seq); err != nil {
			return o.s.Wrap("append outbox", err)
		}
		_, err := o.s.Exec(
			ctx,
			"append outbox",
			Insert("outbox", strings.Split(outboxCols, ", ")),
			e.Origin,
			e.Stream,
			e.Seq,
			e.Time.Wall,
			int64(e.Time.Logical),
			e.EventType,
			peersBytes(e.Payload),
			e.BlobSHA256,
			e.BlobSize,
			dialect.Micros(e.Created),
		)
		return err
	})
	if err != nil {
		return types.OutboxEntry{}, err
	}
	return store.Normalise(e), nil
}

// peersBytes stores an empty byte slice as NULL.
func peersBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func (o outboxStore) Read(
	ctx context.Context, origin, stream string, after int64, limit int,
) ([]types.OutboxEntry, error) {
	var out []types.OutboxEntry
	err := o.s.Rows(ctx, "read outbox", "SELECT "+outboxCols+" FROM outbox "+
		"WHERE origin = ? AND stream = ? AND seq > ? ORDER BY seq LIMIT ?",
		[]any{origin, stream, after, dialect.LimitOr(limit)},
		func(r *sql.Rows) error {
			e, err := scanOutbox(r)
			out = append(out, e)
			return err
		})
	return out, err
}

func (o outboxStore) LastSeq(ctx context.Context, origin, stream string) (int64, error) {
	var seq int64
	err := o.s.Q(ctx).QueryRowContext(ctx, o.s.Dialect().Rebind(
		"SELECT COALESCE(MAX(last_seq), 0) FROM outbox_sequences WHERE origin = ? AND stream = ?"),
		origin, stream).Scan(&seq)
	return seq, o.s.Wrap("outbox last sequence", err)
}

func (o outboxStore) Streams(ctx context.Context, origin string) ([]string, error) {
	var out []string
	err := o.s.Rows(ctx, "list outbox streams",
		"SELECT stream FROM outbox_sequences WHERE origin = ? ORDER BY stream", []any{origin},
		func(r *sql.Rows) error {
			var stream string
			err := r.Scan(&stream)
			out = append(out, stream)
			return err //nolint:wrapcheck // wrapped by Rows
		})
	return out, err
}

func (o outboxStore) Prune(ctx context.Context, origin, stream string, upTo int64) (int64, error) {
	return o.s.Exec(ctx, "prune outbox",
		"DELETE FROM outbox WHERE origin = ? AND stream = ? AND seq <= ?", origin, stream, upTo)
}

const peerCursorCols = "peer, stream, acked_seq, last_sent_at, last_error"

func scanPeerCursor(r dialect.PeersScanner) (types.PeerCursor, error) {
	var (
		c    types.PeerCursor
		sent sql.NullInt64
	)
	err := r.Scan(&c.PeerInstanceID, &c.Stream, &c.AckedSeq, &sent, &c.LastError)
	c.LastSent = dialect.FromMicros(sent)
	return c, err //nolint:wrapcheck // wrapped by the caller
}

func (o outboxStore) PutCursor(ctx context.Context, c types.PeerCursor) error {
	_, err := o.s.Exec(ctx, "put peer cursor",
		Upsert("peer_cursors", strings.Split(peerCursorCols, ", "), []string{"peer", "stream"}),
		c.PeerInstanceID, c.Stream, c.AckedSeq, dialect.Micros(c.LastSent), c.LastError)
	return err
}

func (o outboxStore) Cursor(ctx context.Context, peer, stream string) (types.PeerCursor, error) {
	c, err := scanPeerCursor(o.s.Q(ctx).QueryRowContext(ctx, o.s.Dialect().Rebind(
		"SELECT "+peerCursorCols+" FROM peer_cursors WHERE peer = ? AND stream = ?"), peer, stream))
	if err != nil {
		return types.PeerCursor{}, o.s.Wrap("get peer cursor "+peer+" "+stream, err)
	}
	return c, nil
}

func (o outboxStore) Cursors(ctx context.Context, peer string) ([]types.PeerCursor, error) {
	where, args := "", []any(nil)
	if peer != "" {
		where, args = " WHERE peer = ?", []any{peer}
	}
	var out []types.PeerCursor
	err := o.s.Rows(ctx, "list peer cursors",
		"SELECT "+peerCursorCols+" FROM peer_cursors"+where+" ORDER BY peer, stream", args,
		func(r *sql.Rows) error {
			c, err := scanPeerCursor(r)
			out = append(out, c)
			return err
		})
	return out, err
}

func (o outboxStore) DeleteCursors(ctx context.Context, peer string) error {
	_, err := o.s.Exec(ctx, "delete peer cursors", "DELETE FROM peer_cursors WHERE peer = ?", peer)
	return err
}

func (o outboxStore) Applied(
	ctx context.Context,
	origin, stream string,
) (types.AppliedMarker, error) {
	m := types.AppliedMarker{Origin: origin, Stream: stream}
	err := o.s.Q(ctx).QueryRowContext(ctx, o.s.Dialect().Rebind(
		"SELECT COALESCE(MAX(last_seq), 0) FROM applied_markers WHERE origin = ? AND stream = ?"),
		origin, stream).Scan(&m.LastSeq)
	return m, o.s.Wrap("get applied marker", err)
}

func (o outboxStore) SetApplied(ctx context.Context, m types.AppliedMarker) error {
	_, err := o.s.Exec(
		ctx,
		"set applied marker",
		Upsert(
			"applied_markers",
			[]string{"origin", "stream", "last_seq"},
			[]string{"origin", "stream"},
		),
		m.Origin,
		m.Stream,
		m.LastSeq,
	)
	return err
}

const failedFetchCols = "stream, sha256, sha1, repo_key, first_failed_at, failures, last_error"

func scanFailedFetch(r dialect.PeersScanner) (types.FailedBlobFetch, error) {
	var (
		f     types.FailedBlobFetch
		first sql.NullInt64
	)
	err := r.Scan(&f.Stream, &f.SHA256, &f.SHA1, &f.RepoKey, &first, &f.Failures, &f.LastError)
	f.FirstFailed = dialect.FromMicros(first)
	return f, err //nolint:wrapcheck // wrapped by the caller
}

func (o outboxStore) RecordFailedFetch(
	ctx context.Context,
	f types.FailedBlobFetch,
) (types.FailedBlobFetch, error) {
	var out types.FailedBlobFetch
	err := o.s.Tx(ctx, func(ctx context.Context) error {
		// Insert-if-absent with no failures, then count this one, so two
		// first failures racing on PostgreSQL both land on one row.
		if _, err := o.s.Exec(
			ctx,
			"record failed fetch",
			InsertIgnore(
				"failed_blob_fetches",
				strings.Split(failedFetchCols, ", "),
				[]string{"stream", "sha256"},
			),
			f.Stream,
			f.SHA256,
			f.SHA1,
			f.RepoKey,
			dialect.Micros(f.FirstFailed),
			0,
			f.LastError,
		); err != nil {
			return err
		}
		if err := o.s.ExecOne(ctx, "record failed fetch", "UPDATE failed_blob_fetches "+
			"SET failures = failures + 1, last_error = ? WHERE stream = ? AND sha256 = ?",
			f.LastError, f.Stream, f.SHA256); err != nil {
			return err
		}
		var err error
		out, err = scanFailedFetch(o.s.Q(ctx).QueryRowContext(ctx, o.s.Dialect().Rebind(
			"SELECT "+failedFetchCols+" FROM failed_blob_fetches WHERE stream = ? AND sha256 = ?",
		), f.Stream, f.SHA256))
		return o.s.Wrap("record failed fetch", err)
	})
	if err != nil {
		return types.FailedBlobFetch{}, err
	}
	return out, nil
}

func (o outboxStore) FailedFetches(
	ctx context.Context,
	stream string,
) ([]types.FailedBlobFetch, error) {
	where, args := "", []any(nil)
	if stream != "" {
		where, args = " WHERE stream = ?", []any{stream}
	}
	var out []types.FailedBlobFetch
	err := o.s.Rows(
		ctx,
		"list failed fetches",
		"SELECT "+failedFetchCols+" FROM failed_blob_fetches"+where+" ORDER BY stream, sha256",
		args,
		func(r *sql.Rows) error {
			f, err := scanFailedFetch(r)
			out = append(out, f)
			return err
		},
	)
	return out, err
}

func (o outboxStore) ClearFailedFetch(ctx context.Context, stream, sha256 string) error {
	return o.s.ExecOne(ctx, "clear failed fetch "+stream+" "+sha256,
		"DELETE FROM failed_blob_fetches WHERE stream = ? AND sha256 = ?", stream, sha256)
}
